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

func TestControlListenerUsesTLS(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	probe.Close()

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
