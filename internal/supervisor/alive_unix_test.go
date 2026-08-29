//go:build !windows

package supervisor

import "syscall"

func processAlive(pid int) bool {
	if pid == 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

// killProcess, süreci dışarıdan sonlandırır: denetçinin yeniden başlatma
// yolunu sınamak için.
func killProcess(pid int) error {
	return syscall.Kill(pid, syscall.SIGKILL)
}
