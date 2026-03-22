// Package tunnel provides the core multiplexing, forwarding, and slug
// generation logic shared by both the server and the client.
package tunnel

import (
	"crypto/rand"
	"fmt"
	"io"
	"math/big"
	"net"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
)

// Tunnel represents a single client tunnel with its yamux session.
type Tunnel struct {
	ID        string
	Subdomain string
	Mode      string // "http" or "tcp"
	Session   *yamux.Session
	CreatedAt time.Time
	RemoteIP  string
}

// Registry tracks all active tunnels.
type Registry struct {
	mu      sync.RWMutex
	tunnels map[string]*Tunnel // keyed by subdomain
}

// NewRegistry creates an empty tunnel registry.
func NewRegistry() *Registry {
	return &Registry{
		tunnels: make(map[string]*Tunnel),
	}
}

// Register adds a tunnel. Returns an error if the subdomain is taken.
func (r *Registry) Register(t *Tunnel) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tunnels[t.Subdomain]; exists {
		return fmt.Errorf("subdomain %q already in use", t.Subdomain)
	}
	r.tunnels[t.Subdomain] = t
	return nil
}

// Deregister removes a tunnel by subdomain.
func (r *Registry) Deregister(subdomain string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tunnels, subdomain)
}

// Lookup finds a tunnel by subdomain.
func (r *Registry) Lookup(subdomain string) (*Tunnel, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tunnels[subdomain]
	return t, ok
}

// Count returns the number of active tunnels.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tunnels)
}

// List returns a snapshot of all active tunnels.
func (r *Registry) List() []*Tunnel {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Tunnel, 0, len(r.tunnels))
	for _, t := range r.tunnels {
		out = append(out, t)
	}
	return out
}

// Relay bidirectionally copies data between two connections.
// It closes both when either direction finishes.
func Relay(a, b net.Conn) (int64, int64) {
	var wg sync.WaitGroup
	var sent, received int64

	wg.Add(2)
	go func() {
		defer wg.Done()
		received, _ = io.Copy(a, b)
		a.Close()
	}()
	go func() {
		defer wg.Done()
		sent, _ = io.Copy(b, a)
		b.Close()
	}()
	wg.Wait()

	return sent, received
}

// Words for subdomain generation — curated for readability.
var adjectives = []string{
	"swift", "bold", "calm", "dark", "easy", "fair", "glad", "keen",
	"live", "neat", "pure", "rare", "safe", "true", "vast", "warm",
	"blue", "cool", "deep", "fast", "gold", "high", "iron", "just",
	"kind", "lean", "mild", "open", "rich", "slim", "tall", "wise",
}

var nouns = []string{
	"arch", "beam", "cast", "dock", "edge", "flux", "gate", "hive",
	"iris", "jade", "kite", "lens", "mesh", "node", "opus", "peak",
	"quad", "reef", "sail", "tide", "unit", "volt", "wave", "zone",
	"axle", "bolt", "core", "dart", "fern", "glow", "helm", "link",
}

// GenerateSlug produces a random subdomain like "swift-arch-4f2a".
func GenerateSlug() string {
	adj := pick(adjectives)
	noun := pick(nouns)
	hex := randomHex(4)
	return fmt.Sprintf("%s-%s-%s", adj, noun, hex)
}

func pick(list []string) string {
	n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(list))))
	return list[n.Int64()]
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return fmt.Sprintf("%x", b)[:n]
}

// YamuxConfig returns a yamux configuration tuned for tunnel use.
func YamuxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.AcceptBacklog = 256
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 15 * time.Second
	cfg.ConnectionWriteTimeout = 10 * time.Second
	cfg.StreamCloseTimeout = 60 * time.Second
	cfg.StreamOpenTimeout = 30 * time.Second
	cfg.MaxStreamWindowSize = 1024 * 1024 // 1 MB
	cfg.LogOutput = io.Discard
	return cfg
}
