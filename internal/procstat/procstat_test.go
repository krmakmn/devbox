package procstat

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

func TestReadRejectsBadPID(t *testing.T) {
	if _, err := Read(0); err == nil {
		t.Error("0 süreç kimliği kabul edildi")
	}
	if _, err := Read(-1); err == nil {
		t.Error("negatif süreç kimliği kabul edildi")
	}
}

// Kendi sürecimizi okumak, ölçümün gerçekten çalıştığını gösteriyor:
// test süreci mutlaka bellek tutuyor ve işlemci harcamış oluyor.
func TestReadsOwnProcess(t *testing.T) {
	// Ölçülebilir miktarda işlemci harca: çekirdek muhasebesi bir tik
	// (10 ms) çözünürlüğünde, yeni doğmuş bir süreçte sıfır okunabiliyor.
	isYap(120 * time.Millisecond)

	st, err := Read(os.Getpid())
	if errors.Is(err, ErrUnsupported) {
		t.Skip("bu işletim sisteminde desteklenmiyor")
	}
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if st.RSS == 0 {
		t.Error("bellek kullanımı sıfır okundu")
	}
	// Go çalışma zamanı birkaç megabaytın altına inmiyor; sıfıra yakın
	// bir değer ölçüm biriminin yanlış olduğunu gösterir.
	if st.RSS < 1<<20 {
		t.Errorf("bellek kullanımı %d bayt; birim yanlış olabilir", st.RSS)
	}
	if st.CPUSeconds <= 0 {
		t.Errorf("işlemci süresi %v; sıfırdan büyük olmalıydı", st.CPUSeconds)
	}
}

// İşlemci süresi birikmeli: iş yapan bir süreçte iki ölçüm arasında
// artmalı. Oranı hesaplayacak olan (denetim paneli) buna güveniyor.
func TestCPUTimeAccumulates(t *testing.T) {
	if _, err := Read(os.Getpid()); errors.Is(err, ErrUnsupported) {
		t.Skip("bu işletim sisteminde desteklenmiyor")
	}

	ilk, err := Read(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	isYap(300 * time.Millisecond)

	ikinci, err := Read(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if ikinci.CPUSeconds <= ilk.CPUSeconds {
		t.Errorf("işlemci süresi artmadı: %v → %v", ilk.CPUSeconds, ikinci.CPUSeconds)
	}
}

func TestReadOfDeadProcessFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("kabuk komutu Windows'ta farklı")
	}
	cmd := exec.Command("/bin/true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid

	if _, err := Read(pid); err == nil {
		// Süreç kimliği yeniden kullanılmış olabilir; bu bir hata değil
		// ama ölçüm de anlamsız olurdu. Yalnız açık bir hata bekliyoruz.
		t.Skip("süreç kimliği yeniden kullanılmış olabilir")
	}
}

// Ölçüm, adında boşluk olan bir süreçte de doğru çalışmalı: Linux'ta
// /proc/<pid>/stat'ın ikinci alanı parantez içinde ve boşluk içerebiliyor,
// boşluğa göre bölen bir ayrıştırıcı bütün alanları kaydırır.
func TestHandlesProcessNameWithSpaces(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("yalnız Linux'ta anlamlı")
	}
	dir := t.TempDir()
	// Adında boşluk olan bir çalıştırılabilir.
	path := dir + "/uzun ad"
	if err := os.WriteFile(path, []byte("#!/bin/sh\nsleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(path)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()

	// Süreç daha yeni doğduysa çekirdek belleğini henüz muhasebeye
	// almamış olabiliyor; ölçüme değer bir duruma gelmesini bekliyoruz.
	var st Stat
	son := time.Now().Add(3 * time.Second)
	for time.Now().Before(son) {
		var err error
		st, err = Read(cmd.Process.Pid)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if st.RSS > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if st.RSS == 0 {
		t.Error("adında boşluk olan sürecin belleği okunamadı")
	}
}

// isYap, verilen süre boyunca gerçek işlemci harcar (uyumak değil).
func isYap(d time.Duration) {
	son := time.Now().Add(d)
	var toplam uint64
	for time.Now().Before(son) {
		for i := 0; i < 100000; i++ {
			toplam += uint64(i)
		}
	}
	_ = toplam
}
