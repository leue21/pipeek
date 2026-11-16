package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// What a peer sends must come back as the same sample on the hub.
func TestSampleRoundTrip(t *testing.T) {
	at := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	in := sample{At: at, CPU: 12.5, CPUOK: true, Temperature: 61, TempOK: true, FanRPM: 3100, FanOK: true,
		FreqMHz: 1500, MaxFreqMHz: 2400, FreqOK: true, Load: [3]float64{1, 2, 3}, LoadOK: true,
		VoltageOK: true, UnderVoltage: true, Uptime: formatUptime(90061), UptimeSeconds: 90061, UptimeOK: true,
		Memory: memory{1000, 400, 200, 50}, MemoryOK: true, RX: 10, TX: 20, NetworkOK: true,
		Networks: []network{{Name: "eth0", RX: 10, TX: 20, Ready: true}, {Name: "wlan0"}},
		Disks:    []disk{{Path: "/", Total: 100, Used: 40, Available: 60, Percent: 40, OK: true, ReadRate: 5, WriteRate: 6, IOOK: true}, {Path: "/mnt/x"}},
		Reboot:   rebootStatus{Known: true, Required: true, Reasons: []string{"Kernel 6.18.70 is installed"}, Running: "6.18.60"},
		Warnings: []string{"Required mount unavailable: /mnt/x"}}
	a, _ := newApp("/", "pi3", 3*time.Second, time.Minute)
	b, err := a.apiJSON(in, temperatureWarm, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	var doc apiSample
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	out := sampleFromAPI(&doc)
	out.At = out.At.UTC()
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip changed the sample:\n in %+v\nout %+v", in, out)
	}
	if statusFromClass(doc.Temperature.Status) != temperatureWarm || statusFromClass("bogus") != temperatureUnavailable {
		t.Fatal("temperature status")
	}
}

func TestHostPages(t *testing.T) {
	peerApp, _ := newApp("/", "pi3", time.Second, time.Minute)
	start := time.Now().Add(-10 * time.Second)
	peerApp.update(sample{At: start, CPU: 20, CPUOK: true, Disks: []disk{{Path: "/<i>evil", OK: true, Percent: 10}}})
	peer := httptest.NewServer(peerApp.handler())
	defer peer.Close()

	peers, _ := parsePeers("pi3=" + peer.URL + ",ghost=http://127.0.0.1:1")
	hub, _ := newApp("/pipeek/", "rpi5", time.Second, time.Minute)
	hub.fleet = newFleet(peers, time.Second, time.Minute)
	hub.fleet.poll(context.Background())
	hub.fleet.poll(context.Background()) // same sample again: not a second point
	peerApp.update(sample{At: start.Add(time.Second), CPU: 30, CPUOK: true})
	hub.fleet.poll(context.Background())
	if n := hub.fleet.states[0].hist.count; n != 2 {
		t.Fatalf("history points: %d, want 2", n)
	}
	hub.update(sample{At: time.Now()})
	srv := httptest.NewServer(hub.handler())
	defer srv.Close()

	_, overview := get(t, srv.URL+"/pipeek/")
	if !strings.Contains(overview, `href="/pipeek/hosts/pi3"`) {
		t.Fatal("overview card does not link to the hub's page for the peer")
	}
	code, page := get(t, srv.URL+"/pipeek/hosts/pi3")
	for _, want := range []string{"PiPeek · pi3", "via rpi5", `href="/pipeek/">← All machines`, `href="` + peer.URL + `/">Open on pi3`,
		`hx-get="/pipeek/hosts/pi3/metrics"`, `data-sampled="` + strconv.FormatInt(start.Add(time.Second).UnixMilli(), 10), "30.0", "2 samples in memory"} {
		if code != 200 || !strings.Contains(page, want) {
			t.Fatalf("peer page lacks %q (status %d)", want, code)
		}
	}
	if strings.Contains(page, "ALL MACHINES") || strings.Contains(page, "<i>evil") {
		t.Fatal("peer page shows the overview or unescaped peer content")
	}

	res, err := http.Get(srv.URL + "/pipeek/hosts/pi3/metrics")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if age, err := strconv.Atoi(res.Header.Get("X-PiPeek-Sample-Age")); err != nil || age < 0 || res.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("fragment headers: %v", res.Header)
	}

	_, ghost := get(t, srv.URL+"/pipeek/hosts/ghost")
	if !strings.Contains(ghost, "ghost has not answered yet: not reachable") || strings.Contains(ghost, "RESOURCE USAGE") {
		t.Fatal("never-reached peer page")
	}
	if code, _ := get(t, srv.URL+"/pipeek/hosts/nobody"); code != 404 {
		t.Fatalf("unknown host: %d", code)
	}

	// Offline: the last sample stays, with a notice.
	peer.Close()
	hub.fleet.poll(context.Background())
	hub.fleet.states[0].LastSeen = time.Now().Add(-time.Minute)
	_, offline := get(t, srv.URL+"/pipeek/hosts/pi3")
	if !strings.Contains(offline, "pi3 is not answering (not reachable). Showing its last sample") || !strings.Contains(offline, "30.0") {
		t.Fatal("offline peer page")
	}

	solo, _ := newApp("/", "rpi5", time.Second, time.Minute)
	solo.update(sample{At: time.Now()})
	soloSrv := httptest.NewServer(solo.handler())
	defer soloSrv.Close()
	if code, _ := get(t, soloSrv.URL+"/hosts/pi3"); code != 404 {
		t.Fatal("host pages without -peers")
	}
}
