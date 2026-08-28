//go:build windows

package phppool

import "syscall"

const stillActive = 259

func processAlive(pid int) bool {
	h, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)

	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}

// killProcess, süreci dışarıdan sonlandırır: testlerde php-cgi'nin haber
// vermeden ölmesini taklit etmek için.
func killProcess(pid int) error {
	const processTerminate = 0x0001
	h, err := syscall.OpenProcess(processTerminate, false, uint32(pid))
	if err != nil {
		return err
	}
	defer syscall.CloseHandle(h)
	return syscall.TerminateProcess(h, 1)
}
