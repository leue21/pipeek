package main

import (
	"math"
	"time"
)

// history is a fixed-size ring of chart points for one machine: this one,
// or a peer the hub polls. It is not safe for concurrent use; the owner
// (the sampling goroutine, or the fleet under its mutex) serializes access.
type history struct {
	window      time.Duration
	points      []point
	next, count int
}

func newHistory(window, interval time.Duration) *history {
	return &history{window: window, points: make([]point, int(window/interval)+1)}
}

func (h *history) add(s sample) {
	h.points[h.next] = point{At: s.At, CPU: s.CPU, Memory: percent(s.Memory.Used, s.Memory.Total), Temperature: s.Temperature, RX: s.RX, TX: s.TX, CPUOK: s.CPUOK, MemoryOK: s.MemoryOK, TempOK: s.TempOK, NetworkOK: s.NetworkOK}
	h.next = (h.next + 1) % len(h.points)
	if h.count < len(h.points) {
		h.count++
	}
}

// last is the time of the newest point, or zero if there is none.
func (h *history) last() time.Time {
	if h.count == 0 {
		return time.Time{}
	}
	return h.points[(h.next-1+len(h.points))%len(h.points)].At
}

// fill draws the charts into v for the window ending at now. A gap longer
// than two intervals breaks the line instead of bridging an outage.
func (h *history) fill(v *view, now time.Time, interval time.Duration) {
	points := make([]point, 0, h.count)
	for i := 0; i < h.count; i++ {
		p := h.points[(h.next-h.count+i+len(h.points))%len(h.points)]
		if !p.At.Before(now.Add(-h.window)) {
			points = append(points, p)
		}
	}
	scale := 1024.0
	for _, p := range points {
		scale = math.Max(scale, math.Max(p.RX, p.TX))
	}
	v.Count, v.History, v.NetworkScale = len(points), h.window.String(), formatBytes(scale)+"/s"
	v.CPUChart = chart(points, now, h.window, interval, 100, func(p point) (float64, bool) { return p.CPU, p.CPUOK })
	v.MemoryChart = chart(points, now, h.window, interval, 100, func(p point) (float64, bool) { return p.Memory, p.MemoryOK })
	v.TempChart = chart(points, now, h.window, interval, 100, func(p point) (float64, bool) { return p.Temperature, p.TempOK })
	v.RXChart = chart(points, now, h.window, interval, scale, func(p point) (float64, bool) { return p.RX, p.NetworkOK })
	v.TXChart = chart(points, now, h.window, interval, scale, func(p point) (float64, bool) { return p.TX, p.NetworkOK })
}
