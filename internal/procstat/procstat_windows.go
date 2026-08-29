//go:build windows

package procstat

import (
	"syscall"
	"unsafe"
)

var (
	kernel32                 = syscall.NewLazyDLL("kernel32.dll")
	psapi                    = syscall.NewLazyDLL("psapi.dll")
	procGetProcessTimes      = kernel32.NewProc("GetProcessTimes")
	procGetProcessMemoryInfo = psapi.NewProc("GetProcessMemoryInfo")
	processQueryLimitedInfo  = uint32(0x1000)
	processVMRead            = uint32(0x0010)
)

// processMemoryCounters, PROCESS_MEMORY_COUNTERS.
type processMemoryCounters struct {
	CB                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

// fileTimeToSeconds, FILETIME'ı (100 nanosaniyelik aralıklar) saniyeye
// çevirir.
func fileTimeToSeconds(ft syscall.Filetime) float64 {
	ticks := uint64(ft.HighDateTime)<<32 | uint64(ft.LowDateTime)
	return float64(ticks) / 1e7
}

func read(pid int) (Stat, error) {
	// PROCESS_QUERY_LIMITED_INFORMATION, yükseltilmemiş bir süreçten
	// kendi çocuklarını sorgulamaya yetiyor; tam erişim istemek
	// gereksiz yere geniş olurdu.
	h, err := syscall.OpenProcess(processQueryLimitedInfo|processVMRead, false, uint32(pid))
	if err != nil {
		return Stat{}, err
	}
	defer syscall.CloseHandle(h)

	var creation, exit, kernel, user syscall.Filetime
	ret, _, err := procGetProcessTimes.Call(uintptr(h),
		uintptr(unsafe.Pointer(&creation)), uintptr(unsafe.Pointer(&exit)),
		uintptr(unsafe.Pointer(&kernel)), uintptr(unsafe.Pointer(&user)))
	if ret == 0 {
		return Stat{}, err
	}

	var counters processMemoryCounters
	counters.CB = uint32(unsafe.Sizeof(counters))
	ret, _, err = procGetProcessMemoryInfo.Call(uintptr(h),
		uintptr(unsafe.Pointer(&counters)), uintptr(counters.CB))
	if ret == 0 {
		return Stat{}, err
	}

	return Stat{
		RSS:        uint64(counters.WorkingSetSize),
		CPUSeconds: fileTimeToSeconds(kernel) + fileTimeToSeconds(user),
	}, nil
}
