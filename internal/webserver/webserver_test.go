package webserver

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "altın dosyaları yeniden üret")

func TestMain(m *testing.M) {
	if os.Getenv("DEVBOX_FAKE_WEBSERVER") == "1" {
		runFakeWebServer()
		return
	}
	os.Exit(m.Run())
}

// runFakeWebServer, httpd/nginx yerine geçen sahte bir çalıştırılabilir.
// Çağrıldığı argümanları FAKE_LOG dosyasına ekler; FAKE_VALIDATE_FAIL=1 ise
// söz dizimi denetimini reddeder.
func runFakeWebServer() {
	args := strings.Join(os.Args[1:], " ")
	if logPath := os.Getenv("FAKE_LOG"); logPath != "" {
		f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err == nil {
			fmt.Fprintln(f, args)
			f.Close()
		}
	}
	if os.Getenv("FAKE_VALIDATE_FAIL") == "1" && strings.Contains(args, "-t") {
		fmt.Fprintln(os.Stderr, "Syntax error on line 42: yapılandırma hatalı")
		os.Exit(1)
	}
	os.Exit(0)
}

func testSites() []Site {
	return []Site{
		{
			Name:         "magaza",
			Domain:       "magaza.test",
			Aliases:      []string{"www.magaza.test", "*.magaza.test"},
			DocumentRoot: `C:\projeler\magaza\public`,
			Listen:       "127.0.0.1:8080",
			PHPBackends:  []string{"127.0.0.1:9000", "127.0.0.1:9001"},
			LogDir:       `C:\devbox\log`,
		},
		{
			// PHP'siz statik site.
			Name:         "belgeler",
			Domain:       "belgeler.test",
			DocumentRoot: `C:\projeler\belgeler`,
			Listen:       "127.0.0.1:8080",
			Index:        []string{"index.html"},
			LogDir:       `C:\devbox\log`,
		},
	}
}

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)

	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("altın dosya okunamadı (-update ile üretin): %v", err)
	}
	if got != string(want) {
		t.Errorf("üretilen yapılandırma altın dosyadan farklı.\n--- üretilen ---\n%s\n--- beklenen ---\n%s", got, want)
	}
}

func TestApacheRender(t *testing.T) {
	got, err := (&Apache{}).Render(testSites())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	checkGolden(t, "apache.conf", got)
}

func TestNginxRender(t *testing.T) {
	got, err := (&Nginx{}).Render(testSites())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	checkGolden(t, "nginx.conf", got)
}

