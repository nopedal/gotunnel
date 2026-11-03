package server

import (
	"bufio"
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/rs/zerolog"
	"golang.org/x/crypto/acme/autocert"
	"golang.org/x/time/rate"

	"github.com/nopedal/gotunnel/internal/proto"
	tunnelpkg "github.com/nopedal/gotunnel/internal/tunnel"
)

// Server is the tunnel server.
type Server struct {
	cfg      *Config
	log      zerolog.Logger
	registry *tunnelpkg.Registry

	rateMu    sync.Mutex
	rateLimit map[string]*rate.Limiter
}

// New creates a new tunnel server.
func New(cfg *Config, log zerolog.Logger) *Server {
	return &Server{
		cfg:       cfg,
		log:       log,
		registry:  tunnelpkg.NewRegistry(),
		rateLimit: make(map[string]*rate.Limiter),
	}
}

// Run starts all listeners and blocks until the context is cancelled, which
// is a normal shutdown and returns nil, or a listener fails.
func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 3)

	// The HTTPS and control listeners share one certificate manager:
	// tls-alpn-01 challenges are answered on the HTTPS port, so the manager
	// requesting a certificate must be the one serving that port.
	var certs *autocert.Manager
	var controlTLS *tls.Config
	if s.cfg.TLSEnabled {
		certs = s.certManager()
		controlTLS = &tls.Config{
			GetCertificate: certs.GetCertificate,
			MinVersion:     tls.VersionTLS12,
		}
	}

	go func() { errCh <- s.listenControl(ctx, controlTLS) }()
	go func() { errCh <- s.listenHTTP(ctx) }()
	go s.pruneLimitersLoop(ctx, time.Minute)

	if s.cfg.TLSEnabled {
		go func() { errCh <- s.listenHTTPS(ctx, certs) }()
	}

	s.log.Info().
		Str("domain", s.cfg.Domain).
		Int("control_port", s.cfg.ControlPort).
		Int("http_port", s.cfg.HTTPPort).
		Int("https_port", s.cfg.HTTPSPort).
		Bool("tls", s.cfg.TLSEnabled).
		Msg("tunnel server started")

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return nil
	}
}

// listenControl accepts client connections. When tlsCfg is non-nil the
// connections are TLS, so the auth token and tunnel traffic are encrypted.
func (s *Server) listenControl(ctx context.Context, tlsCfg *tls.Config) error {
	addr := fmt.Sprintf(":%d", s.cfg.ControlPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("control listen: %w", err)
	}
	if tlsCfg != nil {
		ln = tls.NewListener(ln, tlsCfg)
	}
	defer ln.Close()

	s.log.Info().Str("addr", addr).Bool("tls", tlsCfg != nil).Msg("control listener ready")

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				s.log.Error().Err(err).Msg("control accept error")
				continue
			}
		}
		go s.handleControl(ctx, conn)
	}
}

func (s *Server) getLimiter(ip string) *rate.Limiter {
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	if lim, ok := s.rateLimit[ip]; ok {
		return lim
	}
	lim := rate.NewLimiter(rate.Every(12*time.Second), 5)
	s.rateLimit[ip] = lim
	return lim
}

// pruneLimiters forgets limiters that have refilled to their full burst.
// A new limiter starts full, so dropping them changes nothing for those
// IPs, but it keeps the map from growing with every address that has ever
// connected to the control port.
func (s *Server) pruneLimiters(now time.Time) {
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	for ip, lim := range s.rateLimit {
		if lim.TokensAt(now) >= float64(lim.Burst()) {
			delete(s.rateLimit, ip)
		}
	}
}

func (s *Server) pruneLimitersLoop(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.pruneLimiters(now)
		}
	}
}

