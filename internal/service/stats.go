package service

import (
	"math"
	"time"
)

// ReplayRatePerHour estimates replay downloads per hour once polling is paid for.
func ReplayRatePerHour(hourlyBudget, enabledPlayers int, pollInterval time.Duration) float64 {
	if pollInterval <= 0 {
		pollInterval = DefaultSettings.PollInterval
	}
	pollCost := float64(enabledPlayers) * float64(time.Hour) / float64(pollInterval)
	return math.Max(float64(hourlyBudget)-pollCost, 1)
}

// ETA estimates how long `pending` downloads take at `perHour`.
func ETA(pending int64, perHour float64) time.Duration {
	if pending <= 0 || perHour <= 0 {
		return 0
	}
	return time.Duration(float64(pending) / perHour * float64(time.Hour))
}
