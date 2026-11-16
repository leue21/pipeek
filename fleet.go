package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Hub mode: with -peers, this PiPeek also polls other PiPeeks' JSON API and
// shows every machine in an overview on its own page. Peers need nothing
// new; the JSON they already serve is the whole protocol.

type peer struct {
	Name string
	URL  string // the peer's dashboard, ending in "/"
}

// parsePeers reads "name=url,name=url". The URL is the peer's dashboard
// address, including any -base-path, e.g. https://pi3.example.ts.net:8443/.
func parsePeers(s string) ([]peer, error) {
	var peers []peer
	seen := map[string]bool{}
	for _, item := range splitList(s) {
		name, raw, ok := strings.Cut(item, "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			return nil, fmt.Errorf("peer %q: use name=url", item)
		}
		for _, r := range name {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
				return nil, fmt.Errorf("peer name %q: use letters, digits, '.', '-' and '_'", name)
			}
		}
		if seen[name] {
			return nil, fmt.Errorf("peer name %q is used twice", name)
		}
		seen[name] = true
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return nil, fmt.Errorf("peer %q: %q is not an http(s) dashboard URL", name, raw)
		}
		if !strings.HasSuffix(u.Path, "/") {
			u.Path += "/"
		}
		peers = append(peers, peer{Name: name, URL: u.String()})
	}
	return peers, nil
}

type peerState struct {
	Peer     peer
	Sample   *apiSample      // last good sample, nil if never reached
	Raw      json.RawMessage // the same sample as the peer sent it
	LastSeen time.Time       // when Sample was fetched
	Age      time.Duration   // how old the peer said the sample was then
	Error    string          // why the last fetch failed; "" if it worked
	hist     *history        // chart points built from the samples fetched
}

// A peer's answer is untrusted input: it is size-limited, must be valid JSON,
// and everything shown from it goes through html/template escaping.
const maxPeerResponse = 256 << 10

type fleet struct {
	peers    []peer
	client   *http.Client
	interval time.Duration
	mu       sync.RWMutex
	states   []peerState
}

// The hub fetches each peer at most once per interval, so a history the size
// of its own holds the whole window for every peer.
func newFleet(peers []peer, interval, window time.Duration) *fleet {
	f := &fleet{peers: peers, interval: interval, states: make([]peerState, len(peers))}
	for i, p := range peers {
		f.states[i] = peerState{Peer: p, hist: newHistory(window, interval)}
	}
	// Short timeout: an unreachable peer must not hold up the others, and the
	// next poll is only one interval away.
	f.client = &http.Client{Timeout: min(interval, 5*time.Second)}
	return f
}

func (f *fleet) run(ctx context.Context) {
	ticker := time.NewTicker(f.interval)
	defer ticker.Stop()
	for {
		f.poll(ctx)
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return
		}
	}
}

func (f *fleet) poll(ctx context.Context) {
	var wg sync.WaitGroup
	for i, p := range f.peers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, raw, age, err := f.fetch(ctx, p)
			f.mu.Lock()
			defer f.mu.Unlock()
			st := &f.states[i]
			if err != nil {
				st.Error = err.Error()
				return
			}
			// A peer sampling slower than we poll returns the same sample
			// twice; only a newer one is a new chart point.
			if s.SampledAt.After(st.hist.last()) {
				st.hist.add(sampleFromAPI(s))
			}
			st.Sample, st.Raw, st.LastSeen, st.Age, st.Error = s, raw, time.Now(), age, ""
		}()
	}
	wg.Wait()
}

func (f *fleet) fetch(ctx context.Context, p peer) (*apiSample, json.RawMessage, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", p.URL+"api/sample.json", nil)
	if err != nil {
		return nil, nil, 0, err
	}
	req.Header.Set("User-Agent", "PiPeek/"+buildVersion())
	res, err := f.client.Do(req)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("not reachable")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, nil, 0, fmt.Errorf("answered %s", res.Status)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxPeerResponse+1))
	if err != nil {
		return nil, nil, 0, fmt.Errorf("not reachable")
	}
	if len(body) > maxPeerResponse {
		return nil, nil, 0, fmt.Errorf("answer too large")
	}
	var s apiSample
	if err := json.Unmarshal(body, &s); err != nil || s.SampledAt.IsZero() {
		return nil, nil, 0, fmt.Errorf("not a PiPeek")
	}
	ms, _ := strconv.ParseInt(res.Header.Get("X-PiPeek-Sample-Age"), 10, 64)
	return &s, json.RawMessage(body), time.Duration(max(ms, 0)) * time.Millisecond, nil
}

