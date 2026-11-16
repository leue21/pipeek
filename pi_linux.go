package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Optional readings: a board without the sensor simply does not show it.
// None of them is a warning or marks /healthz degraded, and none needs more
// than read access to /proc and /sys.

// Missing sensors are looked for again at most this often, so a board without
// them costs one directory listing a minute rather than one per sample.
const rediscoverEvery = time.Minute

type ioCounters struct {
	dev           string
	read, written uint64 // bytes
}

// discover finds the sysfs files for optional readings. It runs on the first
// sample, and again only when something is missing and rediscoverEvery passed.
func (c *collector) discover(now time.Time) {
	complete := c.voltPath != "" && c.fanPath != "" && len(c.freqPaths) > 0
	if complete || (!c.discoveredAt.IsZero() && now.Sub(c.discoveredAt) < rediscoverEvery) {
		return
	}
	c.discoveredAt = now
	hwmons, _ := filepath.Glob(filepath.Join(c.sys, "class/hwmon/hwmon*"))
	for _, dir := range hwmons {
		name, _ := os.ReadFile(filepath.Join(dir, "name"))
		// rpi_volt is the Raspberry Pi firmware's under-voltage monitor.
		if c.voltPath == "" && strings.TrimSpace(string(name)) == "rpi_volt" {
			if p := filepath.Join(dir, "in0_lcrit_alarm"); exists(p) {
				c.voltPath = p
			}
		}
		if c.fanPath == "" {
			if p := filepath.Join(dir, "fan1_input"); exists(p) {
				c.fanPath = p
			}
		}
	}
	if len(c.freqPaths) == 0 {
		policies, _ := filepath.Glob(filepath.Join(c.sys, "devices/system/cpu/cpufreq/policy*"))
		for _, dir := range policies {
			if exists(filepath.Join(dir, "scaling_cur_freq")) {
				c.freqPaths = append(c.freqPaths, dir)
			}
		}
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func readNumber(path string) (float64, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
	return v, err == nil
}

// collectPi fills in the optional readings. A file that stops being readable
// is forgotten, so it is looked for again by the next discovery.
func (c *collector) collectPi(s *sample, now time.Time) {
	c.discover(now)
	if c.voltPath != "" {
		if v, ok := readNumber(c.voltPath); ok {
			s.UnderVoltage, s.VoltageOK = v != 0, true
		} else {
			c.voltPath = ""
		}
	}
	if c.fanPath != "" {
		if v, ok := readNumber(c.fanPath); ok && v >= 0 {
			s.FanRPM, s.FanOK = v, true
		} else {
			c.fanPath = ""
		}
	}
	// Cores on separate clocks (big.LITTLE, most x86) report the fastest.
	for _, dir := range c.freqPaths {
		cur, ok := readNumber(filepath.Join(dir, "scaling_cur_freq"))
		if !ok {
			c.freqPaths = nil
			s.FreqOK = false
			break
		}
		limit, _ := readNumber(filepath.Join(dir, "cpuinfo_max_freq"))
		s.FreqMHz = max(s.FreqMHz, cur/1000)
		s.MaxFreqMHz = max(s.MaxFreqMHz, limit/1000)
		s.FreqOK = true
	}
	if data, err := c.read("loadavg"); err == nil {
		s.Load, s.LoadOK = parseLoad(data)
	}
}

func parseLoad(data string) ([3]float64, bool) {
	var load [3]float64
	f := strings.Fields(data)
	if len(f) < 3 {
		return load, false
	}
	for i := range load {
		v, err := strconv.ParseFloat(f[i], 64)
		if err != nil || v < 0 {
			return load, false
		}
		load[i] = v
	}
	return load, true
}

// parseDiskstats returns bytes read and written per "major:minor". Sectors in
// /proc/diskstats are always 512 bytes, whatever the device's sector size.
func parseDiskstats(data string) map[string]ioCounters {
	result := map[string]ioCounters{}
	for _, line := range strings.Split(data, "\n") {
		f := strings.Fields(line)
		if len(f) < 10 {
			continue
		}
		read, err1 := strconv.ParseUint(f[5], 10, 64)
		written, err2 := strconv.ParseUint(f[9], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		dev := f[0] + ":" + f[1]
		result[dev] = ioCounters{dev: dev, read: read * 512, written: written * 512}
	}
	return result
}

// Linux's dev_t layout, as glibc's major() and minor() decode it.
func deviceNumber(dev uint64) string {
	major := (dev>>8)&0xfff | (dev>>32)&^uint64(0xfff)
	minor := dev&0xff | (dev>>12)&^uint64(0xff)
	return strconv.FormatUint(major, 10) + ":" + strconv.FormatUint(minor, 10)
}

// collectDiskIO adds read/write rates to filesystems backed by a block device.
// tmpfs, overlayfs, btrfs subvolumes and network mounts have none and show no
// rate; that is not an error.
func (c *collector) collectDiskIO(s *sample, now time.Time) {
	data, err := c.read("diskstats")
	if err != nil {
		c.io, c.ioAt = nil, time.Time{}
		return
	}
	stats := parseDiskstats(data)
	elapsed := now.Sub(c.ioAt).Seconds()
	next := make(map[string]ioCounters, len(s.Disks))
	for i := range s.Disks {
		d := &s.Disks[i]
		if !d.OK {
			continue
		}
		var st syscall.Stat_t
		if syscall.Stat(d.Path, &st) != nil {
			continue
		}
		cur, ok := stats[deviceNumber(uint64(st.Dev))]
		if !ok {
			continue
		}
		next[d.Path] = cur
		prev, seen := c.io[d.Path]
		if seen && prev.dev == cur.dev && !c.ioAt.IsZero() && elapsed > 0 && cur.read >= prev.read && cur.written >= prev.written {
			d.ReadRate = float64(cur.read-prev.read) / elapsed
			d.WriteRate = float64(cur.written-prev.written) / elapsed
			d.IOOK = true
		}
	}
	c.io, c.ioAt = next, now
}
