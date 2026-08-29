//go:build windows

package proc

import (
	"fmt"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"unsafe"
)

var (
	kernel32                  = syscall.NewLazyDLL("kernel32.dll")
	procCreateJobObjectW      = kernel32.NewProc("CreateJobObjectW")
	procSetInformationJobObj  = kernel32.NewProc("SetInformationJobObject")
	procAssignProcessToJobObj = kernel32.NewProc("AssignProcessToJobObject")
	procTerminateJobObject    = kernel32.NewProc("TerminateJobObject")
)

const (
	jobObjectExtendedLimitInformation = 9
	jobObjectLimitKillOnJobClose      = 0x00002000
	jobObjectLimitBreakawayOK         = 0x00000800

	processSetQuota  = 0x0100
	processTerminate = 0x0001
)

type ioCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

type jobObjectBasicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type jobObjectExtendedLimitInfo struct {
	BasicLimitInformation jobObjectBasicLimitInformation
	IoInfo                ioCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

type groupImpl struct {
	mu     sync.Mutex
	job    syscall.Handle
	closed bool
}

func newGroupImpl() (*groupImpl, error) {
	h, _, err := procCreateJobObjectW.Call(0, 0)
	if h == 0 {
		return nil, fmt.Errorf("proc: iş nesnesi oluşturulamadı: %w", err)
	}
	job := syscall.Handle(h)

	// Tutamak kapanınca üyeleri öldür. Asıl garanti bu.
	info := jobObjectExtendedLimitInfo{}
	info.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnJobClose
	ret, _, err := procSetInformationJobObj.Call(
		uintptr(job),
		uintptr(jobObjectExtendedLimitInformation),
		uintptr(unsafe.Pointer(&info)),
		unsafe.Sizeof(info),
	)
	if ret == 0 {
		syscall.CloseHandle(job)
		return nil, fmt.Errorf("proc: iş nesnesi sınırları ayarlanamadı: %w", err)
	}
	return &groupImpl{job: job}, nil
}

func (g *groupImpl) prepare(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// Konsol penceresi açılmasın: DevBox arka planda onlarca süreç başlatır.
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
}

func (g *groupImpl) add(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return fmt.Errorf("proc: süreç henüz başlatılmamış")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return fmt.Errorf("proc: grup kapatılmış")
	}

	h, err := syscall.OpenProcess(processSetQuota|processTerminate, false, uint32(cmd.Process.Pid))
	if err != nil {
		return fmt.Errorf("proc: süreç tutamağı açılamadı (pid %d): %w", cmd.Process.Pid, err)
	}
	defer syscall.CloseHandle(h)

	ret, _, err := procAssignProcessToJobObj.Call(uintptr(g.job), uintptr(h))
	if ret == 0 {
		return fmt.Errorf("proc: süreç iş nesnesine atanamadı (pid %d): %w", cmd.Process.Pid, err)
	}
	return nil
}

func (g *groupImpl) close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return nil
	}
	g.closed = true

	// TerminateJobObject'i açıkça çağırıyoruz: tutamağı kapatmak da öldürür
	// ama bu yol eşzamanlı ve dönüş değeri denetlenebilir.
	procTerminateJobObject.Call(uintptr(g.job), 1)
	return syscall.CloseHandle(g.job)
}

// terminateTree, Windows'ta desteklenmiyor.
//
// Süreç grubuna taşınabilir bir "nazikçe dur" sinyali yok:
// GenerateConsoleCtrlEvent bir konsol oturumu gerektiriyor ve DevBox'ın
// başlattığı süreçler HideWindow ile konsolsuz çalışıyor. Çağıran bu
// hatayı görüp doğrudan killTree'ye geçiyor.
func terminateTree(cmd *exec.Cmd) error {
	return fmt.Errorf("proc: Windows'ta süreç grubuna sinyal gönderilemiyor")
}

// killTree, süreci ve tüm alt süreçlerini öldürür.
//
// taskkill /T, süreç ağacını üst-süreç kimliğinden yürüyerek kapatıyor;
// Windows'ta halihazırda çalışan torunları kapsayan taşınabilir tek yol
// bu. (Yeni bir iş nesnesi kurmak yalnız atamadan sonra doğan süreçleri
// kapsardı — yani asıl sorunu, zaten doğmuş torunları çözmezdi.)
// taskkill.exe her Windows kurulumunda var; yine de bulunamazsa sürecin
// kendisi öldürülüyor.
func killTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	kill := exec.Command("taskkill.exe", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := kill.Run(); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}