// Apache ve Nginx, Windows'ta bile eğik bölü bekler; ters bölü kaçış
// karakteri sayılır ve yol sessizce bozulur.
func TestWindowsPathsUseForwardSlashes(t *testing.T) {
	for _, d := range []Driver{&Apache{}, &Nginx{}} {
		got, err := d.Render(testSites())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(got, `\`) && !strings.Contains(got, `\.`) {
			t.Errorf("%s: yapılandırmada ters bölü var:\n%s", d.Name(), got)
		}
		if !strings.Contains(got, "C:/projeler/magaza/public") {
			t.Errorf("%s: belge kökü eğik bölüye çevrilmemiş", d.Name())
		}
	}
}

// Bu satır olmadan nginx var olmayan bir .php yolunu da PHP'ye gönderir ve
// cgi.fix_pathinfo açıkken yüklenmiş bir resim PHP olarak çalıştırılabilir.
func TestNginxGuardsAgainstPathInfoExecution(t *testing.T) {
	got, err := (&Nginx{}).Render(testSites())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "try_files $uri =404;") {
		t.Error("php location bloğunda try_files koruması yok")
	}
}

func TestApacheDeniesMissingPHPFiles(t *testing.T) {
	got, err := (&Apache{}).Render(testSites())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `<If "-f %{REQUEST_FILENAME}">`) {
		t.Error("var olmayan .php yolları için koruma yok")
	}
	if !strings.Contains(got, "<Else>") || !strings.Contains(got, "Require all denied") {
		t.Error("var olmayan .php yolu reddedilmiyor")
	}
	if !strings.Contains(got, "Require local") {
		t.Error("sunucu loopback ile sınırlanmamış")
	}
}

// PHP, HTTPS değişkenini "boş ve 'off' değilse açık" diye yorumlar.
// X-Forwarded-Proto'yu doğrudan geçirmek, düz HTTP isteğinde bile PHP'nin
// kendini HTTPS altında sanmasına yol açar; mutlak URL'ler ve güvenli çerez
// kararları bozulur.
func TestHTTPSFlagIsNotLeakedOnPlainHTTP(t *testing.T) {
	nginx, err := (&Nginx{}).Render(testSites())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(nginx, "fastcgi_param HTTPS $http_x_forwarded_proto") {
		t.Error("nginx: X-Forwarded-Proto doğrudan HTTPS'e geçiriliyor")
	}
	if !strings.Contains(nginx, "map $http_x_forwarded_proto $devbox_https") {
		t.Error("nginx: şema eşlemesi yok")
	}
	if !strings.Contains(nginx, "fastcgi_param HTTPS $devbox_https") {
		t.Error("nginx: HTTPS eşlenmiş değişkenden alınmıyor")
	}

	apache, err := (&Apache{}).Render(testSites())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(apache, `SetEnvIf X-Forwarded-Proto "^https$" HTTPS=on`) {
		t.Error("apache: HTTPS yalnız tam eşleşmede ayarlanmıyor")
	}
}

// PHP'siz bir sitede try_files, var olmayan bir ön denetleyiciye
// yönlendirilmemeli; anlamsız bir iç yönlendirme üretir.
func TestStaticSiteFallsBackTo404(t *testing.T) {
	sites := []Site{{
		Name: "belgeler", Domain: "belgeler.test",
		DocumentRoot: "/x", Listen: "127.0.0.1:8080", LogDir: "/log",
	}}
	got, err := (&Nginx{}).Render(sites)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "try_files $uri $uri/ =404;") {
		t.Errorf("PHP'siz sitede 404 geri düşüşü yok:\n%s", got)
	}
	if strings.Contains(got, "/index.php?$query_string") {
		t.Error("PHP'siz sitede ön denetleyiciye yönlendirme var")
	}
}

func TestBlocksDotFiles(t *testing.T) {
	apache, _ := (&Apache{}).Render(testSites())
	if !strings.Contains(apache, `<DirectoryMatch "/\.">`) {
		t.Error("apache: nokta ile başlayan yollar engellenmemiş")
	}
	nginx, _ := (&Nginx{}).Render(testSites())
	if !strings.Contains(nginx, `location ~ /\.(?!well-known).*`) {
		t.Error("nginx: nokta ile başlayan yollar engellenmemiş")
	}
	// ACME doğrulaması çalışmaya devam etmeli.
	if !strings.Contains(nginx, "well-known") {
		t.Error("nginx: .well-known ayrık tutulmamış")
	}
}

// Değerler doğrudan yapılandırma dosyasına yazıldığı için, satır sonu ya da
// tırnak içeren bir alan adı üretilen dosyaya yönerge eklemek demek.
func TestRejectsUnsafeValues(t *testing.T) {
	base := Site{Name: "a", Domain: "a.test", DocumentRoot: "/x", Listen: "127.0.0.1:80", LogDir: "/log"}

	bad := []Site{
		func() Site { s := base; s.Domain = "a.test\n    Require all granted"; return s }(),
		func() Site { s := base; s.Domain = `a.test" evil "`; return s }(),
		func() Site { s := base; s.Name = "../kacti"; return s }(),
		func() Site { s := base; s.Listen = "127.0.0.1:80;\nevil"; return s }(),
		func() Site { s := base; s.PHPBackends = []string{"127.0.0.1:9000;evil"}; return s }(),
		func() Site { s := base; s.Index = []string{"index.php\ndeny all"}; return s }(),
		func() Site { s := base; s.FrontController = "../../evil.php"; return s }(),
	}

	for i, s := range bad {
		if err := s.Validate(); err == nil {
			t.Errorf("%d. güvensiz site kabul edildi: %+v", i, s)
		}
		if _, err := (&Nginx{}).Render([]Site{s}); err == nil {
			t.Errorf("%d. güvensiz site nginx yapılandırmasına yazıldı", i)
		}
		if _, err := (&Apache{}).Render([]Site{s}); err == nil {
			t.Errorf("%d. güvensiz site apache yapılandırmasına yazıldı", i)
		}
	}
}

// Günlük dizini olmayan bir site, sunucunun derleme varsayılanına düşer:
// tüm siteler tek dosyaya yazar ve DevBox günlükleri bulamaz. Bu, root
// olmayan bir kullanıcıyla koşulan CI'da izin hatası olarak ortaya çıkmıştı.
func TestRejectsSiteWithoutLogDir(t *testing.T) {
	s := Site{Name: "a", Domain: "a.test", DocumentRoot: "/x", Listen: "127.0.0.1:80"}
	err := s.Validate()
	if err == nil {
		t.Fatal("günlük dizini olmayan site kabul edildi")
	}
	if !strings.Contains(err.Error(), "günlük") {
		t.Errorf("hata sebebi anlatmıyor: %v", err)
	}
}

func TestRejectsDuplicateSiteNames(t *testing.T) {
	sites := []Site{
		{Name: "a", Domain: "a.test", DocumentRoot: "/x", Listen: "127.0.0.1:80", LogDir: "/log"},
		{Name: "a", Domain: "b.test", DocumentRoot: "/y", Listen: "127.0.0.1:80", LogDir: "/log"},
	}
	if _, err := (&Nginx{}).Render(sites); err == nil {
		t.Error("aynı adlı iki site kabul edildi")
	}
}

// Apache, BOM'lu bir yapılandırma dosyasında tamamen yanıltıcı bir hata
// verir ve sebebini bulmak saatler alır.
func TestWriteUsesLFWithoutBOM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sites.conf")
	if err := (&Apache{}).Write(path, testSites()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "\r\n") {
		t.Error("yapılandırmada CRLF var")
	}
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		t.Error("yapılandırma BOM ile başlıyor")
	}
	if !strings.Contains(string(data), "DevBox tarafından üretildi") {
		t.Error("üretildiğini belirten başlık yok")
	}
}

// --- Apply ----------------------------------------------------------------

func fakeDriver(t *testing.T, validateFails bool) (*Nginx, string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "cagrilar.log")

	t.Setenv("DEVBOX_FAKE_WEBSERVER", "1")
	t.Setenv("FAKE_LOG", logPath)
	if validateFails {
		t.Setenv("FAKE_VALIDATE_FAIL", "1")
	}
	return &Nginx{Binary: os.Args[0]}, logPath
}

func TestApplyWritesValidatesAndReloads(t *testing.T) {
	driver, logPath := fakeDriver(t, false)
	path := filepath.Join(t.TempDir(), "sites.conf")

	if err := Apply(context.Background(), driver, path, testSites()); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	calls, _ := os.ReadFile(logPath)
	text := string(calls)
	if !strings.Contains(text, "-t") {
		t.Error("söz dizimi denetimi çalıştırılmadı")
	}
	if !strings.Contains(text, "-s reload") {
		t.Error("yeniden yükleme çalıştırılmadı")
	}
	// Sıra önemli: doğrulama yeniden yüklemeden önce gelmeli.
	if strings.Index(text, "-t") > strings.Index(text, "-s reload") {
		t.Error("yeniden yükleme doğrulamadan önce yapıldı")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("yapılandırma yazılmadı: %v", err)
	}
}

// Bozuk bir yapılandırmayla yeniden yükleme, çalışan siteleri de düşürür.
func TestApplyRollsBackWhenValidationFails(t *testing.T) {
	driver, logPath := fakeDriver(t, true)
	path := filepath.Join(t.TempDir(), "sites.conf")

	const previous = "# önceki çalışan yapılandırma\n"
	if err := os.WriteFile(path, []byte(previous), 0o644); err != nil {
		t.Fatal(err)
	}

	err := Apply(context.Background(), driver, path, testSites())
	if err == nil {
		t.Fatal("doğrulama başarısız olduğu hâlde Apply başarılı döndü")
	}
	if !strings.Contains(err.Error(), "line 42") {
		t.Errorf("hata sunucunun mesajını taşımıyor: %v", err)
	}

	data, _ := os.ReadFile(path)
	if string(data) != previous {
		t.Errorf("eski yapılandırma geri konmadı:\n%s", data)
	}
	if calls, _ := os.ReadFile(logPath); strings.Contains(string(calls), "-s reload") {
		t.Error("doğrulama başarısızken yeniden yükleme yapıldı")
	}
}

func TestApplyRemovesFileWhenValidationFailsAndNoPrevious(t *testing.T) {
	driver, _ := fakeDriver(t, true)
	path := filepath.Join(t.TempDir(), "sites.conf")

	if err := Apply(context.Background(), driver, path, testSites()); err == nil {
		t.Fatal("doğrulama başarısız olduğu hâlde Apply başarılı döndü")
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("doğrulanamayan yapılandırma dosyası bırakıldı")
	}
}

// Sunucu kurulu değilken yapılandırma üretmek meşru bir senaryo.
func TestApplyWithoutBinaryIsNotAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sites.conf")
	if err := Apply(context.Background(), &Nginx{}, path, testSites()); err != nil {
		t.Fatalf("çalıştırılabilir yokken hata döndü: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("yapılandırma yazılmadı: %v", err)
	}
}

func TestValidateReportsMissingBinary(t *testing.T) {
	err := (&Apache{Binary: filepath.Join(t.TempDir(), "yok-httpd")}).Validate(context.Background(), "x.conf")
	if err == nil {
		t.Fatal("olmayan çalıştırılabilir için hata dönmedi")
	}
	if !strings.Contains(err.Error(), "tanımlı değil") {
		t.Errorf("hata = %v", err)
	}
}
