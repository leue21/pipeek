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
	UnderVoltageSeen                                   time.Time
	Hosts                                              []hostCard
	Notice                                             string // a peer page's offline notice
	NoData                                             bool   // a peer that never answered
}
type app struct {
	base, host        string
	interval, history time.Duration
	templates         *template.Template
	mu                sync.RWMutex
	fragment          []byte
	json              []byte
	fleetJSON         []byte
	fleet             *fleet // nil unless -peers is set
	sampled           time.Time
	degraded          bool
	// History is owned exclusively by the sampling goroutine.
	hist              *history
	temperatureStatus temperatureStatus
	underVoltageSeen  time.Time
	self              hostCard // this machine's latest card, under mu
}

func newApp(base, host string, interval, history time.Duration) (*app, error) {
	funcs := template.FuncMap{"bytes": func(n uint64) string { return formatBytes(float64(n)) }, "rate": formatBytes, "pct": percent, "f1": func(n float64) string { return fmt.Sprintf("%.1f", n) }, "unix": func(t time.Time) int64 { return t.UnixMilli() }, "f2": func(n float64) string { return fmt.Sprintf("%.2f", n) }, "freq": formatFrequency, "deref": func(p *float64) float64 { return *p }}
	t, err := template.New("").Funcs(funcs).ParseFS(assets, "web/*.html")
	if err != nil {
		return nil, err
	}
	return &app{base: base, host: host, interval: interval, history: history, templates: t, hist: newHistory(history, interval)}, nil
}
func (a *app) update(s sample) {
	a.hist.add(s)
	a.temperatureStatus = nextTemperatureStatus(a.temperatureStatus, s.Temperature, s.TempOK)
	if s.VoltageOK && s.UnderVoltage {
		a.underVoltageSeen = s.At
	}
	v := view{TemperatureStatus: a.temperatureStatus, UnderVoltageSeen: a.underVoltageSeen, Hosts: a.hostCards(s), Sample: s}
	a.hist.fill(&v, s.At, a.interval)
	var buf bytes.Buffer
	if err := a.templates.ExecuteTemplate(&buf, "metrics", v); err != nil {
		log.Printf("render metrics: %v", err)
		return
	}
	doc, err := a.apiJSON(s, a.temperatureStatus, a.underVoltageSeen)
	if err != nil {
		log.Printf("render api: %v", err)
		return
	}
	var fleetDoc []byte
	if a.fleet != nil {
		states := a.fleet.snapshot()
		fleetDoc, err = fleetJSON(doc, a.host, a.base, states, s.At, a.interval)
		if err != nil {
			log.Printf("render fleet: %v", err)
			return
		}
	}
	a.mu.Lock()
	a.json = doc
	a.fleetJSON = fleetDoc
	a.fragment = buf.Bytes()
	a.sampled = s.At
	a.degraded = len(s.Warnings) > 0
	a.mu.Unlock()
}

// hostCards is the overview row on this machine's own page.
func (a *app) hostCards(s sample) []hostCard {
	if a.fleet == nil {
		return nil
	}
	self := selfCard(s, a.host, a.base, a.temperatureStatus)
	a.mu.Lock()
	a.self = self
	a.mu.Unlock()
	return overviewCards(self, a.fleet.snapshot(), "", a.base, s.At, a.interval)
}

// overviewCards lists this machine first, then the peers, and marks the one
// the page shows: selected is a peer name, or "" for this machine.
func overviewCards(self hostCard, states []peerState, selected, base string, now time.Time, interval time.Duration) []hostCard {
	self.Selected = selected == ""
	cards := []hostCard{self}
	for _, st := range states {
		c := peerCard(st, base, now, interval)
		c.Selected = st.Peer.Name == selected
		cards = append(cards, c)
	}
	return cards
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
func formatFrequency(mhz float64) string {
	if mhz >= 1000 {
		return fmt.Sprintf("%.2f GHz", mhz/1000)
	}
	return fmt.Sprintf("%.0f MHz", mhz)
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
		data := pageData{Base: a.base, Host: a.host, MetricsURL: a.base + "metrics", Interval: a.interval.Milliseconds(), SampleAge: age, Fragment: template.HTML(fragment)}
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
	mux.HandleFunc("GET "+a.base+"api/sample.json", func(w http.ResponseWriter, r *http.Request) {
		a.mu.RLock()
		b := a.json
		age := max(int64(0), time.Since(a.sampled).Milliseconds())
		a.mu.RUnlock()
		if b == nil {
			http.Error(w, "no sample yet", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("X-PiPeek-Sample-Age", strconv.FormatInt(age, 10))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	})
	mux.HandleFunc("GET "+a.base+"hosts/{name}", func(w http.ResponseWriter, r *http.Request) { a.servePeer(w, r, false) })
	mux.HandleFunc("GET "+a.base+"hosts/{name}/metrics", func(w http.ResponseWriter, r *http.Request) { a.servePeer(w, r, true) })
	mux.HandleFunc("GET "+a.base+"api/fleet.json", func(w http.ResponseWriter, r *http.Request) {
		a.mu.RLock()
		b := a.fleetJSON
		a.mu.RUnlock()
		if b == nil {
			http.Error(w, "no peers configured (-peers)", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
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
