package trust

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/krmakmn/devbox/internal/certs"
)

func newRoot(t *testing.T) (rootPEMPath string) {
	t.Helper()
	store, err := certs.Open(t.TempDir())
	if err != nil {
		t.Fatalf("certs.Open: %v", err)
	}
	return store.RootCertPath()
}

func TestLoadCertRejectsGarbage(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bozuk.pem")
	if err := os.WriteFile(bad, []byte("bu bir sertifika değil"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCert(bad); err == nil {
		t.Error("bozuk PEM kabul edildi")
	}
	if _, err := loadCert(filepath.Join(dir, "yok.pem")); err == nil {
		t.Error("olmayan dosya kabul edildi")
	}
}

func TestFirefoxProfileDiscovery(t *testing.T) {
	root := t.TempDir()

	// Gerçek bir profil: NSS veritabanı var.
	profile := filepath.Join(root, "a1b2c3.default-release")
	mkdir(t, profile)
	touch(t, filepath.Join(profile, "cert9.db"))

	// Eski biçim: yalnız key4.db.
	legacy := filepath.Join(root, "eski.default")
	mkdir(t, legacy)
	touch(t, filepath.Join(legacy, "key4.db"))

	// Profil değil: veritabanı yok.
	mkdir(t, filepath.Join(root, "Crash Reports"))
	// Dosya, dizin değil.
	touch(t, filepath.Join(root, "profiles.ini"))

	got := firefoxProfiles([]string{root, filepath.Join(root, "yok")})
	if len(got) != 2 {
		t.Fatalf("%d profil bulundu, beklenen 2: %v", len(got), got)
	}
	found := map[string]bool{}
	for _, p := range got {
		found[filepath.Base(p)] = true
	}
	if !found["a1b2c3.default-release"] || !found["eski.default"] {
		t.Errorf("beklenen profiller bulunamadı: %v", got)
	}
}

func TestFirefoxProfilesDeduplicates(t *testing.T) {
	root := t.TempDir()
	profile := filepath.Join(root, "tek.default")
	mkdir(t, profile)
	touch(t, filepath.Join(profile, "cert9.db"))

	// Aynı kök iki kez verilirse profil bir kez dönmeli; yoksa certutil
	// aynı profile iki kez çalıştırılır.
	got := firefoxProfiles([]string{root, root})
	if len(got) != 1 {
		t.Errorf("%d profil döndü, beklenen 1: %v", len(got), got)
	}
}

func TestCertutilArgs(t *testing.T) {
	args := certutilArgs(`C:\Users\k\AppData\Roaming\Mozilla\Firefox\Profiles\x.default`, `C:\devbox\root.crt`)
	joined := strings.Join(args, " ")

	if !strings.Contains(joined, "sql:C:\\Users") {
		t.Errorf("profil yolu sql: önekiyle verilmemiş: %v", args)
	}
	// Güven bayrağı yalnız TLS sunucu kimliği olmalı; e-posta ve kod
	// imzalama yetkisi vermek yerel bir CA için gereğinden fazla.
	for i, a := range args {
		if a == "-t" {
			if args[i+1] != "C,," {
				t.Errorf("güven bayrağı %q, beklenen \"C,,\"", args[i+1])
			}
		}
	}
	if args[0] != "-A" {
		t.Errorf("ilk argüman %q, beklenen -A", args[0])
	}
}

func TestIsWindowsCertutil(t *testing.T) {
	cases := map[string]bool{
		`C:\Windows\System32\certutil.exe`:    true,
		`C:\Windows\SysWOW64\certutil.exe`:    true,
		`C:\Users\k\scoop\shims\certutil.exe`: false,
		`/usr/bin/certutil`:                   false,
	}
	for path, want := range cases {
		if got := isWindowsCertutil(path); got != want {
			t.Errorf("isWindowsCertutil(%q) = %v, beklenen %v", path, got, want)
		}
	}
}

func TestInstallReportsEveryTarget(t *testing.T) {
	rootPEM := newRoot(t)

	results, err := Install(rootPEM)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("hiç sonuç dönmedi; kullanıcı neyin olduğunu göremez")
	}

	// Başarısız her hedef ne yapılacağını söylemeli. Sessiz başarısızlık,
	// kullanıcının saatlerce yanlış yerde hata aramasına yol açar.
	for _, r := range results {
		if r.Err != nil && r.Hint == "" {
			t.Errorf("%q başarısız oldu ama ipucu yok", r.Target)
		}
		if r.String() == "" {
			t.Errorf("%q için boş özet", r.Target)
		}
	}
}

// Gerçek güven deposuna yazan test. Geliştirme makinesinde kendiliğinden
// çalışmasını istemiyoruz; CI'da Windows işinde açık.
func TestSystemStoreRoundTrip(t *testing.T) {
	if os.Getenv("DEVBOX_TEST_TRUST_STORE") != "1" {
		t.Skip("DEVBOX_TEST_TRUST_STORE=1 değil; gerçek güven deposuna dokunulmuyor")
	}
	if runtime.GOOS != "windows" {
		t.Skip("işletim sistemi güven deposu yalnız Windows'ta destekleniyor")
	}

	rootPEM := newRoot(t)
	t.Cleanup(func() { Uninstall(rootPEM) })

	if installed, err := IsInstalled(rootPEM); err != nil {
		t.Fatalf("IsInstalled: %v", err)
	} else if installed {
		t.Fatal("yeni üretilen kök depoda görünüyor")
	}

	cert, err := loadCert(rootPEM)
	if err != nil {
		t.Fatal(err)
	}
	if res := installSystem(cert); !res.Installed {
		t.Fatalf("kurulum başarısız: %v", res.Err)
	}

	if installed, err := IsInstalled(rootPEM); err != nil {
		t.Fatalf("IsInstalled: %v", err)
	} else if !installed {
		t.Error("kurulumdan sonra kök depoda bulunamadı")
	}

	if err := Uninstall(rootPEM); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if installed, err := IsInstalled(rootPEM); err != nil {
		t.Fatalf("IsInstalled: %v", err)
	} else if installed {
		t.Error("kaldırmadan sonra kök hâlâ depoda")
	}
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}
