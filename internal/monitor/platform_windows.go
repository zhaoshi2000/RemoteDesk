//go:build windows

package monitor

import (
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

var kernel = syscall.NewLazyDLL("kernel32.dll")
var getTimes = kernel.NewProc("GetSystemTimes")
var getMemory = kernel.NewProc("GlobalMemoryStatusEx")
var getDisk = kernel.NewProc("GetDiskFreeSpaceExW")
var getProcessMemory = syscall.NewLazyDLL("psapi.dll").NewProc("GetProcessMemoryInfo")

type memoryStatus struct {
	Length, Load                                                                          uint32
	TotalPhys, AvailPhys, TotalPage, AvailPage, TotalVirtual, AvailVirtual, AvailExtended uint64
}
type processCounters struct {
	Size, Faults                                                                                                               uint32
	PeakWorkingSet, WorkingSet, PeakPagedPool, PagedPool, PeakNonPagedPool, NonPagedPool, Pagefile, PeakPagefile, PrivateUsage uintptr
}

func platform(path string) (Sample, Counters) {
	var s Sample
	var c Counters
	var idle, kernelTime, user uint64
	if ok, _, _ := getTimes.Call(uintptr(unsafe.Pointer(&idle)), uintptr(unsafe.Pointer(&kernelTime)), uintptr(unsafe.Pointer(&user))); ok != 0 {
		c.Total = kernelTime + user
		c.Idle = idle
		c.CPUOK = true
	}
	mem := memoryStatus{}
	mem.Length = uint32(unsafe.Sizeof(mem))
	if ok, _, _ := getMemory.Call(uintptr(unsafe.Pointer(&mem))); ok != 0 {
		total, used := mem.TotalPhys, mem.TotalPhys-mem.AvailPhys
		s.MemoryTotal = &total
		s.MemoryUsed = &used
	}
	pc := processCounters{}
	pc.Size = uint32(unsafe.Sizeof(pc))
	handle, e := syscall.GetCurrentProcess()
	if e == nil {
		if ok, _, _ := getProcessMemory.Call(uintptr(handle), uintptr(unsafe.Pointer(&pc)), uintptr(pc.Size)); ok != 0 {
			rss := uint64(pc.WorkingSet)
			s.ProcessRSS = &rss
		}
	}
	// Inspect the actual state volume; if it does not exist yet, use the working volume.
	if _, e := os.Stat(path); e != nil {
		path = "."
	}
	path, e = filepath.Abs(path)
	if e == nil {
		p, e := syscall.UTF16PtrFromString(path)
		if e == nil {
			var free, total, allFree uint64
			if ok, _, _ := getDisk.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&free)), uintptr(unsafe.Pointer(&total)), uintptr(unsafe.Pointer(&allFree))); ok != 0 && allFree <= total {
				used := total - allFree
				s.DiskTotal = &total
				s.DiskUsed = &used
			}
		}
	}
	// Windows network totals are not collected: nil must remain distinct from zero.
	return s, c
}
