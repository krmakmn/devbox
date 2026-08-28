//go:build windows

package elevate

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

var (
	shell32              = syscall.NewLazyDLL("shell32.dll")
	procShellExecuteExW  = shell32.NewProc("ShellExecuteExW")
	kernel32             = syscall.NewLazyDLL("kernel32.dll")
	procWaitForSingleObj = kernel32.NewProc("WaitForSingleObject")
)

const (
	// TokenElevation bilgi sınıfı (winnt.h).
	tokenElevation = 20

	seeMaskNoCloseProcess = 0x00000040
	seeMaskNoAsync        = 0x00000100
	swHide                = 0
	infinite              = 0xFFFFFFFF

	errorCancelled = 1223 // ERROR_CANCELLED: kullanıcı UAC'yi reddetti
)

// shellExecuteInfo, SHELLEXECUTEINFOW yapısı.
type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         uintptr
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     uintptr
	lpIDList     uintptr
	lpClass      *uint16
	hkeyClass    uintptr
	dwHotKey     uint32
	hIcon        uintptr
	hProcess     uintptr
}

// IsElevated, sürecin yönetici hakkıyla çalışıp çalışmadığını söyler.
func IsElevated() bool {
	token, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		return false
	}
	defer token.Close()

	var elevated uint32
	var size uint32
	err = syscall.GetTokenInformation(
		token, tokenElevation,
		(*byte)(unsafe.Pointer(&elevated)),
		uint32(unsafe.Sizeof(elevated)), &size,
	)
	return err == nil && elevated != 0
}

// Relaunch, DevBox'ı verilen argümanlarla yönetici olarak yeniden başlatır
// ve bitmesini bekler.
//
// Kullanıcı UAC penceresini reddederse ErrDeclined döner. Yükseltilmiş
// sürecin çıkış kodu sıfır değilse hata döner; böylece çağıran işlemin
// gerçekten yapıldığını bilir.
func Relaunch(args []string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("elevate: çalıştırılabilir bulunamadı: %w", err)
	}
	verb, err := syscall.UTF16PtrFromString("runas")
	if err != nil {
		return err
	}
	file, err := syscall.UTF16PtrFromString(exe)
	if err != nil {
		return err
	}
	params, err := syscall.UTF16PtrFromString(buildCommandLine(args))
	if err != nil {
		return err
	}
	cwd, _ := os.Getwd()
	dir, err := syscall.UTF16PtrFromString(cwd)
	if err != nil {
		return err
	}

	info := shellExecuteInfo{
		fMask:        seeMaskNoCloseProcess | seeMaskNoAsync,
		lpVerb:       verb,
		lpFile:       file,
		lpParameters: params,
		lpDirectory:  dir,
		// Pencere gizli: yükseltilmiş süreç çıktısını göstermiyoruz,
		// yalnız çıkış kodunu okuyoruz.
		nShow: swHide,
	}
	info.cbSize = uint32(unsafe.Sizeof(info))

	ret, _, callErr := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info)))
	if ret == 0 {
		if errno, ok := callErr.(syscall.Errno); ok && errno == errorCancelled {
			return ErrDeclined
		}
		return fmt.Errorf("elevate: yükseltilmiş süreç başlatılamadı: %w", callErr)
	}
	if info.hProcess == 0 {
		return nil
	}
	handle := syscall.Handle(info.hProcess)
	defer syscall.CloseHandle(handle)

	procWaitForSingleObj.Call(uintptr(handle), infinite)

	var code uint32
	if err := syscall.GetExitCodeProcess(handle, &code); err != nil {
		return fmt.Errorf("elevate: çıkış kodu okunamadı: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("elevate: yükseltilmiş işlem %d koduyla başarısız oldu", code)
	}
	return nil
}
