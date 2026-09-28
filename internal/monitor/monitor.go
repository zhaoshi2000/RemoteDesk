// Package monitor collects real host/process samples. Unsupported metrics are nil,
// never fabricated zeroes. Network counters are host-wide, not P2P traffic.
package monitor

import (
	"os"
	"runtime"
	"sync"
	"time"
)

type Sample struct {
	At          time.Time `json:"at"`
	CPUPercent  *float64  `json:"cpu_percent"`
	MemoryTotal *uint64   `json:"memory_total"`
	MemoryUsed  *uint64   `json:"memory_used"`
	ProcessRSS  *uint64   `json:"process_rss"`
	DiskTotal   *uint64   `json:"disk_total"`
	DiskUsed    *uint64   `json:"disk_used"`
	NetworkRX   *uint64   `json:"network_rx"`
	NetworkTX   *uint64   `json:"network_tx"`
	RXPerSecond *float64  `json:"rx_per_second"`
	TXPerSecond *float64  `json:"tx_per_second"`
	HeapBytes   uint64    `json:"heap_bytes"`
	Goroutines  int       `json:"goroutines"`
}
type Counters struct {
	Total, Idle, RX, TX uint64
	CPUOK, NetOK        bool
}
type Collector struct {
	mu       sync.Mutex
	path     string
	previous Counters
	last     time.Time
	history  []Sample
}

func New(path string) *Collector {
	if path == "" {
		path = "."
	}
	return &Collector{path: path}
}
func (c *Collector) Collect() Sample {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now().UTC()
	sample, next := platform(c.path)
	sample.At = now
	elapsed := now.Sub(c.last).Seconds()
	if !c.last.IsZero() && elapsed > 0 {
		if next.CPUOK && c.previous.CPUOK && next.Total > c.previous.Total && next.Idle >= c.previous.Idle {
			total := next.Total - c.previous.Total
			idle := next.Idle - c.previous.Idle
			if idle <= total {
				v := 100 * float64(total-idle) / float64(total)
				sample.CPUPercent = &v
			}
		}
		if next.NetOK && c.previous.NetOK && next.RX >= c.previous.RX && next.TX >= c.previous.TX {
			rx, tx := float64(next.RX-c.previous.RX)/elapsed, float64(next.TX-c.previous.TX)/elapsed
			sample.RXPerSecond = &rx
			sample.TXPerSecond = &tx
		}
	}
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	sample.HeapBytes = m.Alloc
	sample.Goroutines = runtime.NumGoroutine()
	c.previous = next
	c.last = now
	if len(c.history) == 720 {
		copy(c.history, c.history[1:])
		c.history = c.history[:719]
	}
	c.history = append(c.history, sample)
	return sample
}
func (c *Collector) History() []Sample {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Sample, len(c.history))
	copy(out, c.history)
	return out
}
func (c *Collector) Latest() Sample {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.history) == 0 {
		return Sample{}
	}
	return c.history[len(c.history)-1]
}
func Hostname() string {
	s, e := os.Hostname()
	if e != nil {
		return "unknown"
	}
	return s
}
