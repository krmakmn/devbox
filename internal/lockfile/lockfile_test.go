package lockfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	lock := New("magaza", "windows/amd64")
	lock.Add(Entry{Kind: "php", Name: "php", Requested: "8.3", Resolved: "8.3.14", Source: `C:\php\php-cgi.exe`})
	lock.Add(Entry{Kind: "service", Name: "redis", Resolved: "7.2.4"})

	if err := lock.Save(dir); err != nil {
		t.Fatal(err)
	}
	geri, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if geri.Project != "magaza" || geri.Platform != "windows/amd64" {
		t.Errorf("kilit = %+v", geri)
	}
	if len(geri.Entries) != 2 {
		t.Fatalf("%d girdi", len(geri.Entries))
	}
}

// Kilit dosyası depoya giriyor: sıralama kararlı olmalı, yoksa her
// çalıştırma anlamsız bir fark üretir.
func TestOutputIsStable(t *testing.T) {
	dir := t.TempDir()
	ilkYazim := func(sira []Entry) string {
		lock := New("magaza", "linux/amd64")
		// Zaman alanını sabitle ki karşılaştırma yalnız sırayı ölçsün.
		lock.GeneratedAt = New("x", "y").GeneratedAt
		for _, e := range sira {
			lock.Add(e)
		}
		if err := lock.Save(dir); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(Path(dir))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}

	a := []Entry{
		{Kind: "service", Name: "redis", Resolved: "7"},
		{Kind: "php", Name: "php", Resolved: "8.3"},
		{Kind: "container", Name: "api", Resolved: "sha256:x"},
	}
	b := []Entry{a[2], a[0], a[1]}

	ilk := ilkYazim(a)
	ikinci := ilkYazim(b)
	if ilk != ikinci {
		t.Errorf("aynı içerik farklı sırayla farklı dosya üretti:\n%s\n---\n%s", ilk, ikinci)
	}
}

func TestCompareFindsAllKindsOfDifference(t *testing.T) {
	beklenen := New("magaza", "linux/amd64")
	beklenen.Add(Entry{Kind: "php", Name: "php", Resolved: "8.3.14"})
	beklenen.Add(Entry{Kind: "service", Name: "redis", Resolved: "7.2.4"})
	beklenen.Add(Entry{Kind: "service", Name: "meilisearch", Resolved: "1.5.0"})

	gercek := New("magaza", "linux/amd64")
	gercek.Add(Entry{Kind: "php", Name: "php", Resolved: "8.2.9"})       // farklı sürüm
	gercek.Add(Entry{Kind: "service", Name: "redis", Resolved: "7.2.4"}) // aynı
	gercek.Add(Entry{Kind: "container", Name: "api", Resolved: "sha256:x"})

	diffs := Compare(beklenen, gercek)
	if len(diffs) != 3 {
		t.Fatalf("%d fark bulundu, 3 bekleniyordu: %v", len(diffs), diffs)
	}

	metin := make([]string, 0, len(diffs))
	for _, d := range diffs {
		metin = append(metin, d.String())
	}
	birlesik := strings.Join(metin, "\n")

	// Sürüm farkı.
	if !strings.Contains(birlesik, "8.3.14") || !strings.Contains(birlesik, "8.2.9") {
		t.Errorf("sürüm farkı bildirilmedi:\n%s", birlesik)
	}
	// Kilitte var, makinede yok.
	if !strings.Contains(birlesik, "meilisearch") || !strings.Contains(birlesik, "makinede yok") {
		t.Errorf("eksik bileşen bildirilmedi:\n%s", birlesik)
	}
	// Makinede var, kilitte yok — bu da bir fark.
	if !strings.Contains(birlesik, "api") || !strings.Contains(birlesik, "kilitte yok") {
		t.Errorf("fazladan bileşen bildirilmedi:\n%s", birlesik)
	}
}

func TestCompareFindsNothingWhenIdentical(t *testing.T) {
	a := New("magaza", "linux/amd64")
	a.Add(Entry{Kind: "php", Name: "php", Resolved: "8.3.14"})
	b := New("magaza", "windows/amd64") // platform farkı fark sayılmıyor
	b.Add(Entry{Kind: "php", Name: "php", Resolved: "8.3.14"})

	if diffs := Compare(a, b); len(diffs) != 0 {
		t.Errorf("aynı sürümlerde fark bulundu: %v", diffs)
	}
}

// İleri bir sürümle yazılmış kilit dosyası sessizce yanlış okunmamalı.
func TestRejectsNewerFormat(t *testing.T) {
	dir := t.TempDir()
	data := `{"version": 99, "project": "magaza", "entries": []}`
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(dir)
	if err == nil {
		t.Fatal("ileri sürümlü kilit kabul edildi")
	}
	if !strings.Contains(err.Error(), "güncelleyin") {
		t.Errorf("hata çözümü söylemiyor: %v", err)
	}
}

func TestRejectsCorruptFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("{bozuk"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Error("bozuk kilit sessizce yutuldu")
	}
}
