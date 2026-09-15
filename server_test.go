package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDashboardThroughProxy(t *testing.T) {
	for _, base := range []string{"/", "/pipeek/", "/tools/pipeek/"} {
		t.Run(base, func(t *testing.T) {
			a, err := newApp(base, "<script>alert(1)</script>", 3*time.Second, 10*time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			a.update(sample{At: time.Now(), Warnings: []string{"<b>unavailable</b>"}})
			upstream := httptest.NewServer(a.handler())
			defer upstream.Close()
			u, _ := url.Parse(upstream.URL)
			proxy := httptest.NewServer(httputil.NewSingleHostReverseProxy(u))
			defer proxy.Close()
			for _, route := range []struct {
				path, contains string
				code           int
			}{
				{base, "System overview", 200}, {base + "metrics", "&lt;b&gt;unavailable&lt;/b&gt;", 200}, {base + "assets/style.css", "--bg", 200}, {base + "assets/app.js", "document.hidden", 200}, {base + "assets/vendor/htmx.min.js", "2.0.10", 200}, {base + "assets/favicon.svg", "<svg", 200}, {base + "healthz", "degraded", 503}, {base + "assets/index.html", "404", 404}, {base + "assets/", "404", 404}, {base + "unknown", "404", 404},
			} {
				res, err := http.Get(proxy.URL + route.path)
				if err != nil {
					t.Fatal(err)
				}
				b, _ := io.ReadAll(res.Body)
				res.Body.Close()
				if res.StatusCode != route.code || !strings.Contains(string(b), route.contains) {
					t.Fatalf("%s: status %d, body %.200s", route.path, res.StatusCode, b)
				}
				if !strings.Contains(route.path, "assets/") && res.Header.Get("Cache-Control") != "no-store" {
					t.Fatal("dynamic response can be cached")
				}
				if route.path == base {
					body := string(b)
					if strings.Contains(body, "<script>alert(1)") || !strings.Contains(body, `hx-get="`+base+`metrics"`) || !strings.Contains(body, `href="`+base+`assets/style.css?v=mocha-1"`) {
						t.Fatal("unsafe hostname or wrong prefix")
					}
				}
			}
			if base != "/" {
				client := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
				res, err := client.Get(proxy.URL + strings.TrimSuffix(base, "/"))
				if err != nil {
					t.Fatal(err)
				}
				res.Body.Close()
				if res.StatusCode != 308 || res.Header.Get("Location") != base {
					t.Fatal("missing canonical redirect")
				}
			}
			req := httptest.NewRequest("POST", base+"metrics", nil)
			w := httptest.NewRecorder()
			a.handler().ServeHTTP(w, req)
			if w.Code != 405 {
				t.Fatal("accepted write method")
			}
			a.update(sample{At: time.Now()})
			w = httptest.NewRecorder()
			a.handler().ServeHTTP(w, httptest.NewRequest("GET", base+"healthz", nil))
			if w.Code != 200 {
				t.Fatal("healthy sample not healthy")
			}
			a.update(sample{At: time.Now().Add(-time.Minute)})
			w = httptest.NewRecorder()
			a.handler().ServeHTTP(w, httptest.NewRequest("GET", base+"healthz", nil))
			if w.Code != 503 {
				t.Fatal("stale sample not detected")
			}
		})
	}
}
func TestConcurrentReaders(t *testing.T) {
	a, _ := newApp("/", "test", time.Second, time.Minute)
	a.update(sample{At: time.Now()})
	h := a.handler()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/metrics", nil))
			}
		}()
	}
	for i := 0; i < 20; i++ {
		a.update(sample{At: time.Now()})
	}
	wg.Wait()
}
func TestBaseValidation(t *testing.T) {
	for _, s := range []string{"", "relative", "//bad", "/../x", "/x/./y", "/x?bad", "/x\"bad", "/x//"} {
		if _, e := normalizeBase(s); e == nil {
			t.Errorf("accepted %q", s)
		}
	}
	for _, s := range []string{"/", "/pipeek", "/tools/pi_peek-1/"} {
		if _, e := normalizeBase(s); e != nil {
			t.Errorf("rejected %q", s)
		}
	}
}
func BenchmarkSampleAndRender(b *testing.B) {
	a, _ := newApp("/", "test", 3*time.Second, 10*time.Minute)
	c := collector{proc: "/proc", sys: "/sys", disks: []string{"/"}}
	for i := 0; i < len(a.points); i++ {
		a.update(c.collect(time.Now()))
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		a.update(c.collect(time.Now()))
	}
}
func BenchmarkServeMetrics(b *testing.B) {
	a, _ := newApp("/", "test", 3*time.Second, 10*time.Minute)
	a.update(sample{At: time.Now()})
	h := a.handler()
	r := httptest.NewRequest("GET", "/metrics", nil)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		h.ServeHTTP(httptest.NewRecorder(), r)
	}
}

func TestSampleAgeForFreshAndFrozenResponses(t *testing.T) {
	a, _ := newApp("/", "host", time.Second, time.Minute)
	a.update(sample{At: time.Now().Add(-time.Minute)})
	w := httptest.NewRecorder()
	a.handler().ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	age, err := strconv.ParseInt(w.Header().Get("X-PiPeek-Sample-Age"), 10, 64)
	if err != nil || age < 60000 {
		t.Fatalf("incorrect age: %d %v", age, err)
	}
	w = httptest.NewRecorder()
	a.handler().ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(w.Body.String(), `data-sample-age="60`) {
		t.Fatal("initial page missing server-computed age")
	}
	a.update(sample{At: time.Now()})
	w = httptest.NewRecorder()
	a.handler().ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	age, err = strconv.ParseInt(w.Header().Get("X-PiPeek-Sample-Age"), 10, 64)
	if err != nil || age >= 1000 {
		t.Fatalf("fresh sample age: %d %v", age, err)
	}
}
