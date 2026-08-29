package scaffold

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTemplatesAreListedAndResolved(t *testing.T) {
	list := Templates()
	if len(list) == 0 {
		t.Fatal("şablon yok")
	}
	// Sıralı olmalı: liste her çalıştırmada aynı görünsün.
	for i := 1; i < len(list); i++ {
		if list[i-1].Name > list[i].Name {
			t.Errorf("şablonlar sıralı değil: %s, %s", list[i-1].Name, list[i].Name)
		}
	}

	if _, err := Get("LARAVEL"); err != nil {
		t.Errorf("büyük harfli ad çözülemedi: %v", err)
	}
	err := func() error { _, err := Get("django"); return err }()
	if err == nil {
		t.Fatal("bilinmeyen şablon kabul edildi")
	}
	// Hata, seçenekleri saymalı: kullanıcı ne yazacağını bilsin.
	if !strings.Contains(err.Error(), "laravel") {
		t.Errorf("hata seçenekleri saymıyor: %v", err)
	}
}

func TestToolFreeTemplatesWriteFiles(t *testing.T) {
	for _, name := range []string{"php", "static"} {
		tmpl, err := Get(name)
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(t.TempDir(), name)
		if err := tmpl.Create(context.Background(), Options{Dir: dir}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) == 0 {
			t.Errorf("%s şablonu dosya üretmedi", name)
		}
	}

	// PHP şablonu belge kökünü public/ altına koyuyor; algılama buna
	// bakıp root alanını dolduruyor.
	dir := t.TempDir()
	tmpl, _ := Get("php")
	if err := tmpl.Create(context.Background(), Options{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "public", "index.php")); err != nil {
		t.Errorf("public/index.php yok: %v", err)
	}
}

// Var olan bir projenin üstüne yazmak geri alınamaz.
func TestCreateRefusesNonEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "onemli.txt"), []byte("veri"), 0o644); err != nil {
		t.Fatal(err)
	}
	tmpl, _ := Get("static")
	if err := tmpl.Create(context.Background(), Options{Dir: dir}); err == nil {
		t.Fatal("dolu dizinin üstüne yazıldı")
	}
	if _, err := os.Stat(filepath.Join(dir, "onemli.txt")); err != nil {
		t.Error("var olan dosya silinmiş")
	}
}

// Araç yoksa hangi aracın eksik olduğu ve nasıl kurulacağı söylenmeli;
// kullanıcı boş bir klasörle kalıp hatayı DevBox'ta aramasın.
func TestMissingToolExplainsItself(t *testing.T) {
	tmpl, err := Get("wordpress")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath(tmpl.Tool); err == nil {
		t.Skip("wp-cli kurulu; bu test onun yokluğuna dayanıyor")
	}
	err = tmpl.Create(context.Background(), Options{Dir: filepath.Join(t.TempDir(), "site")})
	if err == nil {
		t.Fatal("eksik araçla iskelet kuruldu")
	}
	if !strings.Contains(err.Error(), tmpl.Tool) || !strings.Contains(err.Error(), "Kurulum:") {
		t.Errorf("hata eksik aracı ve kurulumu söylemiyor: %v", err)
	}
}

// Gerçek kurucuyla çıkan hata: create-vite verilen yolu çalışma dizinine
// ekliyor, yani mutlak yol verilince proje /tmp/x/tmp/x/proje gibi bir
// yere kuruluyordu. Komut, hedefin üst dizininde çalışıp yalnız adı
// almalı.
func TestScaffolderRunsInParentWithBareName(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sahte araç kabuk betiği")
	}
	binDir := t.TempDir()
	kayit := filepath.Join(binDir, "kayit.txt")

	// composer yerine, çağrıldığı dizini ve argümanlarını yazan sahte araç.
	sahte := "#!/bin/sh\n{ pwd; echo \"$@\"; } > " + kayit + "\nmkdir -p \"$3\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "composer"), []byte(sahte), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	root := t.TempDir()
	hedef := filepath.Join(root, "magaza")

	tmpl, _ := Get("laravel")
	if err := tmpl.Create(context.Background(), Options{Dir: hedef}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	data, err := os.ReadFile(kayit)
	if err != nil {
		t.Fatal(err)
	}
	satirlar := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(satirlar) != 2 {
		t.Fatalf("sahte araç beklenen çıktıyı yazmadı: %q", data)
	}
	calismaDizini, argumanlar := satirlar[0], satirlar[1]

	if !sameRealPath(t, calismaDizini, root) {
		t.Errorf("kurucu %q dizininde çalıştı, %q bekleniyordu", calismaDizini, root)
	}
	if !strings.HasSuffix(argumanlar, " magaza") {
		t.Errorf("kurucuya verilen argümanlar = %q; çıplak ad bekleniyordu", argumanlar)
	}
	if strings.Contains(argumanlar, root) {
		t.Errorf("kurucuya mutlak yol verilmiş: %q", argumanlar)
	}
}

// sameRealPath, sembolik bağları çözerek karşılaştırır (macOS'ta
// t.TempDir /var yerine /private/var döner).
func sameRealPath(t *testing.T, a, b string) bool {
	t.Helper()
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		ra = a
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		rb = b
	}
	return ra == rb
}
