package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCPU(t *testing.T) {
	before, err := parseCPU("cpu 100 20 30 400 10 5 5 10 90 10\ncpu0 0 0 0 0\n")
	if err != nil {
		t.Fatal(err)
	}
	if before.total != 580 || before.idle != 410 {
		t.Fatalf("guest time counted twice: %+v", before)
	}
	after, _ := parseCPU("cpu 120 20 40 460 20 5 5 10 90 10\n")
	v, ok := cpuUsage(before, after)
	if !ok || v != 30 {
		t.Fatalf("usage=%v ready=%v", v, ok)
	}
	for _, c := range []cpuCounters{before, {1, 1}, {600, 500}, {600, 1}} {
		if _, ok := cpuUsage(before, c); ok {
			t.Fatalf("accepted reset/invalid counters: %+v", c)
		}
	}
	if _, err := parseCPU("cpu nope 0 0 0"); err == nil {
		t.Fatal("accepted malformed CPU")
	}
}
func TestMemory(t *testing.T) {
	m, err := parseMemory("MemTotal: 1000 kB\nMemFree: 100 kB\nMemAvailable: 600 kB\nSwapTotal: 200 kB\nSwapFree: 150 kB\n")
	if err != nil || m.Used != 400*1024 || m.SwapUsed != 50*1024 {
		t.Fatalf("%+v %v", m, err)
	}
	if _, err := parseMemory("MemTotal: 1000 kB"); err == nil {
		t.Fatal("missing MemAvailable accepted")
	}
	m, err = parseMemory("MemTotal: 100 kB\nMemAvailable: 200 kB\n")
	if err != nil || m.Used != 0 || m.SwapUsed != 0 {
		t.Fatalf("underflow: %+v %v", m, err)
	}
}
func netFixture(rx, tx string) string {
	return "Inter-| Receive | Transmit\n face |bytes packets errs drop fifo frame compressed multicast|bytes packets errs drop fifo colls carrier compressed\n eth0: " + rx + " 0 0 0 0 0 0 0 " + tx + " 0 0 0 0 0 0 0\n lo: 999999 0 0 0 0 0 0 0 999999 0 0 0 0 0 0 0\n"
}
func TestCollectorRatesAndFailures(t *testing.T) {
	root := t.TempDir()
	write := func(name, data string) {
		t.Helper()
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("proc/stat", "cpu 100 0 0 900\n")
	write("proc/meminfo", "MemTotal: 1000 kB\nMemAvailable: 500 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n")
	write("proc/uptime", "90061.00 0.00\n")
	write("proc/net/dev", netFixture("1000", "2000"))
	write("sys/class/thermal/thermal_zone0/type", "cpu-thermal\n")
	write("sys/class/thermal/thermal_zone0/temp", "42500\n")
	c := collector{proc: filepath.Join(root, "proc"), sys: filepath.Join(root, "sys"), disks: []string{root}}
	now := time.Now()
	s := c.collect(now)
	if s.CPUOK || s.NetworkOK || !s.TempOK || s.Temperature != 42.5 || s.Uptime != "1d 01h 01m" || len(s.Networks) != 1 || len(s.Warnings) != 0 || !s.Disks[0].OK {
		t.Fatalf("initial: %+v", s)
	}
	write("proc/stat", "cpu 150 0 0 950\n")
	write("proc/net/dev", netFixture("1600", "3200"))
	s = c.collect(now.Add(3 * time.Second))
	if !s.CPUOK || s.CPU != 50 || !s.NetworkOK || s.RX != 200 || s.TX != 400 {
		t.Fatalf("rates: %+v", s)
	}
	write("proc/net/dev", netFixture("10", "20"))
	s = c.collect(now.Add(6 * time.Second))
	if s.NetworkOK || s.RX != 0 {
		t.Fatal("network reset became a spike")
	}
	c.interfaces = map[string]bool{"lo": true}
	s = c.collect(now.Add(9 * time.Second))
	if len(s.Networks) != 1 || s.Networks[0].Name != "lo" {
		t.Fatal("interface filter ignored")
	}
	write("proc/stat", "broken")
	write("proc/net/dev", "broken")
	write("sys/class/thermal/thermal_zone0/temp", "broken")
	s = c.collect(now.Add(12 * time.Second))
	if s.CPUOK || s.NetworkOK || s.TempOK || len(s.Warnings) != 2 {
		t.Fatalf("failure: %+v", s)
	}
	write("proc/stat", "cpu 1000 0 0 1000\n")
	write("proc/net/dev", netFixture("1000", "1000"))
	s = c.collect(now.Add(15 * time.Second))
	if s.CPUOK || s.NetworkOK {
		t.Fatal("rates must warm up after missing counters")
	}
}
func TestHistoryBoundAndGaps(t *testing.T) {
	a, err := newApp("/", "test", time.Second, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for i := 0; i < 100; i++ {
		a.update(sample{At: now.Add(time.Duration(i) * time.Second), CPU: 50, CPUOK: true})
	}
	if a.count != 11 || len(a.points) != 11 {
		t.Fatalf("unbounded history: %d %d", a.count, len(a.points))
	}
	a.update(sample{At: now.Add(time.Hour)})
	if !strings.Contains(string(a.fragment), "1 samples in memory") {
		t.Fatal("old samples survived time window")
	}
	p := []point{{At: now, CPU: 50, CPUOK: true}, {At: now.Add(time.Second)}, {At: now.Add(2 * time.Second), CPU: 20, CPUOK: true}}
	d := chart(p, now.Add(2*time.Second), 10*time.Second, time.Second, 100, func(p point) (float64, bool) { return p.CPU, p.CPUOK })
	if strings.Count(d, "M") != 2 || strings.Contains(d, "L") {
		t.Fatalf("chart bridges missing values: %s", d)
	}
	short := []point{{At: now, CPU: 50, CPUOK: true}, {At: now.Add(time.Second), CPU: 60, CPUOK: true}}
	d = chart(short, now.Add(time.Second), time.Second, time.Second, 100, func(p point) (float64, bool) { return p.CPU, p.CPUOK })
	if !strings.Contains(d, "L") {
		t.Fatal("short history disconnected adjacent samples")
	}
	short[1].At = now.Add(5 * time.Second)
	d = chart(short, short[1].At, 10*time.Second, time.Second, 100, func(p point) (float64, bool) { return p.CPU, p.CPUOK })
	if strings.Contains(d, "L") {
		t.Fatal("chart bridges a sampling outage")
	}

}

func TestRequiredMountDisappearanceAndRecovery(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "media disk")
	if err := os.Mkdir(target, 0755); err != nil {
		t.Fatal(err)
	}
	proc := filepath.Join(root, "proc")
	if err := os.MkdirAll(filepath.Join(proc, "self"), 0755); err != nil {
		t.Fatal(err)
	}
	mountFile := filepath.Join(proc, "self/mountinfo")
	write := func(data string) {
		t.Helper()
		if err := os.WriteFile(mountFile, []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	c := collector{proc: proc, sys: root, disks: []string{target}, requiredMounts: map[string]bool{target: true}}
	// The directory exists even though the drive was absent at startup.
	write("1 0 8:1 / / rw - ext4 /dev/root rw\n")
	if c.collect(time.Now()).Disks[0].OK {
		t.Fatal("unmounted directory accepted at startup")
	}
	entry := "2 1 8:2 / " + strings.ReplaceAll(target, " ", `\040`) + " rw - ext4 /dev/media rw\n"
	write(entry)
	if !c.collect(time.Now()).Disks[0].OK {
		t.Fatal("mounted disk unavailable")
	}
	write("")
	if c.collect(time.Now()).Disks[0].OK {
		t.Fatal("unmount silently fell back to parent filesystem")
	}
	write(entry)
	if !c.collect(time.Now()).Disks[0].OK {
		t.Fatal("remount did not recover")
	}
	if err := os.Remove(mountFile); err != nil {
		t.Fatal(err)
	}
	if c.collect(time.Now()).Disks[0].OK {
		t.Fatal("missing mount table accepted")
	}
	// Remembered filesystem identity must not be replaced on a mismatch.
	write(entry)
	expected := c.diskIDs[target]
	changed := expected
	changed.X__val[0] ^= 1
	c.diskIDs[target] = changed
	if c.collect(time.Now()).Disks[0].OK {
		t.Fatal("replacement filesystem accepted")
	}
	if c.diskIDs[target] != changed {
		t.Fatal("mismatch overwrote expected identity")
	}
	c.diskIDs[target] = expected
	if !c.collect(time.Now()).Disks[0].OK {
		t.Fatal("original filesystem did not recover")
	}
}