func (s *Server) handleControl(_ context.Context, conn net.Conn) {
	remoteIP := conn.RemoteAddr().(*net.TCPAddr).IP.String()

	if !s.getLimiter(remoteIP).Allow() {
		s.log.Warn().Str("ip", remoteIP).Msg("rate limited")
		conn.Close()
		return
	}

	if s.registry.Count() >= s.cfg.MaxClients {
		s.log.Warn().Str("ip", remoteIP).Msg("max clients reached")
		conn.Close()
		return
	}

	session, err := yamux.Server(conn, tunnelpkg.YamuxConfig())
	if err != nil {
		s.log.Error().Err(err).Str("ip", remoteIP).Msg("yamux session error")
		conn.Close()
		return
	}

	// Accept the control stream (stream 0)
	ctrlStream, err := session.Accept()
	if err != nil {
		s.log.Error().Err(err).Str("ip", remoteIP).Msg("accept control stream")
		session.Close()
		return
	}

	// Authenticate
	msgType, data, err := proto.ReadMessage(ctrlStream)
	if err != nil || msgType != proto.TypeAuthRequest {
		s.log.Warn().Str("ip", remoteIP).Msg("invalid auth message")
		session.Close()
		return
	}

	var authReq proto.AuthRequest
	if err := proto.Decode(data, &authReq); err != nil {
		session.Close()
		return
	}

	if subtle.ConstantTimeCompare([]byte(authReq.Token), []byte(s.cfg.Token)) != 1 {
		proto.WriteMessage(ctrlStream, proto.TypeAuthResponse, &proto.AuthResponse{
			OK: false, Error: "invalid token",
		})
		s.log.Warn().Str("ip", remoteIP).Msg("auth failed")
		session.Close()
		return
	}

	proto.WriteMessage(ctrlStream, proto.TypeAuthResponse, &proto.AuthResponse{OK: true})
	s.log.Info().Str("ip", remoteIP).Msg("client authenticated")

	// Read tunnel request
	msgType, data, err = proto.ReadMessage(ctrlStream)
	if err != nil || msgType != proto.TypeTunnelRequest {
		session.Close()
		return
	}

	var tunReq proto.TunnelRequest
	if err := proto.Decode(data, &tunReq); err != nil {
		session.Close()
		return
	}

	subdomain := tunReq.Subdomain
	if subdomain == "" {
		subdomain = tunnelpkg.GenerateSlug()
	} else if subdomain, err = tunnelpkg.NormalizeSubdomain(subdomain); err != nil {
		proto.WriteMessage(ctrlStream, proto.TypeTunnelResponse, &proto.TunnelResponse{
			OK: false, Error: err.Error(),
		})
		session.Close()
		return
	}

	tun := &tunnelpkg.Tunnel{
		ID:        subdomain,
		Subdomain: subdomain,
		Mode:      tunReq.Mode,
		Session:   session,
		CreatedAt: time.Now(),
		RemoteIP:  remoteIP,
	}

	if err := s.registry.Register(tun); err != nil {
		proto.WriteMessage(ctrlStream, proto.TypeTunnelResponse, &proto.TunnelResponse{
			OK: false, Error: err.Error(),
		})
		session.Close()
		return
	}

	scheme := "http"
	if s.cfg.TLSEnabled {
		scheme = "https"
	}
	publicURL := fmt.Sprintf("%s://%s.%s", scheme, subdomain, s.cfg.Domain)

	proto.WriteMessage(ctrlStream, proto.TypeTunnelResponse, &proto.TunnelResponse{
		OK: true, URL: publicURL, Subdomain: subdomain,
	})

	s.log.Info().
		Str("subdomain", subdomain).
		Str("url", publicURL).
		Str("mode", tunReq.Mode).
		Str("ip", remoteIP).
		Msg("tunnel established")

	// Watch for session close
	go func() {
		<-session.CloseChan()
		s.registry.Deregister(subdomain)
		s.log.Info().Str("subdomain", subdomain).Str("ip", remoteIP).Msg("tunnel closed")
	}()
}

