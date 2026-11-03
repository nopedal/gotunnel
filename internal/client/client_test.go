package client

import (
	"bytes"
	"context"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/yamux"

	"github.com/nopedal/gotunnel/internal/proto"
	tunnelpkg "github.com/nopedal/gotunnel/internal/tunnel"
)

// If the TLS handshake fails (a plaintext server, or an attacker interfering
// with the handshake), the client must give up rather than retry over plain
// TCP and hand its token to whoever is listening.
func TestConnectDoesNotFallBackToPlaintext(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	var (
		mu       sync.Mutex
		received []byte
		accepted int
	)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			accepted++
			first := accepted == 1
			mu.Unlock()

			go func() {
				defer conn.Close()
				if first {
					// Break the TLS handshake.
					return
				}
				conn.SetReadDeadline(time.Now().Add(time.Second))
				data, _ := io.ReadAll(conn)
				mu.Lock()
				received = append(received, data...)
				mu.Unlock()
			}()
		}
	}()

	const token = "super-secret-token"
	c := New(&Config{Server: ln.Addr().String(), Token: token, Port: 3000, Mode: "http"})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.connectOnce(ctx); err == nil {
		t.Fatal("connectOnce succeeded against a server that broke the TLS handshake")
	}

	mu.Lock()
	defer mu.Unlock()
	if accepted != 1 {
		t.Errorf("client opened %d connections, want 1 (no plaintext retry)", accepted)
	}
	if bytes.Contains(received, []byte(token)) {
		t.Error("token was sent in plaintext")
	}
}

// Ctrl-C cancels the context; Connect must treat that as a clean exit.
func TestConnectReturnsNilWhenCancelled(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing listening: every attempt fails and Connect backs off

	c := New(&Config{Server: addr, Token: "t", Port: 3000, Insecure: true})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Connect(ctx) }()

	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Connect returned %v after cancellation, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Connect did not return after cancellation")
	}
}

// A reply of the wrong type (e.g. a mismatched server version) must not be
// decoded as a tunnel response: an {"ok":true} payload would otherwise look
// like a successful tunnel with an empty URL.
func TestConnectRejectsUnexpectedTunnelResponseType(t *testing.T) {
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
		session, err := yamux.Server(conn, tunnelpkg.YamuxConfig())
		if err != nil {
			return
		}
		defer session.Close()
		ctrl, err := session.Accept()
		if err != nil {
			return
		}
		proto.ReadMessage(ctrl) // auth request
		proto.WriteMessage(ctrl, proto.TypeAuthResponse, &proto.AuthResponse{OK: true})
		proto.ReadMessage(ctrl) // tunnel request
		proto.WriteMessage(ctrl, proto.TypeAuthResponse, &proto.AuthResponse{OK: true})
		<-session.CloseChan()
	}()

	c := New(&Config{Server: ln.Addr().String(), Token: "t", Port: 3000, Insecure: true})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err = c.connectOnce(ctx)
	if err == nil || !strings.Contains(err.Error(), "unexpected message type") {
		t.Fatalf("connectOnce error = %v, want unexpected message type", err)
	}
}
