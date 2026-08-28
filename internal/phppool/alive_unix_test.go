//go:build !windows

package phppool

import "syscall"

// processAlive, sürecin hâlâ var olup olmadığını söyler. 0 sinyali süreci
// etkilemez, yalnızca varlığını sınar.
func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// killProcess, süreci dışarıdan sonlandırır: testlerde php-cgi'nin haber
// vermeden ölmesini taklit etmek için.
func killProcess(pid int) error {
	return syscall.Kill(pid, syscall.SIGKILL)
}