func (s *Server) listenHTTP(ctx context.Context) error {
	addr := fmt.Sprintf(":%d", s.cfg.HTTPPort)
	srv := &http.Server{
		Addr:         addr,
		Handler:      s.httpHandler(),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
	go func() {
		<-ctx.Done()
		srv.Shutdown(context.Background())
	}()
	s.log.Info().Str("addr", addr).Msg("http listener ready")
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}

// certManager returns the ACME manager for the tunnel domain and its
// subdomains. Certificates are only requested for the base domain and for
// subdomains with an active tunnel: the wildcard DNS record sends every name
// to this server, so allowing any subdomain would let anyone burn through
// the domain's Let's Encrypt quota by connecting with random SNI names.
func (s *Server) certManager() *autocert.Manager {
	return &autocert.Manager{
		Prompt: autocert.AcceptTOS,
		Cache:  autocert.DirCache(s.cfg.CertDir),
		HostPolicy: func(_ context.Context, host string) error {
			if host == s.cfg.Domain {
				return nil
			}
			if sub, ok := strings.CutSuffix(host, "."+s.cfg.Domain); ok {
				if _, active := s.registry.Lookup(sub); active {
					return nil
				}
			}
			return fmt.Errorf("host %q not allowed", host)
		},
	}
}

func (s *Server) listenHTTPS(ctx context.Context, m *autocert.Manager) error {
	addr := fmt.Sprintf(":%d", s.cfg.HTTPSPort)
	srv := &http.Server{
		Addr:         addr,
		Handler:      s.httpHandler(),
		TLSConfig:    httpsTLSConfig(m),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
	go func() {
		<-ctx.Done()
		srv.Shutdown(context.Background())
	}()
	s.log.Info().Str("addr", addr).Msg("https listener ready")
	if err := srv.ListenAndServeTLS("", ""); err != http.ErrServerClosed {
		return err
	}
	return nil
}

// httpsTLSConfig builds the public HTTPS listener's TLS config from
// m.TLSConfig, which advertises the acme-tls/1 protocol. Without it the
// tls-alpn-01 challenge handshake is rejected during ALPN negotiation and
// autocert can never obtain a certificate.
func httpsTLSConfig(m *autocert.Manager) *tls.Config {
	cfg := m.TLSConfig()
	cfg.MinVersion = tls.VersionTLS12
	return cfg
}

func (s *Server) httpHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := strings.ToLower(r.Host)
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}

		// API endpoints on bare domain
		if host == s.cfg.Domain {
			switch r.URL.Path {
			case "/health":
				s.handleHealth(w, r)
			case "/api/tunnels":
				// Lists every client's IP address, so it is not public.
				if !s.authorized(r) {
					w.Header().Set("WWW-Authenticate", `Bearer realm="tunnel"`)
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				s.handleAPITunnels(w, r)
			default:
				http.Error(w, "not found", http.StatusNotFound)
			}
			return
		}

		// Extract subdomain
		subdomain := strings.TrimSuffix(host, "."+s.cfg.Domain)
		if subdomain == host || subdomain == "" {
			http.Error(w, "invalid host", http.StatusBadRequest)
			return
		}

		tun, ok := s.registry.Lookup(subdomain)
		if !ok {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(w, "tunnel %q not found — it may have disconnected\n", subdomain)
			return
		}

		s.proxyToClient(w, r, tun)
	})
}

func (s *Server) proxyToClient(w http.ResponseWriter, r *http.Request, tun *tunnelpkg.Tunnel) {
	stream, err := tun.Session.Open()
	if err != nil {
		s.log.Error().Err(err).Str("subdomain", tun.Subdomain).Msg("open stream failed")
		http.Error(w, "tunnel unavailable", http.StatusBadGateway)
		return
	}
	defer stream.Close()

	// Forward the HTTP request over the stream
	if err := r.Write(stream); err != nil {
		s.log.Error().Err(err).Str("subdomain", tun.Subdomain).Msg("write request to stream")
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}

	// Read the HTTP response from the stream
	resp, err := http.ReadResponse(bufio.NewReader(stream), r)
	if err != nil {
		s.log.Error().Err(err).Str("subdomain", tun.Subdomain).Msg("read response from stream")
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Copy response headers
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// authorized reports whether r carries the server token as a bearer token.
func (s *Server) authorized(r *http.Request) bool {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.Token)) == 1
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "ok",
		"tunnels": s.registry.Count(),
	})
}

func (s *Server) handleAPITunnels(w http.ResponseWriter, _ *http.Request) {
	tunnels := s.registry.List()
	type info struct {
		Subdomain string `json:"subdomain"`
		URL       string `json:"url"`
		Mode      string `json:"mode"`
		RemoteIP  string `json:"remote_ip"`
		CreatedAt string `json:"created_at"`
	}
	out := make([]info, 0, len(tunnels))
	scheme := "http"
	if s.cfg.TLSEnabled {
		scheme = "https"
	}
	for _, t := range tunnels {
		out = append(out, info{
			Subdomain: t.Subdomain,
			URL:       fmt.Sprintf("%s://%s.%s", scheme, t.Subdomain, s.cfg.Domain),
			Mode:      t.Mode,
			RemoteIP:  t.RemoteIP,
			CreatedAt: t.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"tunnels": out, "count": len(out)})
}
