package hostsfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeHosts(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

const userContent = "127.0.0.1 localhost\n" +
	"# kullanıcının kendi notu\n" +
	"10.0.0.5 sunucu.ic\n"

func TestApplyPreservesUserLines(t *testing.T) {
	path := writeHosts(t, userContent)

	err := Apply(path, []Entry{{IP: "127.0.0.1", Names: []string{"magaza.test", "www.magaza.test"}}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	got := read(t, path)
	for _, line := range []string{"127.0.0.1 localhost", "# kullanıcının kendi notu", "10.0.0.5 sunucu.ic"} {
		if !strings.Contains(got, line) {
			t.Errorf("kullanıcı satırı kayboldu: %q\n---\n%s", line, got)
		}
	}
	if !strings.Contains(got, "127.0.0.1 magaza.test www.magaza.test") {
		t.Errorf("girdi yazılmamış:\n%s", got)
	}
	if !strings.Contains(got, beginMarker) || !strings.Contains(got, endMarker) {
		t.Errorf("işaretçiler eksik:\n%s", got)
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	path := writeHosts(t, userContent)
	entries := []Entry{{IP: "127.0.0.1", Names: []string{"magaza.test"}}}

	for i := 0; i < 3; i++ {
		if err := Apply(path, entries); err != nil {
			t.Fatalf("%d. Apply: %v", i, err)
		}
	}

	got := read(t, path)
	if n := strings.Count(got, beginMarker); n != 1 {
		t.Errorf("%d blok var, beklenen 1 — her çalıştırmada blok birikiyor:\n%s", n, got)
	}
	if n := strings.Count(got, "magaza.test"); n != 1 {
		t.Errorf("girdi %d kez yazılmış, beklenen 1", n)
	}
}

func TestApplyReplacesPreviousBlock(t *testing.T) {
	path := writeHosts(t, userContent)
	if err := Apply(path, []Entry{{IP: "127.0.0.1", Names: []string{"eski.test"}}}); err != nil {
		t.Fatal(err)
	}
	if err := Apply(path, []Entry{{IP: "127.0.0.1", Names: []string{"yeni.test"}}}); err != nil {
		t.Fatal(err)
	}

	got := read(t, path)
	if strings.Contains(got, "eski.test") {
		t.Errorf("eski girdi kaldı:\n%s", got)
	}
	if !strings.Contains(got, "yeni.test") {
		t.Errorf("yeni girdi yazılmadı:\n%s", got)
	}
}

func TestRemoveLeavesFileClean(t *testing.T) {
	path := writeHosts(t, userContent)
	if err := Apply(path, []Entry{{IP: "127.0.0.1", Names: []string{"magaza.test"}}}); err != nil {
		t.Fatal(err)
	}
	if err := Remove(path); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	got := read(t, path)
	if strings.Contains(got, beginMarker) || strings.Contains(got, "magaza.test") {
		t.Errorf("blok kaldırılmadı:\n%s", got)
	}
	if got != userContent {
		t.Errorf("dosya özgün hâline dönmedi:\ngörülen: %q\nbeklenen: %q", got, userContent)
	}
}

func TestManagedReadsOnlyOurBlock(t *testing.T) {
	path := writeHosts(t, userContent)
	want := []Entry{
		{IP: "127.0.0.1", Names: []string{"a.test", "b.test"}},
		{IP: "::1", Names: []string{"a.test"}},
	}
	if err := Apply(path, want); err != nil {
		t.Fatal(err)
	}

	got, err := Managed(path)
	if err != nil {
		t.Fatalf("Managed: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("%d girdi okundu, beklenen 2: %+v", len(got), got)
	}
	if got[0].IP != "127.0.0.1" || len(got[0].Names) != 2 {
		t.Errorf("ilk girdi yanlış: %+v", got[0])
	}
	// Kullanıcının localhost satırı bizim değil; okunmamalı.
	for _, e := range got {
		for _, n := range e.Names {
			if n == "localhost" || n == "sunucu.ic" {
				t.Errorf("blok dışındaki satır okundu: %+v", e)
			}
		}
	}
}

func TestRecoversFromMissingEndMarker(t *testing.T) {
	// Kullanıcı bitiş işaretçisini elle silmiş olabilir. Bu durumda
	// dosyanın geri kalanını yok etmek kabul edilemez.
	content := userContent + beginMarker + "\n127.0.0.1 eski.test\n" +
		"# kullanıcının sonradan eklediği satır\n8.8.8.8 dns.ornek\n"
	path := writeHosts(t, content)

	if err := Apply(path, []Entry{{IP: "127.0.0.1", Names: []string{"yeni.test"}}}); err != nil {
		t.Fatal(err)
	}

	got := read(t, path)
	if !strings.Contains(got, "8.8.8.8 dns.ornek") {
		t.Errorf("bozuk blok sonrası kullanıcı satırı silindi:\n%s", got)
	}
	if !strings.Contains(got, "yeni.test") {
		t.Errorf("yeni girdi yazılmadı:\n%s", got)
	}
}

func TestPreservesCRLF(t *testing.T) {
	// Windows'un hosts dosyası CRLF'tir; LF yazmak bazı eski araçları
	// şaşırtıyor.
	path := writeHosts(t, "127.0.0.1 localhost\r\n")
	if err := Apply(path, []Entry{{IP: "127.0.0.1", Names: []string{"magaza.test"}}}); err != nil {
		t.Fatal(err)
	}

	got := read(t, path)
	if strings.Contains(strings.ReplaceAll(got, "\r\n", ""), "\n") {
		t.Errorf("CRLF dosyasına LF satır yazıldı:\n%q", got)
	}
}

func TestApplyToMissingFileCreatesIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	if err := Apply(path, []Entry{{IP: "127.0.0.1", Names: []string{"magaza.test"}}}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !strings.Contains(read(t, path), "magaza.test") {
		t.Error("dosya oluşturulmadı ya da boş")
	}
}

func TestPathIsPlatformCorrect(t *testing.T) {
	p := Path()
	if p == "" {
		t.Fatal("boş yol")
	}
	if !filepath.IsAbs(p) {
		t.Errorf("yol mutlak değil: %q", p)
	}
}
