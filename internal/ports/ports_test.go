package ports

import (
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Gerçek netsh çıktısı. Windows sürümleri arasında başlık metni ve
// boşluklar değişiyor, sayı çiftleri değişmiyor.
const netshOutput = `
Protocol tcp Port Exclusion Ranges

Start Port    End Port
----------    --------
      1024        1123
      1124        1223
      1352        1451
      5357        5357
     50000       50059     *

* - Administered port exclusions.
`

func TestParseExcludedRanges(t *testing.T) {
	got := ParseExcludedRanges(netshOutput)
	want := []Range{
		{1024, 1123}, {1124, 1223}, {1352, 1451}, {5357, 5357}, {50000, 50059},
	}
	if len(got) != len(want) {
		t.Fatalf("%d aralık çözüldü, beklenen %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d. aralık = %v, beklenen %v", i, got[i], want[i])
		}
	}
}

func TestParseExcludedRangesIgnoresNoise(t *testing.T) {
	// Başlık satırları, ayraçlar ve boş satırlar aralık sanılmamalı.
	noisy := `
Protocol tcp Port Exclusion Ranges
Start Port    End Port
----------    --------
      hatalı  satır
      70000      70100
        500        400
       8000        8099
`
	got := ParseExcludedRanges(noisy)
	if len(got) != 1 || got[0] != (Range{8000, 8099}) {
		t.Errorf("gürültülü çıktıdan %v çözüldü, beklenen yalnız 8000-8099", got)
	}
}

func TestParseEmptyOutput(t *testing.T) {
	if got := ParseExcludedRanges(""); len(got) != 0 {
		t.Errorf("boş çıktıdan %v çözüldü", got)
	}
}

func TestRangeContains(t *testing.T) {
	r := Range{1024, 1123}
	for _, p := range []int{1024, 1050, 1123} {
		if !r.Contains(p) {
			t.Errorf("%d aralıkta sayılmadı", p)
		}
	}
	for _, p := range []int{1023, 1124, 0} {
		if r.Contains(p) {
			t.Errorf("%d yanlışlıkla aralıkta sayıldı", p)
		}
	}
}

func TestAllocatePrefersRequestedPort(t *testing.T) {
	a := New("127.0.0.1")
	free := freePort(t)

	got, err := a.Allocate(free)
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if got != free {
		t.Errorf("port %d, beklenen %d", got, free)
	}
	if taken := a.Taken(); len(taken) != 1 || taken[0] != free {
		t.Errorf("tahsis listesi = %v", taken)
	}
}

// Aynı portu iki kez vermek, iki sürecin aynı adrese bağlanmaya
// çalışmasıdır; ikincisi açılışta ölür.
func TestAllocateSkipsAlreadyTakenPort(t *testing.T) {
	a := New("127.0.0.1")
	first := freePort(t)

	if _, err := a.Allocate(first); err != nil {
		t.Fatal(err)
	}
	second, err := a.Allocate(first)
	if err != nil {
		t.Fatalf("ikinci tahsis: %v", err)
	}
	if second == first {
		t.Error("aynı port iki kez tahsis edildi")
	}
}

func TestAllocateSkipsPortInUse(t *testing.T) {
	// Gerçekten dinlenen bir port atlanmalı.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	busy := ln.Addr().(*net.TCPAddr).Port

	a := New("127.0.0.1")
	got, err := a.Allocate(busy)
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if got == busy {
		t.Error("dinlenen port tahsis edildi")
	}
}

// Hyper-V rezervasyonlarında bind "erişim engellendi" ile döner ve hiçbir
// süreç portu tutuyor görünmez; kullanıcı için tamamen anlaşılmaz. Listeyi
// önceden elemek bunu kesiyor.
func TestAllocateSkipsExcludedRanges(t *testing.T) {
	a := New("127.0.0.1")
	base := freePort(t)
	a.SetExcluded([]Range{{Start: base, End: base + 20}})

	got, err := a.Allocate(base)
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if got <= base+20 {
		t.Errorf("rezerve aralıktan port verildi: %d (aralık %d-%d)", got, base, base+20)
	}
}

func TestExcludedRangeExhaustionExplainsWhy(t *testing.T) {
	a := New("127.0.0.1")
	// Tarama penceresinin tamamını rezerve et.
	a.SetExcluded([]Range{{Start: 20000, End: 20000 + 300}})

	_, err := a.Allocate(20000)
	if err == nil {
		t.Fatal("tamamen rezerve aralıkta port bulundu")
	}
	msg := err.Error()
	// Kullanıcı ne olduğunu ve ne yapacağını görmeli.
	for _, want := range []string{"rezerve", "Hyper-V", "netsh"} {
		if !strings.Contains(msg, want) {
			t.Errorf("hata mesajında %q yok:\n%s", want, msg)
		}
	}
}

func TestDiagnosisMentionsIISForWebPorts(t *testing.T) {
	a := New("127.0.0.1")
	msg := a.diagnosis(80)
	if !strings.Contains(msg, "IIS") {
		t.Errorf("80 için tanı IIS'ten söz etmiyor:\n%s", msg)
	}
	if strings.Contains(a.diagnosis(9000), "IIS") {
		t.Error("ilgisiz port için IIS önerisi verildi")
	}
}

func TestAllocateSeries(t *testing.T) {
	a := New("127.0.0.1")
	base := freePort(t)

	got, err := a.AllocateSeries(base, 4)
	if err != nil {
		t.Fatalf("AllocateSeries: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("%d port döndü, beklenen 4", len(got))
	}
	seen := map[int]bool{}
	for _, p := range got {
		if seen[p] {
			t.Errorf("aynı port iki kez: %d", p)
		}
		seen[p] = true
	}
}

func TestAllocateEphemeral(t *testing.T) {
	a := New("127.0.0.1")
	got, err := a.Allocate(0)
	if err != nil {
		t.Fatalf("Allocate(0): %v", err)
	}
	if got == 0 {
		t.Error("geçici port 0 döndü")
	}
}

func TestReleaseMakesPortAvailableAgain(t *testing.T) {
	a := New("127.0.0.1")
	port := freePort(t)

	if _, err := a.Allocate(port); err != nil {
		t.Fatal(err)
	}
	a.Release(port)
	if len(a.Taken()) != 0 {
		t.Errorf("bırakılan port listede kaldı: %v", a.Taken())
	}
	again, err := a.Allocate(port)
	if err != nil {
		t.Fatal(err)
	}
	if again != port {
		t.Errorf("bırakılan port yeniden verilmedi: %d", again)
	}
}

func TestConcurrentAllocationGivesDistinctPorts(t *testing.T) {
	a := New("127.0.0.1")

	var mu sync.Mutex
	seen := map[int]bool{}
	var wg sync.WaitGroup

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			port, err := a.Allocate(0)
			if err != nil {
				t.Errorf("Allocate: %v", err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if seen[port] {
				t.Errorf("aynı port iki kez verildi: %d", port)
			}
			seen[port] = true
		}()
	}
	wg.Wait()
}

// freePort, o an boş olan bir port numarası döner.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port, err := strconv.Atoi(strings.Split(ln.Addr().String(), ":")[1])
	if err != nil {
		t.Fatal(err)
	}
	return port
}
