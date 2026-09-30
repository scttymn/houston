package deploy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// sites is the web, for the warm-up's client: each host's handler, and
// every URL asked for.
type sites struct {
	mu    sync.Mutex
	hosts map[string]http.Handler
	asked []string
}

func (s *sites) RoundTrip(r *http.Request) (*http.Response, error) {
	s.mu.Lock()
	s.asked = append(s.asked, r.URL.String())
	h, ok := s.hosts[r.URL.Host]
	s.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("no such host %s", r.URL.Host)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Result(), nil
}

func (s *sites) client() *http.Client { return &http.Client{Transport: s} }

func (s *sites) got() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Sorted(slices.Values(s.asked))
}

// page is a site whose home page is body, and whose files answer with
// cf-cache-status cache (every path in missing answers 404).
func page(body, cache string, missing ...string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			io.WriteString(w, body)
			return
		}
		if slices.Contains(missing, r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		if cache != "" {
			w.Header().Set("Cf-Cache-Status", cache)
		}
		io.WriteString(w, "file")
	})
}

const home = `<!DOCTYPE html><html><head>
<link rel="preload" href="/assets/font.woff2" as="font"><link rel="icon" href="/favicon.ico">
<style>body{background:url(/not-read.png)}</style>
<script type="module" src="/assets/site-1.js"></script>
<script src="https://other.example/tracker.js"></script>
</head><body>
<picture><source type="image/avif" srcset="/storage/k/160w.avif 160w, /storage/k/320w.avif 320w">
<img src="/storage/k/320w.webp" srcset="/storage/k/160w.webp 160w,/storage/k/320w.webp 320w" alt=""></picture>
<img src="data:image/gif;base64,R0lGOD"><a href="/about">About</a>
<video src="reel.mp4" poster="/poster.jpg#t"></video>
<script src="//other.example/x.js"></script><script src="/assets/site-1.js"></script>
</body></html>`

// The page's own files are fetched, each once, every width of a srcset:
// not another site's, a data: URL, a link, or what CSS asks for.
func TestWarmFetchesThePagesFiles(t *testing.T) {
	s := &sites{hosts: map[string]http.Handler{"shop.example": page(home, "HIT")}}
	var log strings.Builder
	warmUp(context.Background(), s.client(), []string{"shop.example"}, func(f string, a ...any) { fmt.Fprintf(&log, f, a...) })
	want := []string{"https://shop.example/", "https://shop.example/assets/font.woff2", "https://shop.example/assets/site-1.js",
		"https://shop.example/favicon.ico", "https://shop.example/poster.jpg", "https://shop.example/reel.mp4",
		"https://shop.example/storage/k/160w.avif", "https://shop.example/storage/k/160w.webp",
		"https://shop.example/storage/k/320w.avif", "https://shop.example/storage/k/320w.webp"}
	if got := s.got(); !slices.Equal(got, want) {
		t.Errorf("fetched\n %v\nwant\n %v", got, want)
	}
	if got := log.String(); got != "ok  warmed https://shop.example/: 9 files\n" {
		t.Errorf("log %q", got)
	}
}

// A host that redirects (www to the apex) is skipped: its target is warmed
// as a host of its own. So is one that isn't a page, or that fails.
func TestWarmSkipsRedirects(t *testing.T) {
	s := &sites{hosts: map[string]http.Handler{
		"shop.example": page(`<script src="/a.js"></script>`, ""),
		"www.shop.example": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://shop.example/", http.StatusMovedPermanently)
		}),
		"api.shop.example": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{}`)
		}),
	}}
	var log strings.Builder
	warmUp(context.Background(), s.client(), []string{"www.shop.example", "api.shop.example", "down.example", "shop.example"},
		func(f string, a ...any) { fmt.Fprintf(&log, f, a...) })
	if got, want := s.got(), []string{"https://api.shop.example/", "https://down.example/", "https://shop.example/", "https://shop.example/a.js", "https://www.shop.example/"}; !slices.Equal(got, want) {
		t.Errorf("fetched %v", got)
	}
	for _, want := range []string{"warm-up: https://www.shop.example/ skipped: it answers 301", "warm-up: https://api.shop.example/ skipped: it isn't a page (application/json)",
		"warm-up: https://down.example/ skipped:", "ok  warmed https://shop.example/: 1 file\n"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("no %q in\n%s", want, log.String())
		}
	}
}

// Files Cloudflare had to fetch from here are counted; ones that fail are
// named.
func TestWarmReports(t *testing.T) {
	s := &sites{hosts: map[string]http.Handler{"shop.example": page(home, "MISS", "/favicon.ico")}}
	var log strings.Builder
	warmUp(context.Background(), s.client(), []string{"shop.example"}, func(f string, a ...any) { fmt.Fprintf(&log, f, a...) })
	for _, want := range []string{"ok  warmed https://shop.example/: 9 files, 8 newly cached\n", "warm-up: https://shop.example/favicon.ico: 404\n"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("no %q in\n%s", want, log.String())
		}
	}
}

// It stops at warmMaxFiles in all, and at its deadline.
func TestWarmLimits(t *testing.T) {
	var many strings.Builder
	for i := range warmMaxFiles + 50 {
		fmt.Fprintf(&many, `<img src="/i/%d.webp">`, i)
	}
	s := &sites{hosts: map[string]http.Handler{"a.example": page(many.String(), ""), "b.example": page(`<img src="/b.webp">`, "")}}
	var log strings.Builder
	warmUp(context.Background(), s.client(), []string{"a.example", "b.example"}, func(f string, a ...any) { fmt.Fprintf(&log, f, a...) })
	if n := len(s.got()); n != warmMaxFiles+2 {
		t.Errorf("fetched %d, want the two pages and %d files", n, warmMaxFiles)
	}
	if !strings.Contains(log.String(), fmt.Sprintf("asks for %d files; fetching the first %d", warmMaxFiles+50, warmMaxFiles)) {
		t.Errorf("log:\n%s", log.String())
	}

	// A slow site: the warm-up gives up at its deadline.
	slow := &sites{hosts: map[string]http.Handler{"slow.example": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, `<img src="/1.webp"><img src="/2.webp"><img src="/3.webp"><img src="/4.webp"><img src="/5.webp">`)
			return
		}
		<-r.Context().Done()
	})}}
	defer func(was time.Duration) { warmTimeout = was }(warmTimeout)
	warmTimeout = 200 * time.Millisecond
	done := make(chan struct{})
	go func() {
		warmUp(context.Background(), slow.client(), []string{"slow.example"}, func(string, ...any) {})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the warm-up didn't stop at its deadline")
	}
}
