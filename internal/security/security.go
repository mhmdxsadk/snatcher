// Package security protects API resolution requests. Limits are process-local;
// file transfers use expiring signatures checked by the download handler.
package security

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"math"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mhmdxsadk/snatcher/internal/version"
)

type Config struct {
	APIKey          string
	IPPerMinute     int
	IPBurst         int
	GlobalPerMinute int
	GlobalBurst     int
	MaxConcurrent   int
	MaxClients      int
	TrustedProxies  []netip.Prefix
}

type bucket struct {
	tokens  float64
	updated time.Time
}

func (b *bucket) refill(now time.Time, rate, burst int) {
	b.tokens = math.Min(float64(burst), b.tokens+math.Max(0, now.Sub(b.updated).Seconds())*float64(rate)/60)
	b.updated = now
}

func (b *bucket) take(now time.Time, rate, burst int) int {
	b.refill(now, rate, burst)
	if b.tokens >= 1 {
		b.tokens--
		return 0
	}
	return max(1, int(math.Ceil((1-b.tokens)*60/float64(rate))))
}

type Guard struct {
	cfg         Config
	key         [32]byte
	mu          sync.Mutex
	clients     map[netip.Addr]*bucket
	global      bucket
	nextCleanup time.Time
	active      chan struct{}
	now         func() time.Time
}

// New expects validated, positive limits and a configured API key.
func New(cfg Config) *Guard {
	now := time.Now()
	return &Guard{
		cfg:         cfg,
		key:         sha256.Sum256([]byte(cfg.APIKey)),
		clients:     make(map[netip.Addr]*bucket),
		global:      bucket{tokens: float64(cfg.GlobalBurst), updated: now},
		nextCleanup: now.Add(time.Minute),
		active:      make(chan struct{}, cfg.MaxConcurrent),
		now:         time.Now,
	}
}

func (g *Guard) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+version.API+"/snatch" && !strings.HasPrefix(r.URL.Path, "/"+version.API+"/jobs/") {
			next.ServeHTTP(w, r)
			return
		}
		// Only the immediate trusted proxy may describe the original scheme.
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		peer, err := netip.ParseAddr(host)
		if err != nil || !g.trusted(peer.Unmap()) {
			r.Header.Del("X-Forwarded-Proto")
		} else if proto := r.Header.Get("X-Forwarded-Proto"); proto != "http" && proto != "https" {
			r.Header.Del("X-Forwarded-Proto")
		}
		ip, ok := g.clientIP(r)
		if !ok {
			reject(w, 400, "invalid_client", "Unable to determine client address.", 0)
			return
		}
		// Charge unauthenticated attempts too, before checking credentials.
		if retry := g.allowIP(ip); retry > 0 {
			reject(w, 429, "rate_limited", "Too many requests. Try again later.", retry)
			return
		}
		values := r.Header.Values("X-API-Key")
		valid := len(values) == 1
		var provided string
		if valid {
			provided = values[0]
		}
		digest := sha256.Sum256([]byte(provided))
		if subtle.ConstantTimeCompare(digest[:], g.key[:]) != 1 || !valid {
			reject(w, 401, "unauthorized", "A valid X-API-Key header is required.", 0)
			return
		}
		g.mu.Lock()
		retry := g.global.take(g.now(), g.cfg.GlobalPerMinute, g.cfg.GlobalBurst)
		g.mu.Unlock()
		if retry > 0 {
			reject(w, 429, "rate_limited", "The service request limit has been reached. Try again later.", retry)
			return
		}
		select {
		case g.active <- struct{}{}:
			defer func() { <-g.active }()
			next.ServeHTTP(w, r)
		default:
			reject(w, 429, "server_busy", "The service is busy. Try again later.", 1)
		}
	})
}

func (g *Guard) allowIP(ip netip.Addr) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	if !now.Before(g.nextCleanup) {
		// Only remove fully replenished buckets; cleanup cannot reset a quota early.
		for key, b := range g.clients {
			b.refill(now, g.cfg.IPPerMinute, g.cfg.IPBurst)
			if b.tokens >= float64(g.cfg.IPBurst) {
				delete(g.clients, key)
			}
		}
		g.nextCleanup = now.Add(time.Minute)
	}
	b := g.clients[ip]
	if b == nil {
		// Bound memory without evicting active quotas that an attacker could reset.
		if len(g.clients) >= g.cfg.MaxClients {
			return max(1, int(math.Ceil(g.nextCleanup.Sub(now).Seconds())))
		}
		b = &bucket{tokens: float64(g.cfg.IPBurst), updated: now}
		g.clients[ip] = b
	}
	return b.take(now, g.cfg.IPPerMinute, g.cfg.IPBurst)
}

func (g *Guard) trusted(ip netip.Addr) bool {
	for _, prefix := range g.cfg.TrustedProxies {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

func (g *Guard) clientIP(r *http.Request) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}, false
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	peer = peer.Unmap()
	if !g.trusted(peer) {
		return peer, true
	}
	// Walk from the nearest proxy toward the client. Stop at the first untrusted
	// hop so a client-supplied prefix of X-Forwarded-For cannot choose its quota.
	forwarded := strings.Join(r.Header.Values("X-Forwarded-For"), ",")
	if forwarded == "" {
		return peer, true
	}
	hops := strings.Split(forwarded, ",")
	if len(hops) > 32 {
		return netip.Addr{}, false
	}
	for i := len(hops) - 1; i >= 0 && g.trusted(peer); i-- {
		peer, err = netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			return netip.Addr{}, false
		}
		peer = peer.Unmap()
	}
	return peer, true
}

func reject(w http.ResponseWriter, status int, code, message string, retry int) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if retry > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(retry))
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "error", "error": map[string]string{"code": code, "message": message}})
}
