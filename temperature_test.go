package main

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestTemperatureBands(t *testing.T) {
	for _, tc := range []struct {
		temp float64
		want temperatureStatus
	}{{0, temperatureNormal}, {59.9, temperatureNormal}, {60, temperatureWarm}, {69.9, temperatureWarm}, {70, temperatureHot}, {79.9, temperatureHot}, {80, temperatureVeryHot}, {95, temperatureVeryHot}} {
		if got := nextTemperatureStatus(temperatureUnavailable, tc.temp, true); got != tc.want {
			t.Errorf("%.1f: got %v want %v", tc.temp, got, tc.want)
		}
	}
}
func TestTemperatureHysteresis(t *testing.T) {
	state := temperatureUnavailable
	for _, tc := range []struct {
		temp float64
		ok   bool
		want temperatureStatus
	}{
		{59, true, temperatureNormal}, {60, true, temperatureWarm}, {59, true, temperatureWarm}, {58, true, temperatureWarm}, {57.9, true, temperatureNormal},
		{70, true, temperatureHot}, {69, true, temperatureHot}, {68, true, temperatureHot}, {67.9, true, temperatureWarm},
		{80, true, temperatureVeryHot}, {79, true, temperatureVeryHot}, {78, true, temperatureVeryHot}, {77.9, true, temperatureHot},
		{50, true, temperatureNormal}, {95, true, temperatureVeryHot}, {59, true, temperatureWarm},
		{0, false, temperatureUnavailable}, {59, true, temperatureNormal}, {math.NaN(), true, temperatureUnavailable}, {math.Inf(1), true, temperatureUnavailable},
	} {
		state = nextTemperatureStatus(state, tc.temp, tc.ok)
		if state != tc.want {
			t.Fatalf("%.1f available=%v: got %v want %v", tc.temp, tc.ok, state, tc.want)
		}
	}
}
func TestTemperatureStatusRendered(t *testing.T) {
	a, err := newApp("/", "test", time.Second, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		temp         float64
		ok           bool
		class, label string
	}{{45, true, "normal", "Normal"}, {60, true, "warm", "Warm"}, {70, true, "hot", "Hot"}, {80, true, "very-hot", "Very hot"}, {0, false, "unavailable", "Unavailable"}} {
		a.update(sample{At: time.Now(), Temperature: tc.temp, TempOK: tc.ok})
		html := string(a.fragment)
		if !strings.Contains(html, `class="card temperature temperature-`+tc.class+`"`) || !strings.Contains(html, `class="pill temperature-status">`+tc.label+`</span>`) {
			t.Fatalf("missing temperature class/label for %s", tc.label)
		}
	}
}
