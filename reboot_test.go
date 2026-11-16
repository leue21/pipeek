package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"6.18.60+rpt-rpi-2712", "6.18.50+rpt-rpi-2712", 1},
		{"6.18.9+rpt-rpi-2712", "6.18.60+rpt-rpi-2712", -1}, // numeric, not text
		{"6.1.0-20-amd64", "6.1.0-18-amd64", 1},
		{"6.8.0-45-generic", "6.8.0-45-generic", 0},
		{"6.10.0", "6.9.12", 1},
		{"6.1.0-018-amd64", "6.1.0-18-amd64", 0},
	} {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestCheckReboot(t *testing.T) {
	boot, run := t.TempDir(), t.TempDir()
	touch := func(dir, name, data string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	running := "6.18.60+rpt-rpi-2712"

	if r := checkReboot(boot, run, running); r.Known || r.Required {
		t.Fatalf("no kernels installed must be unknown: %+v", r)
	}
	// A newer kernel of another flavor (the Pi 4 one on a Pi 5) is not ours.
	touch(boot, "vmlinuz-6.18.50+rpt-rpi-2712", "")
	touch(boot, "vmlinuz-6.18.60+rpt-rpi-2712", "")
	touch(boot, "vmlinuz-6.18.70+rpt-rpi-v8", "")
	if r := checkReboot(boot, run, running); !r.Known || r.Required {
		t.Fatalf("running the newest: %+v", r)
	}
	touch(boot, "vmlinuz-6.18.70+rpt-rpi-2712", "")
	r := checkReboot(boot, run, running)
	if !r.Required || len(r.Reasons) != 1 || !strings.Contains(r.Reasons[0], "6.18.70+rpt-rpi-2712 is installed") {
		t.Fatalf("newer kernel: %+v", r)
	}

	// Debian and Ubuntu's flag, with and without the package list.
	other := t.TempDir()
	touch(run, "reboot-required", "*** System restart required ***\n")
	if r := checkReboot(other, run, "6.8.0-45-generic"); !r.Known || !r.Required || r.Reasons[0] != "Requested by a package update" {
		t.Fatalf("reboot-required: %+v", r)
	}
	touch(run, "reboot-required.pkgs", "libc6\nlinux-image-6.8.0-47-generic\nlibc6\n")
	if r := checkReboot(other, run, "6.8.0-45-generic"); r.Reasons[0] != "Requested by libc6, linux-image-6.8.0-47-generic" {
		t.Fatalf("reboot-required.pkgs: %+v", r)
	}
}

func TestRebootCheckedOncePerMinute(t *testing.T) {
	root := t.TempDir()
	for name, data := range map[string]string{
		"proc/sys/kernel/osrelease":         "6.18.60+rpt-rpi-2712\n",
		"boot/vmlinuz-6.18.60+rpt-rpi-2712": "",
	} {
		p := filepath.Join(root, name)
		os.MkdirAll(filepath.Dir(p), 0755)
		os.WriteFile(p, []byte(data), 0644)
	}
	c := collector{proc: filepath.Join(root, "proc"), bootDir: filepath.Join(root, "boot"), runDir: filepath.Join(root, "run")}
	now := time.Now()
	var s sample
	c.collectReboot(&s, now)
	if !s.Reboot.Known || s.Reboot.Required {
		t.Fatalf("first check: %+v", s.Reboot)
	}
	os.WriteFile(filepath.Join(root, "boot/vmlinuz-6.18.70+rpt-rpi-2712"), nil, 0644)
	c.collectReboot(&s, now.Add(30*time.Second))
	if s.Reboot.Required {
		t.Fatal("checked again before a minute passed")
	}
	c.collectReboot(&s, now.Add(rebootCheckEvery))
	if !s.Reboot.Required {
		t.Fatal("new kernel not noticed after a minute")
	}
}
