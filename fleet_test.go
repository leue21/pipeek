package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParsePeers(t *testing.T) {
	peers, err := parsePeers("pi3=https://pi3.example.ts.net:8443, bolt=http://100.76.149.12:8080/pipeek")
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 2 || peers[0].URL != "https://pi3.example.ts.net:8443/" || peers[1].URL != "http://100.76.149.12:8080/pipeek/" {
		t.Fatalf("%+v", peers)
	}
	if peers, err := parsePeers(""); err != nil || peers != nil {
		t.Fatal("no peers must be valid")
	}
	for _, bad := range []string{
		"pi3", "=http://x", "pi3=ftp://x", "pi3=http://", "pi 3=http://x",
		"pi3=http://user:pw@x", "pi3=http://x/?a=1", "a=http://x,a=http://y",
	} {
		if _, err := parsePeers(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	return res.StatusCode, string(b)
}

func TestFleet(t *testing.T) {
	// A real PiPeek as a peer, under a base path, with a hostile hostname
	// and disk path: whatever a peer sends must come out escaped.
	peerApp, err := newApp("/pipeek/", "<script>x</script>", time.Second, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	peerApp.update(sample{At: time.Now(), CPU: 12, CPUOK: true, Temperature: 72, TempOK: true, VoltageOK: true, UnderVoltage: true,
		Disks:  []disk{{Path: "/<b>data", OK: true, Percent: 81}, {Path: "/mnt/gone"}},
		Reboot: rebootStatus{Known: true, Required: true}, Warnings: []string{"Required mount unavailable: /mnt/gone"}})
	good := httptest.NewServer(peerApp.handler())
	defer good.Close()
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "nope", 500) }))
	defer broken.Close()
	notPipeek := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"hello":"world"}`) }))
	defer notPipeek.Close()
	huge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"sampled_at":"2026-11-16T12:00:00Z","x":"`+strings.Repeat("a", maxPeerResponse)+`"}`)
	}))
	defer huge.Close()

	peers, err := parsePeers("garage=" + good.URL + "/pipeek/,nas=" + broken.URL + ",tv=" + notPipeek.URL + ",big=" + huge.URL + ",gone=http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	hub, err := newApp("/", "rpi5", time.Second, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	hub.fleet = newFleet(peers, time.Second, time.Minute)
	hub.fleet.poll(context.Background())
	now := time.Now()
	hub.update(sample{At: now, CPU: 5, CPUOK: true})

	errors := map[string]string{}
	for _, st := range hub.fleet.snapshot() {
		errors[st.Peer.Name] = st.Error
	}
	want := map[string]string{"garage": "", "nas": "answered 500 Internal Server Error", "tv": "not a PiPeek", "big": "answer too large", "gone": "not reachable"}
	for name, e := range want {
		if errors[name] != e {
			t.Fatalf("%s: error %q, want %q", name, errors[name], e)
		}
	}

	srv := httptest.NewServer(hub.handler())
	defer srv.Close()
	_, page := get(t, srv.URL+"/metrics")
	for _, s := range []string{"ALL MACHINES", "6 hosts", "rpi5", "This machine", ">garage<", "⚡ Under-voltage", "Restart needed", "Disk missing", "/&lt;b&gt;data", "81.0%", "temp-hot", "1 warning", "Never reached", "answered 500"} {
		if !strings.Contains(page, s) {
			t.Fatalf("overview lacks %q:\n%s", s, page)
		}
	}
	if strings.Contains(page, "<script>x") || strings.Contains(page, "<b>data") {
		t.Fatal("peer content not escaped")
	}

	code, body := get(t, srv.URL+"/api/fleet.json")
	var doc struct {
		Hosts []struct {
			Name   string          `json:"name"`
			Self   bool            `json:"self"`
			Online bool            `json:"online"`
			Error  *string         `json:"error"`
			Sample json.RawMessage `json:"sample"`
		} `json:"hosts"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &doc) != nil || len(doc.Hosts) != 6 {
		t.Fatalf("fleet.json %d: %s", code, body)
	}
	if !doc.Hosts[0].Self || !doc.Hosts[1].Online || !strings.Contains(string(doc.Hosts[1].Sample), `"under_voltage":true`) {
		t.Fatalf("fleet.json hosts: %s", body)
	}
	if doc.Hosts[2].Online || string(doc.Hosts[2].Sample) != "null" || doc.Hosts[2].Error == nil {
		t.Fatalf("unreached peer: %s", body)
	}

	// A peer that stops answering keeps its last sample but goes offline
	// after three intervals, like /healthz.
	good.Close()
	hub.fleet.poll(context.Background())
	if c := peerCard(hub.fleet.snapshot()[0], "/", now.Add(2*time.Second), time.Second); !c.Online {
		t.Fatal("offline too early")
	}
	c := peerCard(hub.fleet.snapshot()[0], "/", now.Add(10*time.Second), time.Second)
	if c.Online || c.Status != "Offline" || c.Since.IsZero() || c.Reason != "not reachable" {
		t.Fatalf("offline card: %+v", c)
	}
}

func TestNoFleetWithoutPeers(t *testing.T) {
	a, err := newApp("/", "rpi5", time.Second, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	a.update(sample{At: time.Now()})
	srv := httptest.NewServer(a.handler())
	defer srv.Close()
	if code, _ := get(t, srv.URL+"/api/fleet.json"); code != 404 {
		t.Fatalf("fleet.json without peers: %d", code)
	}
	if _, page := get(t, srv.URL+"/metrics"); strings.Contains(page, "ALL MACHINES") {
		t.Fatal("overview shown without peers")
	}
}
