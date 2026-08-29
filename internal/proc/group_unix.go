//go:build !windows

package proc

import (
	"fmt"
	"os/exec"
	"sync"
	"syscall"
)

// Unix tarafında iş nesnesi yok; çocukları kendi süreç grubuna alıp gruba
// sinyal gönderiyoruz. Windows'un çekirdek düzeyindeki garantisi kadar güçlü
// değil (DevBox öldürülürse çocuklar kalır) ama geliştirme ve CI için yeterli.
type groupImpl struct {
	mu     sync.Mutex
	pids   []int
	closed bool
}

func newGroupImpl() (*groupImpl, error) { return &groupImpl{}, nil }

func (g *groupImpl) prepare(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

func (g *groupImpl) add(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pids = append(g.pids, cmd.Process.Pid)
	return nil
}

func (g *groupImpl) close() error {
	g.mu.Lock()
	pids := g.pids
	g.pids = nil
	g.closed = true
	g.mu.Unlock()

	for _, pid := range pids {
		// Negatif pid: süreç grubunun tamamı.
		syscall.Kill(-pid, syscall.SIGKILL)
	}
	return nil
}

// terminateTree, süreç grubuna SIGTERM gönderir.
//
// Negatif pid: çekirdek bunu "bu pid'li süreç grubunun tamamı" diye
// okuyor. prepare() Setpgid ile her süreci kendi grubunun lideri yaptığı
// için grup kimliği süreç kimliğine eşit.
func terminateTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return fmt.Errorf("proc: süreç başlatılmamış")
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err != nil {
		// Grup yoksa (Setpgid uygulanamadıysa) sürecin kendisine.
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	return nil
}

func killTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}
