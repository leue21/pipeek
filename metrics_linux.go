package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type cpuCounters struct{ total, idle uint64 }
type netCounters struct{ rx, tx uint64 }
type memory struct{ Total, Used, SwapTotal, SwapUsed uint64 }
type disk struct {
	Path                   string
	Total, Used, Available uint64
	Percent                float64
	OK                     bool
}
type network struct {
	Name   string
	RX, TX float64
	Ready  bool
}
type sample struct {
	At                                time.Time
	CPU, Temperature                  float64
	CPUOK, TempOK, MemoryOK, UptimeOK bool
	Memory                            memory
	Uptime                            string
	Disks                             []disk
	Networks                          []network
	RX, TX                            float64
	NetworkOK                         bool
	Warnings                          []string
}
type collector struct {
	proc, sys  string
	disks      []string
	interfaces map[string]bool
	cpu        cpuCounters
	cpuAt      time.Time
	nets       map[string]netCounters
	netAt      time.Time
	tempPath   string
}

func parseCPU(data string) (cpuCounters, error) {
	fields := strings.Fields(strings.SplitN(data, "\n", 2)[0])
	if len(fields) < 5 || fields[0] != "cpu" {
		return cpuCounters{}, fmt.Errorf("missing aggregate CPU counters")
	}
	var c cpuCounters
	// Guest time is already included in user/nice; only count the first eight fields.
	for i := 1; i < len(fields) && i <= 8; i++ {
		v, err := strconv.ParseUint(fields[i], 10, 64)
		if err != nil {
			return c, err
		}
		c.total += v
		if i == 4 || i == 5 {
			c.idle += v
		}
	}
	return c, nil
}
func cpuUsage(before, after cpuCounters) (float64, bool) {
	if after.total <= before.total || after.idle < before.idle {
		return 0, false
	}
	total, idle := after.total-before.total, after.idle-before.idle
	if idle > total {
		return 0, false
	}
	return float64(total-idle) / float64(total) * 100, true
}
func parseMemory(data string) (memory, error) {
	values := map[string]uint64{}
	for _, line := range strings.Split(data, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		v, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil {
			return memory{}, err
		}
		values[strings.TrimSuffix(f[0], ":")] = v * 1024
	}
	total, ok := values["MemTotal"]
	available, availOK := values["MemAvailable"]
	if !ok || !availOK || total == 0 {
		return memory{}, fmt.Errorf("missing memory counters")
	}
	swap := values["SwapTotal"]
	return memory{total, total - min(total, available), swap, swap - min(swap, values["SwapFree"])}, nil
}
func parseNetwork(data string) (map[string]netCounters, error) {
	result := map[string]netCounters{}
	for _, line := range strings.Split(data, "\n") {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) < 16 {
			return nil, fmt.Errorf("invalid interface counters")
		}
		rx, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			return nil, err
		}
		tx, err := strconv.ParseUint(fields[8], 10, 64)
		if err != nil {
			return nil, err
		}
		result[strings.TrimSpace(name)] = netCounters{rx, tx}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("missing interface counters")
	}
	return result, nil
}
func (c *collector) read(name string) (string, error) {
	b, e := os.ReadFile(filepath.Join(c.proc, name))
	return string(b), e
}
func (c *collector) collect(now time.Time) sample {
	s := sample{At: now}
	data, err := c.read("stat")
	var cpu cpuCounters
	if err == nil {
		cpu, err = parseCPU(data)
	}
	if err == nil {
		if !c.cpuAt.IsZero() {
			s.CPU, s.CPUOK = cpuUsage(c.cpu, cpu)
		}
		c.cpu, c.cpuAt = cpu, now
	} else {
		c.cpuAt = time.Time{}
		s.Warnings = append(s.Warnings, "CPU counters unavailable")
	}
	data, err = c.read("meminfo")
	if err == nil {
		s.Memory, err = parseMemory(data)
	}
	s.MemoryOK = err == nil
	if err != nil {
		s.Warnings = append(s.Warnings, "Memory counters unavailable")
	}
	data, err = c.read("uptime")
	if err == nil {
		f := strings.Fields(data)
		if len(f) > 0 {
			seconds, e := strconv.ParseFloat(f[0], 64)
			if e == nil && seconds >= 0 && !math.IsNaN(seconds) && !math.IsInf(seconds, 0) {
				s.Uptime = formatUptime(seconds)
				s.UptimeOK = true
			}
		}
	}
	if !s.UptimeOK {
		s.Warnings = append(s.Warnings, "Uptime unavailable")
	}
	if c.tempPath == "" {
		c.tempPath = findTemperature(c.sys)
	}
	if c.tempPath != "" {
		b, e := os.ReadFile(c.tempPath)
		if e == nil {
			v, e := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
			if e == nil && v > -50000 && v < 200000 {
				s.Temperature = v / 1000
				s.TempOK = true
			}
		}
		if !s.TempOK {
			c.tempPath = ""
		}
	}
	for _, path := range c.disks {
		d := disk{Path: path}
		var st syscall.Statfs_t
		if e := syscall.Statfs(path, &st); e == nil {
			d.Total = st.Blocks * uint64(st.Bsize)
			d.Used = (st.Blocks - min(st.Blocks, st.Bfree)) * uint64(st.Bsize)
			d.Available = st.Bavail * uint64(st.Bsize)
			d.Percent = percent(d.Used, d.Used+d.Available)
			d.OK = true
		} else {
			s.Warnings = append(s.Warnings, "Disk unavailable: "+path)
		}
		s.Disks = append(s.Disks, d)
	}
	data, err = c.read("net/dev")
	var nets map[string]netCounters
	if err == nil {
		nets, err = parseNetwork(data)
	}
	if err != nil {
		s.Warnings = append(s.Warnings, "Network counters unavailable")
		c.netAt = time.Time{}
		c.nets = nil
		return s
	}
	elapsed := now.Sub(c.netAt).Seconds()
	for name, n := range nets {
		if (len(c.interfaces) == 0 && name == "lo") || (len(c.interfaces) > 0 && !c.interfaces[name]) {
			continue
		}
		row := network{Name: name}
		if prev, ok := c.nets[name]; ok && !c.netAt.IsZero() && elapsed > 0 && n.rx >= prev.rx && n.tx >= prev.tx {
			row.RX = float64(n.rx-prev.rx) / elapsed
			row.TX = float64(n.tx-prev.tx) / elapsed
			row.Ready = true
			s.NetworkOK = true
			s.RX += row.RX
			s.TX += row.TX
		}
		s.Networks = append(s.Networks, row)
	}
	sort.Slice(s.Networks, func(i, j int) bool { return s.Networks[i].Name < s.Networks[j].Name })
	c.nets, c.netAt = nets, now
	return s
}
func findTemperature(sys string) string {
	paths, _ := filepath.Glob(filepath.Join(sys, "class/thermal/thermal_zone*"))
	for _, p := range paths {
		b, _ := os.ReadFile(filepath.Join(p, "type"))
		kind := strings.TrimSpace(string(b))
		if kind == "cpu-thermal" || kind == "bcm2835_thermal" || kind == "x86_pkg_temp" || kind == "cpu_thermal" {
			return filepath.Join(p, "temp")
		}
	}
	return ""
}
func percent(used, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(used) / float64(total) * 100
}
func formatUptime(seconds float64) string {
	n := int64(seconds)
	return fmt.Sprintf("%dd %02dh %02dm", n/86400, n/3600%24, n/60%60)
}
