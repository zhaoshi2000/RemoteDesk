//go:build !linux

package monitor

// Host counters are intentionally unavailable outside the supported Linux server.
func platform(string) (Sample, Counters) { return Sample{}, Counters{} }
