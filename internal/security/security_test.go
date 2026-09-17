package security

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

func fixture() *Guard {
	return New(Config{APIKey: strings.Repeat("k", 32), IPPerMinute: 20, IPBurst: 5, GlobalPerMinute: 60, GlobalBurst: 10, MaxConcurrent: 2, MaxClients: 100})
}
func request(h http.Handler, ip, auth string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/v1/snatch", nil)
	r.RemoteAddr = ip + ":1234"
	if auth != "" {
		r.Header.Set("X-API-Key", auth)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
}
func token() string { return strings.Repeat("k", 32) }
func TestAuthentication(t *testing.T) {
	for _, auth := range []string{"", "wrong", "Bearer " + token(), token() + " extra", token() + "," + token()} {
		t.Run(auth[:min(len(auth), 12)], func(t *testing.T) {
			w := request(fixture().Wrap(okHandler()), "192.0.2.1", auth)
			if w.Code != 401 || w.Header().Get("WWW-Authenticate") != "" || !strings.Contains(w.Body.String(), `"unauthorized"`) {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
		})
	}
	g := fixture()
	h := g.Wrap(okHandler())
	if w := request(h, "192.0.2.1", token()); w.Code != 200 {
		t.Fatal(w.Code)
	}
	r := httptest.NewRequest("POST", "/v1/snatch", nil)
	r.RemoteAddr = "192.0.2.1:1"
	r.Header.Add("X-API-Key", token())
	r.Header.Add("X-API-Key", token())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	r = httptest.NewRequest("POST", "/v1/snatch", nil)
	r.RemoteAddr = "192.0.2.1:1"
	r.Header.Set("Authorization", "Bearer "+token())
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("legacy Authorization header must not authenticate")
	}
	r = httptest.NewRequest("GET", "/health", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal("health must bypass auth")
	}
}
func TestIPLimitAndRefill(t *testing.T) {
	g := fixture()
	now := time.Now()
	g.now = func() time.Time { return now }
	h := g.Wrap(okHandler())
	for i := 0; i < 5; i++ {
		if w := request(h, "192.0.2.1", token()); w.Code != 200 {
			t.Fatal(w.Code)
		}
	}
	w := request(h, "192.0.2.1", token())
	if w.Code != 429 || w.Header().Get("Retry-After") != "3" {
		t.Fatalf("%d %v", w.Code, w.Header())
	}
	if w := request(h, "192.0.2.2", token()); w.Code != 200 {
		t.Fatal("IP quotas must be independent")
	}
	now = now.Add(3 * time.Second)
	if w := request(h, "192.0.2.1", token()); w.Code != 200 {
		t.Fatal("quota did not refill")
	}
}
func TestFailedAuthConsumesIPQuotaOnly(t *testing.T) {
	g := fixture()
	g.cfg.GlobalBurst = 1
	g.global.tokens = 1
	h := g.Wrap(okHandler())
	for i := 0; i < 5; i++ {
		if w := request(h, "192.0.2.1", "bad"); w.Code != 401 {
			t.Fatal(w.Code)
		}
	}
	if w := request(h, "192.0.2.1", "bad"); w.Code != 429 {
		t.Fatal(w.Code)
	}
	if w := request(h, "192.0.2.2", token()); w.Code != 200 {
		t.Fatal("failed auth spent global quota")
	}
	if w := request(h, "192.0.2.3", token()); w.Code != 429 {
		t.Fatal("global quota not enforced")
	}
}
func TestConcurrentLimitAndRelease(t *testing.T) {
	g := fixture()
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	h := g.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { started <- struct{}{}; <-release; w.WriteHeader(200) }))
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if w := request(h, "192.0.2.1", token()); w.Code != 200 {
				t.Errorf("%d", w.Code)
			}
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("handler not reached")
		}
	}
	w := request(h, "192.0.2.2", token())
	if w.Code != 429 || !strings.Contains(w.Body.String(), "server_busy") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	close(release)
	wg.Wait()
	if w := request(h, "192.0.2.2", token()); w.Code != 200 {
		t.Fatal("concurrency slot leaked")
	}
}
func TestClientIP(t *testing.T) {
	g := fixture()
	g.cfg.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
	for _, tt := range []struct{ peer, xff, want string }{
		{"192.0.2.1:4", "198.51.100.1", "192.0.2.1"},
		{"127.0.0.1:4", "198.51.100.1, 192.0.2.1", "192.0.2.1"},
		{"127.0.0.1:4", "192.0.2.1, 127.0.0.1", "192.0.2.1"},
		{"[::ffff:192.0.2.1]:4", "", "192.0.2.1"},
		{"127.0.0.1:4", "malformed", ""},
	} {
		r := httptest.NewRequest("POST", "/v1/snatch", nil)
		r.RemoteAddr = tt.peer
		r.Header.Set("X-Forwarded-For", tt.xff)
		ip, ok := g.clientIP(r)
		if tt.want == "" {
			if ok {
				t.Fatal("malformed forwarded IP accepted")
			}
			continue
		}
		if !ok || ip.String() != tt.want {
			t.Errorf("%s / %s => %s, %v", tt.peer, tt.xff, ip, ok)
		}
	}
}
func TestBoundedClientMemory(t *testing.T) {
	g := fixture()
	g.cfg.MaxClients = 1
	g.cfg.IPBurst = 2
	g.cfg.IPPerMinute = 1
	now := time.Now()
	g.now = func() time.Time { return now }
	a := netip.MustParseAddr("192.0.2.1")
	b := netip.MustParseAddr("192.0.2.2")
	if g.allowIP(a) != 0 || g.allowIP(a) != 0 {
		t.Fatal("initial quota")
	}
	if g.allowIP(b) == 0 || len(g.clients) != 1 {
		t.Fatal("client cap not enforced")
	}
	now = now.Add(time.Minute)
	if g.allowIP(b) == 0 {
		t.Fatal("partially replenished bucket evicted")
	}
	now = now.Add(time.Minute)
	if g.allowIP(b) != 0 || len(g.clients) != 1 {
		t.Fatal("full bucket not reclaimed")
	}
}
