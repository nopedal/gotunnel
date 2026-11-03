package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/rs/zerolog"
	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"

	"github.com/nopedal/gotunnel/internal/proto"
	tunnelpkg "github.com/nopedal/gotunnel/internal/tunnel"
)

// selfSignedCert returns a throwaway certificate for handshake tests.
func selfSignedCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "tunnel.test"},
		DNSNames:     []string{"tunnel.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// Let's Encrypt validates tls-alpn-01 by connecting to :443 and offering only
// the acme-tls/1 protocol. If the HTTPS listener does not advertise it, the
// handshake fails before autocert can present the challenge certificate, so
// no certificate is ever issued.
func TestHTTPSTLSConfigNegotiatesACMEALPN(t *testing.T) {
	cfg := httpsTLSConfig(&autocert.Manager{Prompt: autocert.AcceptTOS})

	// Stand in for autocert's challenge certificate; ALPN negotiation is
	// what is under test.
	cert := selfSignedCert(t)
	cfg.GetCertificate = func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		return &cert, nil
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.NotFoundHandler(), TLSConfig: cfg}
	go srv.ServeTLS(ln, "", "")
	defer srv.Close()

	conn, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{
		ServerName:         "tunnel.test",
		NextProtos:         []string{acme.ALPNProto},
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatalf("tls-alpn-01 handshake failed: %v", err)
	}
	defer conn.Close()

	if got := conn.ConnectionState().NegotiatedProtocol; got != acme.ALPNProto {
		t.Fatalf("negotiated protocol = %q, want %q", got, acme.ALPNProto)
	}
}

// freePort returns a TCP port that was free a moment ago.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestControlListenerUsesTLS(t *testing.T) {
	port := freePort(t)

	s := New(&Config{
		Domain:      "tunnel.test",
		Token:       "secret",
		ControlPort: port,
		MaxClients:  1,
		TLSEnabled:  true,
	}, zerolog.Nop())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cert := selfSignedCert(t)
	go s.listenControl(ctx, &tls.Config{Certificates: []tls.Certificate{cert}})

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	var conn *tls.Conn
	var err error
	for deadline := time.Now().Add(2 * time.Second); ; {
		conn, err = tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true})
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("TLS dial to control port: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	session, err := yamux.Client(conn, tunnelpkg.YamuxConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	ctrl, err := session.Open()
	if err != nil {
		t.Fatal(err)
	}

	if err := proto.WriteMessage(ctrl, proto.TypeAuthRequest, &proto.AuthRequest{Token: "secret"}); err != nil {
		t.Fatal(err)
	}
	msgType, data, err := proto.ReadMessage(ctrl)
	if err != nil {
		t.Fatalf("read auth response: %v", err)
	}
	var resp proto.AuthResponse
	if err := proto.Decode(data, &resp); err != nil {
		t.Fatal(err)
	}
	if msgType != proto.TypeAuthResponse || !resp.OK {
		t.Fatalf("auth over TLS failed: type=%d resp=%+v", msgType, resp)
	}
}

// SIGINT/SIGTERM cancel the context; that is a clean shutdown, not an error
// (otherwise the CLI prints "Error: context canceled" and exits 1).
func TestRunReturnsNilOnShutdown(t *testing.T) {
	httpPort := freePort(t)
	s := New(&Config{
		Domain:      "tunnel.test",
		Token:       "secret",
		ControlPort: freePort(t),
		HTTPPort:    httpPort,
		MaxClients:  1,
	}, zerolog.Nop())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	healthURL := fmt.Sprintf("http://127.0.0.1:%d/health", httpPort)
	for deadline := time.Now().Add(2 * time.Second); ; {
		req, _ := http.NewRequest(http.MethodGet, healthURL, nil)
		req.Host = "tunnel.test"
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not come up: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v after cancellation, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

// The wildcard DNS record points every name under the domain at the server,
// so the ACME host policy must not request a certificate for arbitrary
// names: anyone could exhaust the domain's Let's Encrypt quota by sending
// TLS handshakes with random SNI values.
func TestCertHostPolicy(t *testing.T) {
	s := New(&Config{Domain: "tunnel.test", Token: "secret"}, zerolog.Nop())
	if err := s.registry.Register(&tunnelpkg.Tunnel{Subdomain: "demo"}); err != nil {
		t.Fatal(err)
	}
	policy := s.certManager().HostPolicy

	tests := map[string]bool{
		"tunnel.test":             true,  // control channel and API
		"demo.tunnel.test":        true,  // active tunnel
		"random-name.tunnel.test": false, // no such tunnel
		"x.demo.tunnel.test":      false,
		"tunnel.test.evil.com":    false,
		"example.com":             false,
	}
	for host, want := range tests {
		err := policy(context.Background(), host)
		if got := err == nil; got != want {
			t.Errorf("HostPolicy(%q) allowed = %v, want %v (err: %v)", host, got, want, err)
		}
	}
}
