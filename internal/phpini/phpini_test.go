package phpini

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderIncludesDefaults(t *testing.T) {
	got, err := Render(Config{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, want := range []string{
		"cgi.fix_pathinfo = 0",
		"display_errors = On",
		"memory_limit = 512M",
		"opcache.revalidate_freq = 0",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("üretilen ini %q içermiyor:\n%s", want, got)
		}
	}
}

// cgi.fix_pathinfo=1 iken php-cgi "/yuklemeler/kedi.jpg/x.php" gibi bir yolu
// geriye doğru yorumlayıp yüklenmiş bir resmi PHP olarak çalıştırabiliyor.
// Web sunucusu yapılandırmaları bunu ayrıca engelliyor; burada kapatmak
// üçüncü savunma hattı.
func TestFixPathinfoIsDisabledByDefault(t *testing.T) {
	if Defaults()["cgi.fix_pathinfo"] != "0" {
		t.Fatal("cgi.fix_pathinfo varsayılan olarak kapalı değil")
	}
	got, _ := Render(Config{})
	if !strings.Contains(got, "cgi.fix_pathinfo = 0") {
		t.Error("üretilen ini'de cgi.fix_pathinfo kapalı değil")
	}
}

// PHP yönergeleri dosya sırasına göre uygular: sonuncusu kazanır. DevBox
// bloğu temel dosyadan SONRA gelmeli, yoksa temel dosya bizim ayarlarımızı
// ezer.
func TestOverridesComeAfterBaseFile(t *testing.T) {
	base := filepath.Join(t.TempDir(), "php.ini-development")
	if err := os.WriteFile(base, []byte("memory_limit = 128M\ndisplay_errors = Off\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := Render(Config{BaseFile: base})
	if err != nil {
		t.Fatal(err)
	}
	baseIdx := strings.Index(got, "memory_limit = 128M")
	ourIdx := strings.Index(got, "memory_limit = 512M")
	if baseIdx < 0 {
		t.Fatal("temel dosya içeriği aktarılmamış")
	}
	if ourIdx < 0 {
		t.Fatal("DevBox bloğu yok")
	}
	if ourIdx < baseIdx {
		t.Error("DevBox bloğu temel dosyadan önce geliyor; ayarlarımız ezilir")
	}
}

func TestProjectSettingsOverrideDefaults(t *testing.T) {
	got, err := Render(Config{Settings: map[string]string{
		"memory_limit":  "1G",
		"date.timezone": "Europe/Istanbul",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "memory_limit = 1G") {
		t.Error("proje ayarı uygulanmadı")
	}
	if strings.Contains(got, "memory_limit = 512M") {
		t.Error("varsayılan da yazılmış; iki kez geçiyor")
	}
	if !strings.Contains(got, "date.timezone = Europe/Istanbul") {
		t.Error("saat dilimi ayarı yok")
	}
}

func TestExtensionsAndDir(t *testing.T) {
	got, err := Render(Config{
		ExtensionDir: `C:\php\ext`,
		Extensions:   []string{"gd", "intl", "pdo_mysql"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Ters bölü tırnak içinde kaçış olarak yorumlanabiliyor.
	if !strings.Contains(got, `extension_dir = "C:/php/ext"`) {
		t.Errorf("uzantı dizini eğik bölüye çevrilmemiş:\n%s", got)
	}
	for _, ext := range []string{"gd", "intl", "pdo_mysql"} {
		if !strings.Contains(got, "extension = "+ext) {
			t.Errorf("%s uzantısı yazılmamış", ext)
		}
	}
}

func TestXdebugBlock(t *testing.T) {
	got, err := Render(Config{Xdebug: &Xdebug{Extension: `C:\php\ext\php_xdebug.dll`}})
	if err != nil {
		t.Fatal(err)
	}
	// Xdebug extension= ile yüklenmez.
	if !strings.Contains(got, `zend_extension = "C:/php/ext/php_xdebug.dll"`) {
		t.Errorf("zend_extension satırı yok:\n%s", got)
	}
	// IDE kapalıyken her isteği yavaşlatmamak için varsayılan "trigger".
	if !strings.Contains(got, "xdebug.start_with_request = trigger") {
		t.Error("varsayılan tetikleme kipi trigger değil")
	}
	if !strings.Contains(got, "xdebug.client_port = 9003") {
		t.Error("Xdebug 3 varsayılan portu kullanılmıyor")
	}
	// Xdebug açıkken opcache kapatılmalı.
	if !strings.Contains(got, "opcache.enable = 0") {
		t.Error("Xdebug ile birlikte opcache kapatılmamış")
	}
}

func TestXdebugCustomSettings(t *testing.T) {
	got, err := Render(Config{Xdebug: &Xdebug{
		Mode: "develop,debug", ClientHost: "192.168.1.5", ClientPort: 9999,
		StartWithRequest: "yes",
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"xdebug.mode = develop,debug",
		"xdebug.client_host = 192.168.1.5",
		"xdebug.client_port = 9999",
		"xdebug.start_with_request = yes",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("%q yok", want)
		}
	}
}

// Değerler doğrudan dosyaya gittiği için satır sonu içeren bir değer,
// istemediğimiz yönergeler eklemek demek.
func TestRejectsInjectionAttempts(t *testing.T) {
	bad := []Config{
		{Settings: map[string]string{"memory_limit": "512M\ndisable_functions ="}},
		{Settings: map[string]string{"memory_limit": "512M\r\nopen_basedir ="}},
		{Settings: map[string]string{"kötü ad": "1"}},
		{Settings: map[string]string{"a=b": "1"}},
		{Extensions: []string{"gd\nextension=evil"}},
		{ExtensionDir: "C:/php/ext\"\nzend_extension=evil"},
		{Xdebug: &Xdebug{Mode: "debug\nauto_prepend_file=evil.php"}},
		{Xdebug: &Xdebug{Extension: "a\"\nb"}},
	}
	for i, cfg := range bad {
		if _, err := Render(cfg); err == nil {
			t.Errorf("%d. enjeksiyon denemesi kabul edildi: %+v", i, cfg)
		}
	}
}

func TestWriteProducesReadableFile(t *testing.T) {
	dir := t.TempDir()
	path, err := Write(dir, Config{Settings: map[string]string{"memory_limit": "333M"}})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "php.ini" {
		t.Errorf("dosya adı %q, beklenen php.ini", filepath.Base(path))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "memory_limit = 333M") {
		t.Error("yazılan dosya beklenen ayarı içermiyor")
	}
}

// Üretilen dosyanın "doğru görünmesi" yetmez; PHP'nin onu ayrıştırıp
// ayarları uygulaması gerekir. PHP kurulu değilse atlanıyor.
func TestGeneratedIniIsAcceptedByPHP(t *testing.T) {
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("php kurulu değil; doğrulama atlanıyor")
	}

	dir := t.TempDir()
	if _, err := Write(dir, Config{Settings: map[string]string{
		"memory_limit":        "333M",
		"upload_max_filesize": "77M",
	}}); err != nil {
		t.Fatal(err)
	}

	// max_execution_time burada sınanmıyor: CLI SAPI onu php.ini'den
	// bağımsız olarak 0'a sabitler (betiklerin süre sınırı olmasın diye).
	// Web SAPI'sinde ayar geçerli, ama bunu CLI ile doğrulayamayız.
	cmd := exec.Command(php, "-c", dir,
		"-r", `echo ini_get("memory_limit"), "|", ini_get("upload_max_filesize"), "|", ini_get("cgi.fix_pathinfo");`)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("php üretilen ini'yi kabul etmedi: %v\n%s", err, out)
	}

	got := strings.TrimSpace(string(out))
	// PHP başlangıç uyarısı verirse çıktıya karışır; ayarlar da uygulanmaz.
	if strings.Contains(strings.ToLower(got), "warning") || strings.Contains(got, "Fatal") {
		t.Fatalf("php başlangıçta uyardı:\n%s", got)
	}
	parts := strings.Split(got, "|")
	if len(parts) != 3 {
		t.Fatalf("beklenmedik çıktı: %q", got)
	}
	if parts[0] != "333M" {
		t.Errorf("memory_limit = %q, beklenen 333M", parts[0])
	}
	if parts[1] != "77M" {
		t.Errorf("upload_max_filesize = %q, beklenen 77M", parts[1])
	}
	if parts[2] != "0" && parts[2] != "" {
		t.Errorf("cgi.fix_pathinfo = %q, beklenen 0", parts[2])
	}
}
