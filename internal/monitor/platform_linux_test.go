//go:build linux

package monitor

import "testing"

func TestCPUDoesNotCountGuestTwice(t *testing.T) {
	total, idle, ok := parseCPU("cpu  10 20 30 40 5 6 7 8 99 88\ncpu0 1 2\n")
	if !ok || total != 126 || idle != 45 {
		t.Fatal(total, idle, ok)
	}
}
func TestMemoryMissingIsUnavailable(t *testing.T) {
	_, _, ok := parseMemory("MemTotal: 100 kB\n")
	if ok {
		t.Fatal("missing available must not be zero")
	}
	total, used, ok := parseMemory("MemTotal: 100 kB\nMemAvailable: 30 kB\n")
	if !ok || total != 102400 || used != 71680 {
		t.Fatal(total, used, ok)
	}
}
func TestNetworkExcludesLoopback(t *testing.T) {
	rx, tx, ok := parseNetwork(" lo: 900 0 0 0 0 0 0 0 800 0 0 0 0 0 0 0\n eth0: 100 0 0 0 0 0 0 0 50 0 0 0 0 0 0 0\n")
	if !ok || rx != 100 || tx != 50 {
		t.Fatal(rx, tx, ok)
	}
}
