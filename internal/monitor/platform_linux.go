//go:build linux

package monitor

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func platform(path string) (Sample, Counters) {
	var s Sample
	var c Counters
	if data, e := os.ReadFile("/proc/stat"); e == nil {
		c.Total, c.Idle, c.CPUOK = parseCPU(string(data))
	}
	if data, e := os.ReadFile("/proc/meminfo"); e == nil {
		total, used, ok := parseMemory(string(data))
		if ok {
			s.MemoryTotal = &total
			s.MemoryUsed = &used
		}
	}
	if data, e := os.ReadFile("/proc/self/statm"); e == nil {
		f := strings.Fields(string(data))
		if len(f) > 1 {
			pages, e := strconv.ParseUint(f[1], 10, 64)
			if e == nil {
				v := pages * uint64(os.Getpagesize())
				s.ProcessRSS = &v
			}
		}
	}
	var disk syscall.Statfs_t
	if syscall.Statfs(path, &disk) == nil && disk.Bsize > 0 {
		total := disk.Blocks * uint64(disk.Bsize)
		used := (disk.Blocks - disk.Bfree) * uint64(disk.Bsize)
		s.DiskTotal = &total
		s.DiskUsed = &used
	}
	if data, e := os.ReadFile("/proc/net/dev"); e == nil {
		c.RX, c.TX, c.NetOK = parseNetwork(string(data))
		if c.NetOK {
			rx, tx := c.RX, c.TX
			s.NetworkRX = &rx
			s.NetworkTX = &tx
		}
	}
	return s, c
}
func parseCPU(data string) (total, idle uint64, ok bool) {
	line := strings.SplitN(data, "\n", 2)[0]
	f := strings.Fields(line)
	if len(f) < 5 || f[0] != "cpu" {
		return
	}
	// guest/guest_nice are already included in user/nice, do not double-count them.
	for i := 1; i < len(f) && i <= 8; i++ {
		v, e := strconv.ParseUint(f[i], 10, 64)
		if e != nil {
			return 0, 0, false
		}
		total += v
		if i == 4 || i == 5 {
			idle += v
		}
	}
	return total, idle, true
}
func parseMemory(data string) (total, used uint64, ok bool) {
	fields := map[string]uint64{}
	scan := bufio.NewScanner(strings.NewReader(data))
	for scan.Scan() {
		f := strings.Fields(scan.Text())
		if len(f) >= 2 {
			v, e := strconv.ParseUint(f[1], 10, 64)
			if e == nil {
				fields[strings.TrimSuffix(f[0], ":")] = v * 1024
			}
		}
	}
	total = fields["MemTotal"]
	available, exists := fields["MemAvailable"]
	if total == 0 || !exists || available > total {
		return 0, 0, false
	}
	return total, total - available, true
}
func parseNetwork(data string) (rx, tx uint64, ok bool) {
	scan := bufio.NewScanner(strings.NewReader(data))
	for scan.Scan() {
		pair := strings.SplitN(scan.Text(), ":", 2)
		if len(pair) != 2 || strings.TrimSpace(pair[0]) == "lo" {
			continue
		}
		f := strings.Fields(pair[1])
		if len(f) < 16 {
			continue
		}
		r, e1 := strconv.ParseUint(f[0], 10, 64)
		t, e2 := strconv.ParseUint(f[8], 10, 64)
		if e1 == nil && e2 == nil {
			rx += r
			tx += t
			ok = true
		}
	}
	return
}
