//go:build !windows

package telemetry

import (
	"context"
	"os"
	"strings"
)

func platformInspect(_ context.Context, s *System) {
	if b, e := os.ReadFile("/etc/os-release"); e == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "PRETTY_NAME=") {
				s.OSVersion = strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), "\"")
			}
		}
	}
	if b, e := os.ReadFile("/proc/cpuinfo"); e == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "model name") {
				_, s.CPUModel, _ = strings.Cut(line, ":")
				s.CPUModel = strings.TrimSpace(s.CPUModel)
				break
			}
		}
	}
	if s.CPUModel != "" {
		s.HardwareProbe = "partial"
	}
}
