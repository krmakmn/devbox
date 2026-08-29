package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// --- yapılandırma ---------------------------------------------------------

func TestParseFullConfig(t *testing.T) {
	data := []byte(`
name: magaza
domain: magaza.test
aliases:
  - www.magaza.test
server: nginx
root: public
php:
  version: "8.3"
  workers: 4
  ini:
    memory_limit: 1G
  extensions: [intl, redis]
  xdebug: true
services:
  - postgres@17
  - redis
env:
  DB_HOST: 127.0.0.1
processes:
  queue: php artisan queue:work
cron:
  - schedule: "* * * * *"
    run: php artisan schedule:run
`)
	cfg, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if cfg.Name != "magaza" || cfg.Domain != "magaza.test" || cfg.Server != "nginx" {
		t.Errorf("temel alanlar yanlış: %+v", cfg)
	}
	if len(cfg.Aliases) != 1 || cfg.Aliases[0] != "www.magaza.test" {
		t.Errorf("aliases = %v", cfg.Aliases)
	}
	if cfg.PHP.Version != "8.3" || cfg.PHP.Workers != 4 || !cfg.PHP.Xdebug {
		t.Errorf("php ayarları yanlış: %+v", cfg.PHP)
	}
	if cfg.PHP.Ini["memory_limit"] != "1G" {
		t.Errorf("php.ini ayarı = %v", cfg.PHP.Ini)
	}
	if len(cfg.PHP.Extensions) != 2 {
		t.Errorf("uzantılar = %v", cfg.PHP.Extensions)
	}
	if cfg.Env["DB_HOST"] != "127.0.0.1" {
		t.Errorf("env = %v", cfg.Env)
	}
	if cfg.Processes["queue"] != "php artisan queue:work" {
		t.Errorf("processes = %v", cfg.Processes)
	}
	if len(cfg.Cron) != 1 || cfg.Cron[0].Schedule != "* * * * *" {
		t.Errorf("cron = %v", cfg.Cron)
	}
}

// "worker: 4" yazan biri (doğrusu "workers") sessizce varsayılanla
// çalışmak yerine yazım hatasını hemen görmeli.
func TestParseRejectsUnknownFields(t *testing.T) {
	_, err := Parse([]byte("name: a\ndomain: a.test\nworker: 4\n"))
	if err == nil {
		t.Fatal("bilinmeyen alan kabul edildi")
	}
	if !strings.Contains(err.Error(), "worker") {
		t.Errorf("hata alanın adını söylemiyor: %v", err)
	}
}

func TestDefaultServerIsDevBox(t *testing.T) {
	cfg, err := Parse([]byte("name: a\ndomain: a.test\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server != ServerDevBox {
		t.Errorf("varsayılan sunucu %q", cfg.Server)
	}
	if cfg.FrontController != "index.php" {
		t.Errorf("varsayılan ön denetleyici %q", cfg.FrontController)
	}
}

func TestValidateRejectsBadConfigs(t *testing.T) {
	cases := map[string]string{
		"ad yok":            "domain: a.test\n",
		"alan adı yok":      "name: a\n",
		"geçersiz sunucu":   "name: a\ndomain: a.test\nserver: iis\n",
		"proxy adresi yok":  "name: a\ndomain: a.test\nserver: proxy\n",
		"proxy şeması yok":  "name: a\ndomain: a.test\nserver: proxy\nproxy: 127.0.0.1:3000\n",
		"geçersiz alan adı": "name: a\ndomain: \"a b.test\"\n",
		"geçersiz ad":       "name: \"a/b\"\ndomain: a.test\n",
		"cron eksik":        "name: a\ndomain: a.test\ncron:\n  - schedule: \"* * * * *\"\n",
		"posta alan adı":    "name: a\ndomain: a.test\nmail:\n  host: \"a b.test\"\n",
		"posta adresi":      "name: a\ndomain: a.test\nmail:\n  smtp: \"1025\"\n",
		"posta kapasitesi":  "name: a\ndomain: a.test\nmail:\n  capacity: -1\n",
		"cron zamanlaması":  "name: a\ndomain: a.test\ncron:\n  - schedule: \"her dakika\"\n    run: ls\n",
	}
	for label, data := range cases {
		cfg, err := Parse([]byte(data))
		if err != nil {
			continue // ayrıştırma zaten reddetti
		}
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s: geçersiz yapılandırma kabul edildi", label)
		}
	}
}

