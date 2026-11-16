package main

import (
	"bytes"
	"html/template"
	"net/http"
	"strconv"
	"time"
)

// The hub shows each peer's full dashboard at <base>hosts/<name>, drawn from
// the JSON it already polls. Only the hub has to be reachable from a browser;
// the peers only have to answer the hub.

func val[T any](p *T) (T, bool) {
	var zero T
	if p == nil {
		return zero, false
	}
	return *p, true
}

// sampleFromAPI turns a peer's /api/sample.json back into the sample the
// dashboard renders. Older peers omit newer fields; those stay unavailable.
func sampleFromAPI(d *apiSample) sample {
	// The JSON is in UTC; the page shows times in the hub's local zone.
	s := sample{At: d.SampledAt.Local(), Warnings: d.Warnings}
	s.CPU, s.CPUOK = val(d.CPU.Percent)
	s.Load, s.LoadOK = val(d.CPU.Load)
	s.FreqMHz, s.FreqOK = val(d.CPU.FrequencyMHz)
	s.MaxFreqMHz, _ = val(d.CPU.MaxFreqMHz)
	if d.Memory != nil {
		s.Memory = memory{d.Memory.Total, d.Memory.Used, d.Memory.SwapTotal, d.Memory.SwapUsed}
		s.MemoryOK = true
	}
	s.Temperature, s.TempOK = val(d.Temperature.Celsius)
	s.FanRPM, s.FanOK = val(d.Temperature.FanRPM)
	s.UnderVoltage, s.VoltageOK = val(d.Power.UnderVoltage)
	if d.Uptime != nil {
		s.UptimeSeconds, s.UptimeOK = *d.Uptime, true
		s.Uptime = formatUptime(*d.Uptime)
	}
	if req, ok := val(d.Reboot.Required); ok {
		s.Reboot = rebootStatus{Known: true, Required: req, Reasons: d.Reboot.Reasons}
		s.Reboot.Running, _ = val(d.Reboot.RunningKernel)
	}
	for _, x := range d.Disks {
		dk := disk{Path: x.Path, OK: x.OK}
		dk.Total, _ = val(x.Total)
		dk.Used, _ = val(x.Used)
		dk.Available, _ = val(x.Available)
		dk.Percent, _ = val(x.Percent)
		read, rok := val(x.ReadRate)
		write, wok := val(x.WriteRate)
		dk.ReadRate, dk.WriteRate, dk.IOOK = read, write, rok && wok
		s.Disks = append(s.Disks, dk)
	}
	rx, rok := val(d.Network.RX)
	tx, tok := val(d.Network.TX)
	s.RX, s.TX, s.NetworkOK = rx, tx, rok && tok
	for _, n := range d.Network.Interfaces {
		r, rok := val(n.RX)
		t, tok := val(n.TX)
		s.Networks = append(s.Networks, network{Name: n.Name, RX: r, TX: t, Ready: rok && tok})
	}
	return s
}

func statusFromClass(class string) temperatureStatus {
	for st := temperatureNormal; st <= temperatureVeryHot; st++ {
		if st.Class() == class {
			return st
		}
	}
	return temperatureUnavailable
}

// peerView builds the dashboard view of one peer. It reads the peer's
// history, so it runs under the fleet's read lock.
func (a *app) peerView(name string, now time.Time) (v view, st peerState, interval time.Duration, found bool) {
	a.fleet.mu.RLock()
	defer a.fleet.mu.RUnlock()
	for _, s := range a.fleet.states {
		if s.Peer.Name == name {
			st, found = s, true
			break
		}
	}
	if !found {
		return
	}
	interval = a.interval
	if st.Sample == nil {
		v.NoData = true
		v.Notice = name + " has not answered yet"
		if st.Error != "" {
			v.Notice += ": " + st.Error
		}
		return
	}
	if peer := time.Duration(st.Sample.Interval * float64(time.Second)); peer > interval {
		interval = peer
	}
	v.Sample = sampleFromAPI(st.Sample)
	v.TemperatureStatus = statusFromClass(st.Sample.Temperature.Status)
	if seen, ok := val(st.Sample.Power.UnderVoltageSeen); ok {
		v.UnderVoltageSeen = seen.Local()
	}
	if now.Sub(st.LastSeen) > 3*a.interval {
		v.Notice = name + " is not answering"
		if st.Error != "" {
			v.Notice += " (" + st.Error + ")"
		}
		v.Notice += ". Showing its last sample, fetched " + st.LastSeen.Format("Jan 2 15:04:05") + "."
	}
	st.hist.fill(&v, v.Sample.At, interval)
	return
}

type pageData struct {
	Base, Host, MetricsURL string
	Interval, SampleAge    int64
	Fragment               template.HTML
	// Set on a peer's page on the hub.
	Peer           bool
	Hub, OriginURL string
	BackURL        string
}

func (a *app) servePeer(w http.ResponseWriter, r *http.Request, fragmentOnly bool) {
	if a.fleet == nil {
		http.NotFound(w, r)
		return
	}
	name := r.PathValue("name")
	v, st, interval, found := a.peerView(name, time.Now())
	if !found {
		http.NotFound(w, r)
		return
	}
	var frag bytes.Buffer
	if err := a.templates.ExecuteTemplate(&frag, "metrics", v); err != nil {
		http.Error(w, "Unable to render dashboard", 500)
		return
	}
	// The age the browser judges freshness by: how old the sample was when
	// the hub fetched it, plus the time since.
	age := int64(0)
	if !st.LastSeen.IsZero() {
		age = (st.Age + time.Since(st.LastSeen)).Milliseconds()
	}
	if fragmentOnly {
		w.Header().Set("X-PiPeek-Sample-Age", strconv.FormatInt(age, 10))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(frag.Bytes())
		return
	}
	data := pageData{Base: a.base, Host: name, MetricsURL: a.base + "hosts/" + name + "/metrics", Interval: interval.Milliseconds(), SampleAge: age,
		Fragment: template.HTML(frag.String()), Peer: true, Hub: a.host, OriginURL: st.Peer.URL, BackURL: a.base}
	var buf bytes.Buffer
	if err := a.templates.ExecuteTemplate(&buf, "index.html", data); err != nil {
		http.Error(w, "Unable to render dashboard", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}
