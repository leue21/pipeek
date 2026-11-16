package main

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Whether the machine needs a restart, from two signals readable without
// root and inside the service sandbox:
//
//   - a kernel newer than the running one installed in /boot, of the same
//     flavor (rpi-2712, v8, amd64, generic, ...)
//   - /run/reboot-required, which Debian and Ubuntu packages create (with the
//     package names in /run/reboot-required.pkgs)
//
// Services that need a restart after a library upgrade (what needrestart
// reports) would need root to inspect other processes, so they are not shown.
// Kernel installs are rare, so this runs once a minute, not every sample.
const rebootCheckEvery = time.Minute

type rebootStatus struct {
	Known, Required bool
	Reasons         []string
	Running         string // the running kernel release, when known
}

func (c *collector) collectReboot(s *sample, now time.Time) {
	if c.rebootAt.IsZero() || now.Sub(c.rebootAt) >= rebootCheckEvery {
		c.rebootAt = now
		running := ""
		if data, err := c.read("sys/kernel/osrelease"); err == nil {
			running = strings.TrimSpace(data)
		}
		c.reboot = checkReboot(c.bootDir, c.runDir, running)
	}
	s.Reboot = c.reboot
}

func checkReboot(boot, run, running string) rebootStatus {
	r := rebootStatus{Running: running}
	if running != "" {
		images, _ := filepath.Glob(filepath.Join(boot, "vmlinuz-*"))
		newest := ""
		for _, image := range images {
			v := strings.TrimPrefix(filepath.Base(image), "vmlinuz-")
			if kernelFlavor(v) != kernelFlavor(running) {
				continue
			}
			if newest == "" || compareVersions(v, newest) > 0 {
				newest = v
			}
		}
		// Without an installed kernel of the running flavor (a custom kernel,
		// a container, /boot elsewhere) there is nothing to compare.
		if newest != "" {
			r.Known = true
			if compareVersions(newest, running) > 0 {
				r.Required = true
				r.Reasons = append(r.Reasons, "Kernel "+newest+" is installed; "+running+" is running")
			}
		}
	}
	if _, err := os.Stat(filepath.Join(run, "reboot-required")); err == nil {
		r.Known, r.Required = true, true
		reason := "Requested by a package update"
		if b, err := os.ReadFile(filepath.Join(run, "reboot-required.pkgs")); err == nil {
			if pkgs := uniqueFields(string(b)); len(pkgs) > 0 {
				reason = "Requested by " + strings.Join(pkgs, ", ")
			}
		}
		r.Reasons = append(r.Reasons, reason)
	}
	return r
}

// The flavor is what follows the last dash: 6.18.60+rpt-rpi-2712 -> 2712,
// 6.1.0-18-amd64 -> amd64, 6.8.0-45-generic -> generic.
func kernelFlavor(release string) string {
	if i := strings.LastIndexByte(release, '-'); i >= 0 {
		return release[i+1:]
	}
	return ""
}

// compareVersions compares runs of digits numerically and everything else as
// text, so 6.18.60 > 6.18.9 and 6.1.0-20 > 6.1.0-18.
func compareVersions(a, b string) int {
	for a != "" && b != "" {
		ra, restA := versionRun(a)
		rb, restB := versionRun(b)
		if c := compareRun(ra, rb); c != 0 {
			return c
		}
		a, b = restA, restB
	}
	switch {
	case a != "":
		return 1
	case b != "":
		return -1
	}
	return 0
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func versionRun(s string) (string, string) {
	digit := isDigit(s[0])
	i := 1
	for i < len(s) && isDigit(s[i]) == digit {
		i++
	}
	return s[:i], s[i:]
}

func compareRun(a, b string) int {
	if isDigit(a[0]) && isDigit(b[0]) {
		a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
		if len(a) != len(b) {
			if len(a) > len(b) {
				return 1
			}
			return -1
		}
	}
	return strings.Compare(a, b)
}

func uniqueFields(s string) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range strings.Fields(s) {
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}
