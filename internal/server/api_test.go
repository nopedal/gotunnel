package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	tunnelpkg "github.com/nopedal/gotunnel/internal/tunnel"
)

func TestTunnelsAPIRequiresToken(t *testing.T) {
	s := New(&Config{Domain: "tunnel.test", Token: "secret"}, zerolog.Nop())
	if err := s.registry.Register(&tunnelpkg.Tunnel{
		Subdomain: "demo",
		Mode:      "http",
		RemoteIP:  "203.0.113.7",
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	h := s.httpHandler()

	tests := []struct {
		name   string
		path   string
		auth   string
		status int
	}{
		{"no token", "/api/tunnels", "", http.StatusUnauthorized},
		{"wrong token", "/api/tunnels", "Bearer nope", http.StatusUnauthorized},
		{"token without scheme", "/api/tunnels", "secret", http.StatusUnauthorized},
		{"valid token", "/api/tunnels", "Bearer secret", http.StatusOK},
		{"health stays public", "/health", "", http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://tunnel.test"+tt.path, nil)
			if tt.auth != "" {
				req.Header.Set("Authorization", tt.auth)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d", rec.Code, tt.status)
			}
			leaked := strings.Contains(rec.Body.String(), "203.0.113.7")
			if tt.status == http.StatusUnauthorized && leaked {
				t.Fatal("unauthorized response leaks client IP")
			}
			if tt.name == "valid token" && !leaked {
				t.Fatalf("authorized response missing tunnel: %s", rec.Body.String())
			}
		})
	}
}
