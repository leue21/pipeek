package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestParseLoad(t *testing.T) {
	load, ok := parseLoad("0.09 0.11 1.50 2/534 516868\n")
	if !ok || load != [3]float64{0.09, 0.11, 1.5} {
		t.Fatalf("%v %v", load, ok)
	}
	for _, bad := range []string{"", "0.1 0.2", "x 0.2 0.3", "-1 0 0"} {
		if _, ok := parseLoad(bad); ok {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestDeviceNumber(t *testing.T) {
	// 179:2 is mmcblk0p2, a Raspberry Pi SD card root; 259:65537 needs the
	// extended minor bits.
	for dev, want := range map[uint64]string{0xb302: "179:2", 0x10010301: "259:65537", 0: "0:0"} {
		if got := deviceNumber(dev); got != want {
			t.Fatalf("deviceNumber(%#x) = %s, want %s", dev, got, want)
		}
	}
}

func TestPiReadings(t *testing.T) {
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
	write("proc/meminfo", "MemTotal: 1000 kB\nMemAvailable: 500 kB\n")
	write("proc/uptime", "100.00 0.00\n")
	write("proc/net/dev", netFixture("1000", "2000"))
	write("proc/loadavg", "0.50 0.25 0.10 1/100 42\n")
	var st syscall.Stat_t
	if err := syscall.Stat(root, &st); err != nil {
		t.Fatal(err)
	}
	dev := strings.Replace(deviceNumber(uint64(st.Dev)), ":", " ", 1)
	write("proc/diskstats", "   1 0 ram0 0 0 0 0 0 0 0 0 0 0 0\n "+dev+" sda1 10 0 2048 0 5 0 1024 0 0 0 0\n")
	write("sys/class/hwmon/hwmon0/name", "cpu_thermal\n")
	write("sys/class/hwmon/hwmon2/name", "rpi_volt\n")
	write("sys/class/hwmon/hwmon2/in0_lcrit_alarm", "0\n")
	write("sys/devices/system/cpu/cpufreq/policy0/scaling_cur_freq", "1500000\n")
	write("sys/devices/system/cpu/cpufreq/policy0/cpuinfo_max_freq", "2400000\n")

	c := collector{proc: filepath.Join(root, "proc"), sys: filepath.Join(root, "sys"), disks: []string{root}}
	now := time.Now()
	s := c.collect(now)
	if !s.LoadOK || s.Load[0] != 0.5 || !s.FreqOK || s.FreqMHz != 1500 || s.MaxFreqMHz != 2400 || !s.VoltageOK || s.UnderVoltage || s.FanOK {
		t.Fatalf("first sample: %+v", s)
	}
	if s.Disks[0].IOOK {
		t.Fatal("disk rate before a second sample")
	}
	if len(s.Warnings) != 0 {
		t.Fatalf("optional readings must not warn: %v", s.Warnings)
	}

	// A fan plugged in later is found by the next discovery, not every sample.
	write("sys/class/hwmon/hwmon3/fan1_input", "3200\n")
	write("sys/class/hwmon/hwmon2/in0_lcrit_alarm", "1\n")
	write("proc/diskstats", " "+dev+" sda1 10 0 4096 0 5 0 7168 0 0 0 0\n")
	s = c.collect(now.Add(2 * time.Second))
	if s.FanOK {
		t.Fatal("rediscovered before rediscoverEvery")
	}
	if !s.UnderVoltage {
		t.Fatal("under-voltage alarm not read")
	}
	d := s.Disks[0]
	if !d.IOOK || d.ReadRate != 2048*512/2 || d.WriteRate != 6144*512/2 {
		t.Fatalf("disk rates: %+v", d)
	}
	s = c.collect(now.Add(2*time.Second + rediscoverEvery))
	if !s.FanOK || s.FanRPM != 3200 {
		t.Fatalf("fan not found on rediscovery: %+v", s)
	}

	// Counters going backwards (device replaced) must not become a spike.
	write("proc/diskstats", " "+dev+" sda1 1 0 1 0 1 0 1 0 0 0 0\n")
	s = c.collect(now.Add(4*time.Second + rediscoverEvery))
	if s.Disks[0].IOOK {
		t.Fatal("counter reset became a rate")
	}

	// Sensors that disappear are hidden, not reported as failures.
	os.RemoveAll(filepath.Join(root, "sys/class/hwmon"))
	os.RemoveAll(filepath.Join(root, "sys/devices"))
	os.Remove(filepath.Join(root, "proc/loadavg"))
	s = c.collect(now.Add(6*time.Second + rediscoverEvery))
	if s.VoltageOK || s.FanOK || s.FreqOK || s.LoadOK || len(s.Warnings) != 0 {
		t.Fatalf("missing sensors: %+v", s)
	}
}

func TestSampleJSON(t *testing.T) {
	a, err := newApp("/pipeek/", "rpi5", 3*time.Second, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	a.update(sample{At: at, CPU: 12.5, CPUOK: true, Temperature: 65, TempOK: true, VoltageOK: true, UnderVoltage: true,
		Load: [3]float64{1, 2, 3}, LoadOK: true, Disks: []disk{{Path: "/", OK: true, Total: 100, ReadRate: 512, IOOK: true}, {Path: "/mnt/x"}}})
	a.update(sample{At: at.Add(3 * time.Second), CPU: 10, CPUOK: true, VoltageOK: true})

	srv := httptest.NewServer(a.handler())
	defer srv.Close()
	res, err := http.Get(srv.URL + "/pipeek/api/sample.json")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "application/json" || res.Header.Get("Cache-Control") != "no-store" || res.Header.Get("X-PiPeek-Sample-Age") == "" {
		t.Fatalf("status %d headers %v", res.StatusCode, res.Header)
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	cpu := doc["cpu"].(map[string]any)
	power := doc["power"].(map[string]any)
	temp := doc["temperature"].(map[string]any)
	if cpu["percent"] != 10.0 || cpu["load"] != nil || cpu["frequency_mhz"] != nil {
		t.Fatalf("cpu: %v", cpu)
	}
	if temp["celsius"] != nil || temp["status"] != "unavailable" {
		t.Fatalf("temperature: %v", temp)
	}
	if power["under_voltage"] != false || power["under_voltage_last_seen"] != "2026-11-16T12:00:00Z" {
		t.Fatalf("power: %v", power)
	}
	if doc["memory"] != nil || doc["host"] != "rpi5" || doc["sampled_at"] != "2026-11-16T12:00:03Z" {
		t.Fatalf("doc: %s", body)
	}
	if len(doc["disks"].([]any)) != 0 || doc["warnings"] == nil {
		t.Fatalf("empty lists must be [] not null: %s", body)
	}

	// The page shows the recovery, and the banner only while it is happening.
	res, _ = http.Get(srv.URL + "/pipeek/metrics")
	page, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if strings.Contains(string(page), "Under-voltage detected") || !strings.Contains(string(page), "Recovered") {
		t.Fatalf("recovered page: %s", page)
	}
	a.update(sample{At: at.Add(6 * time.Second), VoltageOK: true, UnderVoltage: true, FreqOK: true, FreqMHz: 1500, MaxFreqMHz: 2400, FanOK: true, FanRPM: 3100,
		Disks: []disk{{Path: "/", OK: true, Total: 100, ReadRate: 2048, WriteRate: 0, IOOK: true}}})
	res, _ = http.Get(srv.URL + "/pipeek/metrics")
	page, _ = io.ReadAll(res.Body)
	res.Body.Close()
	for _, want := range []string{"Under-voltage detected", "power-bad", "1.50 GHz", "of 2.40 GHz", "Fan 3100 rpm", "Read 2.0 KiB/s", "Write 0 B/s"} {
		if !strings.Contains(string(page), want) {
			t.Fatalf("page lacks %q", want)
		}
	}
}
