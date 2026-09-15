package main

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed web
var assets embed.FS

type point struct {
	At                                 time.Time
	CPU, Memory, Temperature, RX, TX   float64
	CPUOK, MemoryOK, TempOK, NetworkOK bool
}
type view struct {
	TemperatureStatus                                  temperatureStatus
	Sample                                             sample
	CPUChart, MemoryChart, TempChart, RXChart, TXChart string
	NetworkScale                                       string
	Count                                              int
	History                                            string
}
type app struct {
	base, host        string
	interval, history time.Duration
	templates         *template.Template
	mu                sync.RWMutex
	fragment          []byte
	sampled           time.Time
	degraded          bool
	// History is owned exclusively by the sampling goroutine.
	points            []point
	next, count       int
	temperatureStatus temperatureStatus
}

func newApp(base, host string, interval, history time.Duration) (*app, error) {
	funcs := template.FuncMap{"bytes": func(n uint64) string { return formatBytes(float64(n)) }, "rate": formatBytes, "pct": percent, "f1": func(n float64) string { return fmt.Sprintf("%.1f", n) }, "unix": func(t time.Time) int64 { return t.UnixMilli() }}
	t, err := template.New("").Funcs(funcs).ParseFS(assets, "web/*.html")
	if err != nil {
		return nil, err
	}
	return &app{base: base, host: host, interval: interval, history: history, templates: t, points: make([]point, int(history/interval)+1)}, nil
}
func (a *app) update(s sample) {
	a.points[a.next] = point{At: s.At, CPU: s.CPU, Memory: percent(s.Memory.Used, s.Memory.Total), Temperature: s.Temperature, RX: s.RX, TX: s.TX, CPUOK: s.CPUOK, MemoryOK: s.MemoryOK, TempOK: s.TempOK, NetworkOK: s.NetworkOK}
	a.next = (a.next + 1) % len(a.points)
	if a.count < len(a.points) {
		a.count++
	}
	points := make([]point, 0, a.count)
	for i := 0; i < a.count; i++ {
		p := a.points[(a.next-a.count+i+len(a.points))%len(a.points)]
		if !p.At.Before(s.At.Add(-a.history)) {
			points = append(points, p)
		}
	}
	scale := 1024.0
	for _, p := range points {
		scale = math.Max(scale, math.Max(p.RX, p.TX))
	}
	a.temperatureStatus = nextTemperatureStatus(a.temperatureStatus, s.Temperature, s.TempOK)
	v := view{TemperatureStatus: a.temperatureStatus, Sample: s, Count: len(points), History: a.history.String(), NetworkScale: formatBytes(scale) + "/s"}
	v.CPUChart = chart(points, s.At, a.history, a.interval, 100, func(p point) (float64, bool) { return p.CPU, p.CPUOK })
	v.MemoryChart = chart(points, s.At, a.history, a.interval, 100, func(p point) (float64, bool) { return p.Memory, p.MemoryOK })
	v.TempChart = chart(points, s.At, a.history, a.interval, 100, func(p point) (float64, bool) { return p.Temperature, p.TempOK })
	v.RXChart = chart(points, s.At, a.history, a.interval, scale, func(p point) (float64, bool) { return p.RX, p.NetworkOK })
	v.TXChart = chart(points, s.At, a.history, a.interval, scale, func(p point) (float64, bool) { return p.TX, p.NetworkOK })
	var buf bytes.Buffer
	if err := a.templates.ExecuteTemplate(&buf, "metrics", v); err != nil {
		log.Printf("render metrics: %v", err)
		return
	}
	a.mu.Lock()
	a.fragment = buf.Bytes()
	a.sampled = s.At
	a.degraded = len(s.Warnings) > 0
	a.mu.Unlock()
}
func chart(points []point, now time.Time, window, interval time.Duration, scale float64, value func(point) (float64, bool)) string {
	var b strings.Builder
	pen := false
	var prev time.Time
	for _, p := range points {
		v, ok := value(p)
		if !ok {
			pen = false
			continue
		}
		x := 300 * (1 - now.Sub(p.At).Seconds()/window.Seconds())
		y := 56 - 52*math.Max(0, math.Min(1, v/scale))
		cmd := "L"
		if !pen || (!prev.IsZero() && p.At.Sub(prev) > 2*interval) {
			cmd = "M"
		}
		fmt.Fprintf(&b, "%s%.1f,%.1f ", cmd, x, y)
		pen = true
		prev = p.At
	}
	return b.String()
}
func formatBytes(n float64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}
	i := 0
	for n >= 1024 && i < len(units)-1 {
		n /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%.0f %s", n, units[i])
	}
	return fmt.Sprintf("%.1f %s", n, units[i])
}
func (a *app) handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(assets, "web")
	// Expose only the named public assets, never templates or directory listings.
	files := http.FileServer(http.FS(static))
	for _, name := range []string{"style.css", "app.js", "vendor/htmx.min.js", "favicon.svg"} {
		mux.Handle("GET "+a.base+"assets/"+name, http.StripPrefix(a.base+"assets/", files))
	}
	if a.base != "/" {
		mux.HandleFunc("GET "+strings.TrimSuffix(a.base, "/"), func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, a.base, http.StatusPermanentRedirect)
		})
	}
	mux.HandleFunc("GET "+a.base+"{$}", func(w http.ResponseWriter, r *http.Request) {
		a.mu.RLock()
		fragment := a.fragment
		age := max(int64(0), time.Since(a.sampled).Milliseconds())
		a.mu.RUnlock()
		// Only our escaped, rendered template is marked trusted here.
		data := struct {
			Base, Host string
			Interval   int64
			SampleAge  int64
			Fragment   template.HTML
		}{a.base, a.host, a.interval.Milliseconds(), age, template.HTML(fragment)}
		var buf bytes.Buffer
		if err := a.templates.ExecuteTemplate(&buf, "index.html", data); err != nil {
			http.Error(w, "Unable to render dashboard", 500)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(buf.Bytes())
	})
	mux.HandleFunc("GET "+a.base+"metrics", func(w http.ResponseWriter, r *http.Request) {
		a.mu.RLock()
		b := a.fragment
		age := max(int64(0), time.Since(a.sampled).Milliseconds())
		a.mu.RUnlock()
		w.Header().Set("X-PiPeek-Sample-Age", strconv.FormatInt(age, 10))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(b)
	})
	mux.HandleFunc("GET "+a.base+"healthz", func(w http.ResponseWriter, r *http.Request) {
		a.mu.RLock()
		at, degraded := a.sampled, a.degraded
		a.mu.RUnlock()
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if at.IsZero() || time.Since(at) > 3*a.interval {
			w.WriteHeader(503)
			_, _ = w.Write([]byte("stale\n"))
			return
		}
		if degraded {
			w.WriteHeader(503)
			_, _ = w.Write([]byte("degraded\n"))
			return
		}
		_, _ = w.Write([]byte("ok\n"))
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
		w.Header().Set("Cache-Control", "no-store")
		if strings.HasPrefix(r.URL.Path, a.base+"assets/") {
			w.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(86400))
		}
		mux.ServeHTTP(w, r)
	})
}