// devbox.yaml depodan geliyor; klonlayan kişinin makinesinde istediği
// dizini sunmaya yetkisi olmamalı.
func TestRootCannotEscapeProjectDirectory(t *testing.T) {
	// filepath.IsAbs platforma bağlı: Windows'ta "/etc", Linux'ta
	// "C:/Windows" mutlak sayılmıyor. devbox.yaml depodan geldiği için iki
	// platformda da aynı biçimde reddedilmeli.
	for _, root := range []string{
		"../../etc", "/etc", "public/../../..", `..\windows`,
		`C:\Windows`, "c:/windows", `\\sunucu\paylasim`, "/",
	} {
		cfg, err := Parse([]byte("name: a\ndomain: a.test\nroot: " + root + "\n"))
		if err != nil {
			continue
		}
		if err := cfg.Validate(); err == nil {
			t.Errorf("root %q kabul edildi", root)
		}
	}
}

func TestDocumentRoot(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{dir: dir}
	if got := cfg.DocumentRoot(); got != dir {
		t.Errorf("root boşken belge kökü %q", got)
	}
	cfg.Root = "public"
	if got, want := cfg.DocumentRoot(), filepath.Join(dir, "public"); got != want {
		t.Errorf("belge kökü %q, beklenen %q", got, want)
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	original := &Config{
		Name: "magaza", Domain: "magaza.test", Server: ServerNginx, Root: "public",
		PHP: PHP{Version: "8.3", Xdebug: true},
	}
	if err := original.Save(dir); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Name != original.Name || loaded.Domain != original.Domain ||
		loaded.Server != original.Server || loaded.Root != original.Root ||
		loaded.PHP.Version != "8.3" || !loaded.PHP.Xdebug {
		t.Errorf("gidiş-dönüşte kayıp: %+v", loaded)
	}
	if loaded.Dir() != dir {
		t.Errorf("dizin %q, beklenen %q", loaded.Dir(), dir)
	}

	// Dosya, depoya eklenmesi gerektiğini söyleyen bir başlık taşımalı.
	data, _ := os.ReadFile(filepath.Join(dir, FileName))
	if !strings.Contains(string(data), "depoya ekleyin") {
		t.Error("üretilen dosyada açıklama başlığı yok")
	}
}

func TestUsesPHP(t *testing.T) {
	for _, server := range []string{ServerDevBox, ServerApache, ServerNginx} {
		if !(&Config{Server: server}).UsesPHP() {
			t.Errorf("%s için UsesPHP false", server)
		}
	}
	if (&Config{Server: ServerProxy}).UsesPHP() {
		t.Error("proxy için UsesPHP true")
	}
}

// --- çerçeve algılama -----------------------------------------------------

func TestDetectLaravel(t *testing.T) {
	dir := fixture(t, map[string]string{
		"artisan":          "#!/usr/bin/env php",
		"composer.json":    `{"require":{"laravel/framework":"^11.0","php":"^8.2"}}`,
		"public/index.php": "<?php",
	})
	got := Detect(dir)

	if got.Framework != "Laravel" {
		t.Fatalf("çerçeve = %q", got.Framework)
	}
	if got.Config.Root != "public" {
		t.Errorf("belge kökü = %q, beklenen public", got.Config.Root)
	}
	if got.Config.Server != ServerDevBox {
		t.Errorf("sunucu = %q", got.Config.Server)
	}
}

// WordPress kalıcı bağlantıları .htaccess yeniden yazma kurallarına
// dayanıyor; Apache dışında ek yapılandırma gerekir.
func TestDetectWordPressChoosesApache(t *testing.T) {
	dir := fixture(t, map[string]string{
		"wp-config.php": "<?php",
		"index.php":     "<?php",
	})
	got := Detect(dir)

	if got.Framework != "WordPress" {
		t.Fatalf("çerçeve = %q", got.Framework)
	}
	if got.Config.Server != ServerApache {
		t.Errorf("sunucu = %q, beklenen apache", got.Config.Server)
	}
	if len(got.Notes) == 0 || !strings.Contains(strings.Join(got.Notes, " "), "htaccess") {
		t.Error("Apache seçiminin sebebi açıklanmamış")
	}
}

func TestDetectSymfony(t *testing.T) {
	dir := fixture(t, map[string]string{
		"composer.json":    `{"require":{"symfony/framework-bundle":"^7.0"}}`,
		"public/index.php": "<?php",
	})
	if got := Detect(dir); got.Framework != "Symfony" || got.Config.Root != "public" {
		t.Errorf("algılama = %+v", got)
	}
}

func TestDetectNodeFrameworks(t *testing.T) {
	cases := map[string]struct{ framework, proxy string }{
		`{"dependencies":{"next":"14.0.0"}}`:   {"Next.js", "http://127.0.0.1:3000"},
		`{"dependencies":{"nuxt":"3.0.0"}}`:    {"Nuxt", "http://127.0.0.1:3000"},
		`{"devDependencies":{"vite":"5.0.0"}}`: {"Vite", "http://127.0.0.1:5173"},
	}
	for pkg, want := range cases {
		dir := fixture(t, map[string]string{"package.json": pkg})
		got := Detect(dir)
		if got.Framework != want.framework {
			t.Errorf("%s → %q, beklenen %q", pkg, got.Framework, want.framework)
			continue
		}
		if got.Config.Server != ServerProxy || got.Config.Proxy != want.proxy {
			t.Errorf("%s → sunucu %q, hedef %q", pkg, got.Config.Server, got.Config.Proxy)
		}
		if got.Config.Processes["dev"] == "" {
			t.Errorf("%s → geliştirme sunucusu processes'e eklenmemiş", pkg)
		}
	}
}

func TestDetectDjango(t *testing.T) {
	dir := fixture(t, map[string]string{"manage.py": "#!/usr/bin/env python"})
	got := Detect(dir)
	if got.Framework != "Django" || got.Config.Server != ServerProxy {
		t.Errorf("algılama = %+v", got)
	}
}

func TestDetectPlainPHP(t *testing.T) {
	dir := fixture(t, map[string]string{"index.php": "<?php"})
	got := Detect(dir)
	if got.Framework != "PHP" {
		t.Errorf("çerçeve = %q", got.Framework)
	}
	if got.Config.Root != "" {
		t.Errorf("belge kökü = %q, beklenen proje dizini", got.Config.Root)
	}
}

// Yanlış tahmin edip çalışmayan bir yapılandırma üretmektense en basitini
// önermek daha iyi.
func TestDetectFallsBackToStaticSite(t *testing.T) {
	dir := fixture(t, map[string]string{"okuma.txt": "merhaba"})
	got := Detect(dir)
	if got.Framework != "statik site" {
		t.Errorf("çerçeve = %q", got.Framework)
	}
	if len(got.Notes) == 0 {
		t.Error("kullanıcıya durum açıklanmamış")
	}
	if err := got.Config.Validate(); err != nil {
		t.Errorf("önerilen yapılandırma geçersiz: %v", err)
	}
}

func TestDetectSurvivesBrokenComposerJson(t *testing.T) {
	// Bozuk composer.json algılamayı durdurmamalı; diğer kurallar denensin.
	dir := fixture(t, map[string]string{
		"composer.json": "{bu json değil",
		"index.php":     "<?php",
	})
	if got := Detect(dir); got.Framework != "PHP" {
		t.Errorf("bozuk composer.json algılamayı bozdu: %q", got.Framework)
	}
}

func TestDetectedConfigsAreValid(t *testing.T) {
	fixtures := []map[string]string{
		{"artisan": "", "composer.json": `{"require":{"laravel/framework":"^11"}}`},
		{"wp-config.php": "<?php"},
		{"package.json": `{"dependencies":{"next":"14"}}`},
		{"manage.py": ""},
		{"index.php": "<?php"},
		{"okuma.txt": "x"},
	}
	for i, files := range fixtures {
		dir := fixture(t, files)
		got := Detect(dir)
		if err := got.Config.Validate(); err != nil {
			t.Errorf("%d. algılama geçersiz yapılandırma üretti (%s): %v", i, got.Framework, err)
		}
	}
}

func TestSuggestName(t *testing.T) {
	cases := map[string]string{
		"magaza":     "magaza",
		"My-Project": "my-project",
		"proje adı":  "proje-ad",
		"mağaza":     "ma-aza",
		"":           "proje",
		"...":        "proje",
		"web.sitesi": "web-sitesi",
	}
	for in, want := range cases {
		if got := suggestName(in); got != want {
			t.Errorf("suggestName(%q) = %q, beklenen %q", in, got, want)
		}
	}
}

// Posta kutusu, proje alan adının altında duruyor: sertifika joker adı ve
// çözücünün sahiplendiği son ek zaten onu kapsıyor.
func TestMailHostDefaultsUnderProjectDomain(t *testing.T) {
	cfg, err := Parse([]byte("name: magaza\ndomain: magaza.test\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.MailHost(); got != "mail.magaza.test" {
		t.Errorf("MailHost() = %q, beklenen mail.magaza.test", got)
	}

	cfg, err = Parse([]byte("name: magaza\ndomain: magaza.test\nmail:\n  host: posta.magaza.test\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.MailHost(); got != "posta.magaza.test" {
		t.Errorf("MailHost() = %q, yazılan değer kullanılmamış", got)
	}
}

// Yakalayıcı öntanımlı açık: test verisindeki gerçek bir adrese kazara
// posta gitmesi, bu aracın önlemesi gereken hatanın ta kendisi.
func TestMailIsEnabledByDefault(t *testing.T) {
	cfg, err := Parse([]byte("name: magaza\ndomain: magaza.test\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mail.Disabled {
		t.Error("posta yakalayıcı öntanımlı kapalı geldi")
	}
}
