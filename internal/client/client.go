package client

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/hashicorp/yamux"

	"github.com/nopedal/gotunnel/internal/proto"
	tunnelpkg "github.com/nopedal/gotunnel/internal/tunnel"
)

const version = "0.1.0"

// Client is the tunnel client.
type Client struct {
	cfg     *Config
	session *yamux.Session
}

// New creates a new tunnel client.
func New(cfg *Config) *Client {
	return &Client{cfg: cfg}
}

// Connect establishes the tunnel and blocks, serving requests and
// reconnecting as needed, until ctx is cancelled.
func (c *Client) Connect(ctx context.Context) error {
	var backoff time.Duration
	maxBackoff := 30 * time.Second

	for {
		err := c.connectOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}

		if backoff == 0 {
			backoff = time.Second
		} else {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}

		fmt.Fprintf(os.Stderr, "\n  connection lost: %v\n", err)
		fmt.Fprintf(os.Stderr, "  reconnecting in %s...\n\n", backoff)

		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return nil
		}
	}
}

func (c *Client) connectOnce(ctx context.Context) error {
	addr := c.cfg.Server
	if !strings.Contains(addr, ":") {
		addr = addr + ":7000"
	}

	dialer := &net.Dialer{Timeout: 10 * time.Second}

	// Plain TCP only when explicitly requested, for servers running with
	// tls_enabled: false. Never fall back to it after a failed TLS
	// handshake: that would send the token in cleartext to whoever made
	// the handshake fail.
	if c.cfg.Insecure {
		conn, err := dialer.DialContext(ctx, "tcp", addr)
		if err != nil {
			return fmt.Errorf("connect: %w", err)
		}
		return c.runSession(ctx, conn)
	}

	host := c.cfg.Server
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}

	conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{
		ServerName: host,
		MinVersion: tls.VersionTLS12,
	})
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	return c.runSession(ctx, conn)
}

func (c *Client) runSession(ctx context.Context, conn net.Conn) error {
	session, err := yamux.Client(conn, tunnelpkg.YamuxConfig())
	if err != nil {
		conn.Close()
		return fmt.Errorf("yamux: %w", err)
	}
	defer session.Close()
	c.session = session

	// Open control stream (stream 0)
	ctrl, err := session.Open()
	if err != nil {
		return fmt.Errorf("open control stream: %w", err)
	}

	// Authenticate
	if err := proto.WriteMessage(ctrl, proto.TypeAuthRequest, &proto.AuthRequest{
		Token:   c.cfg.Token,
		Version: version,
	}); err != nil {
		return fmt.Errorf("send auth: %w", err)
	}

	msgType, data, err := proto.ReadMessage(ctrl)
	if err != nil {
		return fmt.Errorf("read auth response: %w", err)
	}
	if msgType != proto.TypeAuthResponse {
		return fmt.Errorf("unexpected message type: %d", msgType)
	}

	var authResp proto.AuthResponse
	if err := proto.Decode(data, &authResp); err != nil {
		return fmt.Errorf("decode auth: %w", err)
	}
	if !authResp.OK {
		return fmt.Errorf("auth failed: %s", authResp.Error)
	}

	// Request tunnel
	mode := c.cfg.Mode
	if mode == "" {
		mode = "http"
	}
	if err := proto.WriteMessage(ctrl, proto.TypeTunnelRequest, &proto.TunnelRequest{
		Subdomain: c.cfg.Subdomain,
		Mode:      mode,
		LocalPort: c.cfg.Port,
	}); err != nil {
		return fmt.Errorf("send tunnel request: %w", err)
	}

	msgType, data, err = proto.ReadMessage(ctrl)
	if err != nil {
		return fmt.Errorf("read tunnel response: %w", err)
	}
	if msgType != proto.TypeTunnelResponse {
		return fmt.Errorf("unexpected message type: %d", msgType)
	}

	var tunResp proto.TunnelResponse
	if err := proto.Decode(data, &tunResp); err != nil {
		return fmt.Errorf("decode tunnel response: %w", err)
	}
	if !tunResp.OK {
		return fmt.Errorf("tunnel rejected: %s", tunResp.Error)
	}

	printBanner(tunResp.URL, c.cfg.Port, mode)

	// Accept streams from the server and forward to localhost
	doneCh := make(chan struct{})
	go func() {
		<-ctx.Done()
		session.Close()
	}()

	go func() {
		<-session.CloseChan()
		close(doneCh)
	}()

	for {
		stream, err := session.Accept()
		if err != nil {
			select {
			case <-doneCh:
				return fmt.Errorf("session closed")
			default:
				return fmt.Errorf("accept stream: %w", err)
			}
		}
		go c.handleStream(stream)
	}
}

