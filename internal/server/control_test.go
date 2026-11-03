package server

import (
	"context"
	"net"
	"testing"

	"github.com/hashicorp/yamux"
	"github.com/rs/zerolog"

	"github.com/nopedal/gotunnel/internal/proto"
	tunnelpkg "github.com/nopedal/gotunnel/internal/tunnel"
)

// requestTunnel runs the client side of the control handshake against s and
// returns the server's reply. The session stays open until the test ends,
// so the tunnel remains registered.
func requestTunnel(t *testing.T, s *Server, subdomain string) proto.TunnelResponse {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		s.handleControl(context.Background(), conn)
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	session, err := yamux.Client(conn, tunnelpkg.YamuxConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	ctrl, err := session.Open()
	if err != nil {
		t.Fatal(err)
	}

	if err := proto.WriteMessage(ctrl, proto.TypeAuthRequest, &proto.AuthRequest{Token: s.cfg.Token}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := proto.ReadMessage(ctrl); err != nil {
		t.Fatalf("read auth response: %v", err)
	}
	if err := proto.WriteMessage(ctrl, proto.TypeTunnelRequest, &proto.TunnelRequest{
		Subdomain: subdomain,
		Mode:      "http",
	}); err != nil {
		t.Fatal(err)
	}
	msgType, data, err := proto.ReadMessage(ctrl)
	if err != nil {
		t.Fatalf("read tunnel response: %v", err)
	}
	if msgType != proto.TypeTunnelResponse {
		t.Fatalf("message type = %d, want tunnel response", msgType)
	}
	var resp proto.TunnelResponse
	if err := proto.Decode(data, &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestRequestedSubdomainIsNormalized(t *testing.T) {
	s := New(&Config{Domain: "tunnel.test", Token: "secret", MaxClients: 10}, zerolog.Nop())

	// Browsers always send the host in lowercase, so a tunnel registered as
	// "MyApp" could never be reached.
	resp := requestTunnel(t, s, "MyApp")
	if !resp.OK || resp.Subdomain != "myapp" || resp.URL != "http://myapp.tunnel.test" {
		t.Fatalf("response = %+v, want subdomain myapp at http://myapp.tunnel.test", resp)
	}
	if _, ok := s.registry.Lookup("myapp"); !ok {
		t.Fatal("tunnel not registered under lowercase subdomain")
	}
}

func TestInvalidRequestedSubdomainIsRejected(t *testing.T) {
	s := New(&Config{Domain: "tunnel.test", Token: "secret", MaxClients: 10}, zerolog.Nop())

	resp := requestTunnel(t, s, "a.b")
	if resp.OK {
		t.Fatalf("subdomain %q accepted: %+v", "a.b", resp)
	}
	if s.registry.Count() != 0 {
		t.Fatalf("registry has %d tunnels, want 0", s.registry.Count())
	}
}