func (f *fleet) snapshot() []peerState {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return append([]peerState(nil), f.states...)
}

// One card in the overview, for this machine or a peer.
type hostCard struct {
	Name, URL     string
	Self, Online  bool
	Selected      bool   // the machine whose data the page shows
	Status        string // "Online", "Offline", "Never reached"
	Since         time.Time
	Reason        string
	CPU, Memory   *float64
	Temperature   *float64
	TempClass     string
	UnderVoltage  bool
	RestartNeeded bool
	DiskPath      string
	DiskPercent   *float64
	DiskMissing   bool
	Warnings      int
}

// A peer counts as offline once its last good sample is older than three of
// our intervals, the same rule /healthz uses for this machine.
func peerCard(st peerState, base string, now time.Time, interval time.Duration) hostCard {
	c := hostCard{Name: st.Peer.Name, URL: base + "hosts/" + st.Peer.Name, TempClass: "unavailable"}
	if st.Sample == nil {
		c.Status, c.Reason = "Never reached", st.Error
		return c
	}
	if now.Sub(st.LastSeen) > 3*interval {
		c.Status, c.Since, c.Reason = "Offline", st.LastSeen, st.Error
		return c
	}
	s := st.Sample
	c.Online, c.Status = true, "Online"
	c.CPU = s.CPU.Percent
	if s.Memory != nil {
		c.Memory = &s.Memory.Percent
	}
	c.Temperature = s.Temperature.Celsius
	switch s.Temperature.Status {
	case "normal", "warm", "hot", "very-hot":
		c.TempClass = s.Temperature.Status
	}
	c.UnderVoltage = s.Power.UnderVoltage != nil && *s.Power.UnderVoltage
	c.RestartNeeded = s.Reboot.Required != nil && *s.Reboot.Required
	for _, d := range s.Disks {
		if !d.OK {
			c.DiskMissing = true
			continue
		}
		if d.Percent != nil && (c.DiskPercent == nil || *d.Percent > *c.DiskPercent) {
			c.DiskPath, c.DiskPercent = d.Path, d.Percent
		}
	}
	c.Warnings = len(s.Warnings)
	return c
}

func selfCard(s sample, host, base string, status temperatureStatus) hostCard {
	c := hostCard{Name: host, URL: base, Self: true, Online: true, Status: "This machine", TempClass: status.Class()}
	c.CPU = ptr(s.CPU, s.CPUOK)
	if s.MemoryOK {
		c.Memory = ptr(percent(s.Memory.Used, s.Memory.Total), true)
	}
	c.Temperature = ptr(s.Temperature, s.TempOK)
	c.UnderVoltage = s.VoltageOK && s.UnderVoltage
	c.RestartNeeded = s.Reboot.Known && s.Reboot.Required
	for _, d := range s.Disks {
		if !d.OK {
			c.DiskMissing = true
			continue
		}
		if c.DiskPercent == nil || d.Percent > *c.DiskPercent {
			c.DiskPath, c.DiskPercent = d.Path, ptr(d.Percent, true)
		}
	}
	c.Warnings = len(s.Warnings)
	return c
}

// The document served at <base>api/fleet.json: this machine and every peer,
// each with its sample exactly as /api/sample.json gives it, or null.
type apiFleetHost struct {
	Name     string          `json:"name"`
	URL      string          `json:"url"`
	Self     bool            `json:"self"`
	Online   bool            `json:"online"`
	LastSeen *time.Time      `json:"last_seen"`
	Error    *string         `json:"error"`
	Sample   json.RawMessage `json:"sample"`
}

func fleetJSON(self []byte, host, base string, states []peerState, now time.Time, interval time.Duration) ([]byte, error) {
	hosts := []apiFleetHost{{Name: host, URL: base, Self: true, Online: true, Sample: self}}
	for _, st := range states {
		h := apiFleetHost{Name: st.Peer.Name, URL: st.Peer.URL, Sample: json.RawMessage("null")}
		if st.Sample != nil {
			seen := st.LastSeen.UTC()
			h.LastSeen, h.Sample = &seen, st.Raw
			h.Online = now.Sub(st.LastSeen) <= 3*interval
		}
		h.Error = ptr(st.Error, st.Error != "")
		hosts = append(hosts, h)
	}
	return json.Marshal(struct {
		Hosts []apiFleetHost `json:"hosts"`
	}{hosts})
}