func (c *Client) handleStream(stream net.Conn) {
	defer stream.Close()

	if c.cfg.Mode == "tcp" {
		c.handleTCPStream(stream)
		return
	}

	c.handleHTTPStream(stream)
}

func (c *Client) handleHTTPStream(stream net.Conn) {
	start := time.Now()

	// Read HTTP request from stream
	req, err := http.ReadRequest(bufio.NewReader(stream))
	if err != nil {
		fmt.Fprintf(os.Stderr, "  read request: %v\n", err)
		return
	}

	method := req.Method
	path := req.URL.Path

	// Forward to localhost
	localAddr := fmt.Sprintf("127.0.0.1:%d", c.cfg.Port)
	req.URL.Scheme = "http"
	req.URL.Host = localAddr
	req.RequestURI = ""

	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		elapsed := time.Since(start)
		fmt.Fprintf(os.Stderr, "  %-7s %-40s %s  %s\n", method, truncPath(path), "502", fmtDuration(elapsed))

		// Send a 502 back
		errResp := &http.Response{
			StatusCode: http.StatusBadGateway,
			ProtoMajor: 1,
			ProtoMinor: 1,
			Header:     http.Header{"Content-Type": {"text/plain"}},
			Body:       io.NopCloser(strings.NewReader("could not reach local server\n")),
		}
		errResp.Write(stream)
		return
	}
	defer resp.Body.Close()

	elapsed := time.Since(start)
	statusColor := colorForStatus(resp.StatusCode)
	fmt.Fprintf(os.Stderr, "  %-7s %-40s %s%d\033[0m  %s\n", method, truncPath(path), statusColor, resp.StatusCode, fmtDuration(elapsed))

	resp.Write(stream)
}

func (c *Client) handleTCPStream(stream net.Conn) {
	localAddr := fmt.Sprintf("127.0.0.1:%d", c.cfg.Port)
	local, err := net.DialTimeout("tcp", localAddr, 5*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  tcp connect error: %v\n", err)
		return
	}
	defer local.Close()

	tunnelpkg.Relay(stream, local)
}

func printBanner(url string, port int, mode string) {
	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "  \033[1mtunnel\033[0m\n")
	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "  status     \033[32monline\033[0m\n")
	fmt.Fprintf(os.Stderr, "  forwarding \033[36m%s\033[0m → \033[33mlocalhost:%d\033[0m\n", url, port)
	fmt.Fprintf(os.Stderr, "  mode       %s\n", mode)
	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "  \033[2m%-7s %-40s %s  %s\033[0m\n", "METHOD", "PATH", "STATUS", "TIME")
	fmt.Fprintf(os.Stderr, "  \033[2m%s\033[0m\n", strings.Repeat("─", 68))
}

func colorForStatus(code int) string {
	switch {
	case code >= 200 && code < 300:
		return "\033[32m" // green
	case code >= 300 && code < 400:
		return "\033[36m" // cyan
	case code >= 400 && code < 500:
		return "\033[33m" // yellow
	default:
		return "\033[31m" // red
	}
}

func truncPath(path string) string {
	if len(path) > 40 {
		return path[:37] + "..."
	}
	return path
}

func fmtDuration(d time.Duration) string {
	if d < time.Millisecond {
		return fmt.Sprintf("%dµs", d.Microseconds())
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}
