//go:build windows

package monitor

import (
	"testing"
	"time"
)

func TestWindowsRealHostMetrics(t *testing.T) {
	c := New(t.TempDir())
	a := c.Collect()
	if a.MemoryTotal == nil || *a.MemoryTotal == 0 || a.MemoryUsed == nil || a.ProcessRSS == nil || a.DiskTotal == nil {
		t.Fatal("Windows native counters unavailable", a)
	}
	time.Sleep(120 * time.Millisecond)
	b := c.Collect()
	if b.CPUPercent == nil || *b.CPUPercent < 0 || *b.CPUPercent > 100 {
		t.Fatal("invalid CPU sample", b.CPUPercent)
	}
	if b.NetworkRX != nil || b.RXPerSecond != nil {
		t.Fatal("unsupported network counters must stay nil")
	}
}
