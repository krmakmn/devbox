//go:build !linux && !windows

package procstat

func read(pid int) (Stat, error) { return Stat{}, ErrUnsupported }
