package projects

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/krmakmn/devbox/internal/supervisor"
)

// proje, verilen dizinde bir devbox.yaml oluşturur.
func proje(t *testing.T, dir, name, domain string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := "name: " + name + "\ndomain: " + domain + "\nserver: proxy\nproxy: http://127.0.0.1:1\n"
	if err := os.WriteFile(filepath.Join(dir, "devbox.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func kayit(t *testing.T) *Registry {
	t.Helper()
	r, err := Open(filepath.Join(t.TempDir(), "projeler.json"))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestAddListRemove(t *testing.T) {
	r := kayit(t)
	root := t.TempDir()
	dir := proje(t, filepath.Join(root, "magaza"), "magaza", "magaza.test")

	p, err := r.Add(dir)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "magaza" || p.Domain != "magaza.test" {
		t.Errorf("eklenen proje = %+v", p)
	}

	list, err := r.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("%d proje listelendi", len(list))
	}

	if err := r.Remove("magaza"); err != nil {
		t.Fatal(err)
	}
	if list, _ := r.List(); len(list) != 0 {
		t.Errorf("silinen proje listede kaldı: %+v", list)
	}
	if err := r.Remove("magaza"); err == nil {
		t.Error("olmayan proje silinebildi")
	}
}

// Aynı dizini iki kez eklemek hata değil: kullanıcı emin olmak için
// tekrar yazmış olabilir.
func TestAddIsIdempotent(t *testing.T) {
	r := kayit(t)
	dir := proje(t, filepath.Join(t.TempDir(), "magaza"), "magaza", "magaza.test")

	if _, err := r.Add(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Add(dir); err != nil {
		t.Fatalf("ikinci ekleme hata verdi: %v", err)
	}
	if list, _ := r.List(); len(list) != 1 {
		t.Errorf("%d kayıt oluştu, 1 bekleniyordu", len(list))
	}
}

// İki farklı dizin aynı adı taşırsa arayüzde ve servis adlarında birbirine
// karışırlar; ekleme reddedilmeli.
func TestAddRejectsDuplicateName(t *testing.T) {
	r := kayit(t)
	root := t.TempDir()
	a := proje(t, filepath.Join(root, "bir"), "magaza", "magaza.test")
	b := proje(t, filepath.Join(root, "iki"), "magaza", "baska.test")

	if _, err := r.Add(a); err != nil {
		t.Fatal(err)
	}
	_, err := r.Add(b)
	if err == nil {
		t.Fatal("aynı adlı ikinci proje kabul edildi")
	}
	if !strings.Contains(err.Error(), "name") {
		t.Errorf("hata çözümü söylemiyor: %v", err)
	}
}

func TestAddRejectsDirectoryWithoutConfig(t *testing.T) {
	r := kayit(t)
	if _, err := r.Add(t.TempDir()); err == nil {
		t.Error("devbox.yaml'ı olmayan dizin kabul edildi")
	}
}

// devbox.yaml gerçeğin kaynağı: alan adı değişince liste de değişmeli.
func TestListReadsConfigFreshly(t *testing.T) {
	r := kayit(t)
	dir := proje(t, filepath.Join(t.TempDir(), "magaza"), "magaza", "magaza.test")
	if _, err := r.Add(dir); err != nil {
		t.Fatal(err)
	}

	proje(t, dir, "magaza", "yeni.test")
	list, _ := r.List()
	if list[0].Domain != "yeni.test" {
		t.Errorf("alan adı = %q; kayıt eski değeri gösteriyor", list[0].Domain)
	}
}

// Dizin silinirse girdi kendiliğinden yok olmamalı: kullanıcı diski
// takmayı unutmuş olabilir. Sorunu göstermek, sessizce silmekten iyi.
func TestMissingDirectoryIsReportedNotRemoved(t *testing.T) {
	r := kayit(t)
	root := t.TempDir()
	dir := proje(t, filepath.Join(root, "magaza"), "magaza", "magaza.test")
	if _, err := r.Add(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}

	list, err := r.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("kayıt kendiliğinden silindi")
	}
	if !list[0].Missing {
		t.Error("eksik dizin işaretlenmemiş")
	}
	if list[0].Name != "magaza" {
		t.Errorf("eksik projenin adı kaybolmuş: %q", list[0].Name)
	}
}

// Kayıt dosyası bozuksa açık bir hata verilmeli; sessizce boş liste
// dönmek, kullanıcının projelerinin kaybolduğunu düşünmesine yol açar.
func TestCorruptRegistryFailsLoudly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "projeler.json")
	if err := os.WriteFile(path, []byte("{bozuk"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.List(); err == nil {
		t.Error("bozuk kayıt sessizce yutuldu")
	}
}

// Koşucu, projeyi "devbox up" alt süreci olarak çalıştırıyor ve hazır
// sayması için çıktıdaki sözleşme satırını görmesi gerekiyor.
func TestRunnerStartsAndStopsProject(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sahte çalıştırılabilir kabuk betiği")
	}
	root := t.TempDir()
	dir := proje(t, filepath.Join(root, "magaza"), "magaza", "magaza.test")

	// "devbox up" yerine sözleşmeye uyan sahte bir çalıştırılabilir.
	fake := filepath.Join(root, "sahte-devbox")
	script := "#!/bin/sh\necho \"  magaza" + ReadyLine + "magaza.test\"\nsleep 60\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	r := kayit(t)
	if _, err := r.Add(dir); err != nil {
		t.Fatal(err)
	}
	sup, err := supervisor.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer sup.Close()

	runner := &Runner{Registry: r, Supervisor: sup, Executable: fake}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	st, err := runner.Start(ctx, "magaza")
	if err != nil {
		t.Fatalf("başlatılamadı: %v", err)
	}
	if !st.Running || st.PID == 0 {
		t.Errorf("proje çalışır görünmüyor: %+v", st)
	}
	if st.ServiceName != "proje-magaza" {
		t.Errorf("servis adı = %q", st.ServiceName)
	}

	logs, ok := runner.Logs("magaza")
	if !ok || !strings.Contains(string(logs.Bytes()), "magaza.test") {
		t.Error("projenin günlüğüne ulaşılamıyor")
	}

	if _, err := runner.Stop("magaza"); err != nil {
		t.Fatal(err)
	}
	st, _ = runner.Status("magaza")
	if st.Running {
		t.Error("durdurulan proje hâlâ çalışıyor görünüyor")
	}
}

func TestRunnerRefusesUnknownProject(t *testing.T) {
	sup, err := supervisor.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer sup.Close()

	runner := &Runner{Registry: kayit(t), Supervisor: sup, Executable: "/bin/true"}
	if _, err := runner.Start(context.Background(), "yok"); err == nil {
		t.Error("kayıtlı olmayan proje başlatıldı")
	}
}
