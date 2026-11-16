package main

import (
	"encoding/json"
	"time"
)

// The JSON document served at <base>api/sample.json. It is built once per
// sample, like the HTML fragment, so readers cost a copy and nothing more.
// A value PiPeek could not read is null rather than 0, so a script can tell
// "no reading" from "zero".
type apiSample struct {
	Version     string         `json:"version"`
	Host        string         `json:"host"`
	SampledAt   time.Time      `json:"sampled_at"`
	Interval    float64        `json:"interval_seconds"`
	Uptime      *float64       `json:"uptime_seconds"`
	CPU         apiCPU         `json:"cpu"`
	Memory      *apiMemory     `json:"memory"`
	Temperature apiTemperature `json:"temperature"`
	Power       apiPower       `json:"power"`
	Reboot      apiReboot      `json:"reboot"`
	Disks       []apiDisk      `json:"disks"`
	Network     apiNetwork     `json:"network"`
	Warnings    []string       `json:"warnings"`
}
type apiCPU struct {
	Percent      *float64    `json:"percent"`
	Load         *[3]float64 `json:"load"`
	FrequencyMHz *float64    `json:"frequency_mhz"`
	MaxFreqMHz   *float64    `json:"max_frequency_mhz"`
}
type apiMemory struct {
	Total     uint64  `json:"total_bytes"`
	Used      uint64  `json:"used_bytes"`
	Percent   float64 `json:"percent"`
	SwapTotal uint64  `json:"swap_total_bytes"`
	SwapUsed  uint64  `json:"swap_used_bytes"`
}
type apiTemperature struct {
	Celsius *float64 `json:"celsius"`
	// normal, warm, hot, very-hot or unavailable, as the dashboard shows it.
	Status string   `json:"status"`
	FanRPM *float64 `json:"fan_rpm"`
}
type apiPower struct {
	// null when the board has no under-voltage monitor (anything but a Pi).
	UnderVoltage *bool `json:"under_voltage"`
	// Last sample with under-voltage since PiPeek started; null if none.
	UnderVoltageSeen *time.Time `json:"under_voltage_last_seen"`
}
type apiReboot struct {
	// null when PiPeek cannot tell (no installed kernel to compare against).
	Required *bool    `json:"required"`
	Reasons  []string `json:"reasons"`
}
type apiDisk struct {
	Path      string   `json:"path"`
	OK        bool     `json:"ok"`
	Total     *uint64  `json:"total_bytes"`
	Used      *uint64  `json:"used_bytes"`
	Available *uint64  `json:"available_bytes"`
	Percent   *float64 `json:"percent"`
	ReadRate  *float64 `json:"read_bytes_per_second"`
	WriteRate *float64 `json:"write_bytes_per_second"`
}
type apiNetwork struct {
	RX         *float64       `json:"rx_bytes_per_second"`
	TX         *float64       `json:"tx_bytes_per_second"`
	Interfaces []apiInterface `json:"interfaces"`
}
type apiInterface struct {
	Name string   `json:"name"`
	RX   *float64 `json:"rx_bytes_per_second"`
	TX   *float64 `json:"tx_bytes_per_second"`
}

func ptr[T any](v T, ok bool) *T {
	if !ok {
		return nil
	}
	return &v
}

func (a *app) apiJSON(s sample, status temperatureStatus, underVoltageSeen time.Time) ([]byte, error) {
	doc := apiSample{
		Version:   buildVersion(),
		Host:      a.host,
		SampledAt: s.At.UTC(),
		Interval:  a.interval.Seconds(),
		Uptime:    ptr(s.UptimeSeconds, s.UptimeOK),
		CPU: apiCPU{
			Percent:      ptr(s.CPU, s.CPUOK),
			Load:         ptr(s.Load, s.LoadOK),
			FrequencyMHz: ptr(s.FreqMHz, s.FreqOK),
			MaxFreqMHz:   ptr(s.MaxFreqMHz, s.FreqOK && s.MaxFreqMHz > 0),
		},
		Temperature: apiTemperature{
			Celsius: ptr(s.Temperature, s.TempOK),
			Status:  status.Class(),
			FanRPM:  ptr(s.FanRPM, s.FanOK),
		},
		Power: apiPower{
			UnderVoltage:     ptr(s.UnderVoltage, s.VoltageOK),
			UnderVoltageSeen: ptr(underVoltageSeen.UTC(), !underVoltageSeen.IsZero()),
		},
		Reboot:   apiReboot{Required: ptr(s.Reboot.Required, s.Reboot.Known), Reasons: append([]string{}, s.Reboot.Reasons...)},
		Disks:    []apiDisk{},
		Network:  apiNetwork{RX: ptr(s.RX, s.NetworkOK), TX: ptr(s.TX, s.NetworkOK), Interfaces: []apiInterface{}},
		Warnings: append([]string{}, s.Warnings...),
	}
	if s.MemoryOK {
		m := s.Memory
		doc.Memory = &apiMemory{m.Total, m.Used, percent(m.Used, m.Total), m.SwapTotal, m.SwapUsed}
	}
	for _, d := range s.Disks {
		doc.Disks = append(doc.Disks, apiDisk{
			Path: d.Path, OK: d.OK,
			Total: ptr(d.Total, d.OK), Used: ptr(d.Used, d.OK), Available: ptr(d.Available, d.OK), Percent: ptr(d.Percent, d.OK),
			ReadRate: ptr(d.ReadRate, d.IOOK), WriteRate: ptr(d.WriteRate, d.IOOK),
		})
	}
	for _, n := range s.Networks {
		doc.Network.Interfaces = append(doc.Network.Interfaces, apiInterface{Name: n.Name, RX: ptr(n.RX, n.Ready), TX: ptr(n.TX, n.Ready)})
	}
	return json.Marshal(doc)
}
