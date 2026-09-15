package main

import "math"

type temperatureStatus int

const (
	temperatureUnavailable temperatureStatus = iota
	temperatureNormal
	temperatureWarm
	temperatureHot
	temperatureVeryHot
)

// Escalate at 60/70/80°C; cool below 58/68/78°C before stepping down.
// The sampler owns this state, so every viewer sees the same status.
func nextTemperatureStatus(previous temperatureStatus, degrees float64, available bool) temperatureStatus {
	if !available || math.IsNaN(degrees) || math.IsInf(degrees, 0) {
		return temperatureUnavailable
	}
	current := temperatureNormal
	if degrees >= 80 {
		current = temperatureVeryHot
	} else if degrees >= 70 {
		current = temperatureHot
	} else if degrees >= 60 {
		current = temperatureWarm
	}
	if previous < current || previous == temperatureUnavailable {
		return current
	}
	for previous > current {
		threshold := float64(60 + 10*(previous-temperatureWarm))
		if degrees >= threshold-2 {
			break
		}
		previous--
	}
	return previous
}

func (s temperatureStatus) Class() string {
	switch s {
	case temperatureNormal:
		return "normal"
	case temperatureWarm:
		return "warm"
	case temperatureHot:
		return "hot"
	case temperatureVeryHot:
		return "very-hot"
	default:
		return "unavailable"
	}
}
func (s temperatureStatus) Label() string {
	switch s {
	case temperatureNormal:
		return "Normal"
	case temperatureWarm:
		return "Warm"
	case temperatureHot:
		return "Hot"
	case temperatureVeryHot:
		return "Very hot"
	default:
		return "Unavailable"
	}
}
