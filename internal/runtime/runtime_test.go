package runtime

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// --- sürüm karşılaştırma --------------------------------------------------

func TestVersionCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"8.3.14", "8.3.14", 0},
		{"8.3.14", "8.3.2", 1},
		{"8.3.2", "8.3.14", -1},
		{"8.4.0", "8.3.99", 1},
		{"8.3", "8.3.0", 0},
		{"9.0.0", "10.0.0", -1},
		// Kararlı sürüm, aynı sayılara sahip ön sürümden büyüktür.
		{"8.4.0", "8.4.0RC1", 1},
		{"8.4.0RC1", "8.4.0", -1},
		{"8.4.0RC2", "8.4.0RC1", 1},
	}
	for _, c := range cases {
		if got := ParseVersion(c.a).Compare(ParseVersion(c.b)); got != c.want {
			t.Errorf("Compare(%q, %q) = %d, beklenen %d", c.a, c.b, got, c.want)
		}
	}
}

func TestVersionMatches(t *testing.T) {
	cases := []struct {
		version, constraint string
		want                bool
	}{
		{"8.3.14", "", true},
		{"8.3.14", "latest", true},
		{"8.3.14", "8", true},
		{"8.3.14", "8.3", true},
		{"8.3.14", "8.3.14", true},
		{"8.3.14", "8.4", false},
		{"8.3.14", "7", false},
		{"8.3.2", "8.3.1", false},
		// Ön sürümler, kısıt onları açıkça istemedikçe eşleşmez: "php@8.4"
		// yazan biri RC almak istemez.
		{"8.4.0RC1", "8.4", false},
		{"8.4.0RC1", "8.4.0RC1", true},
	}
	for _, c := range cases {
		if got := ParseVersion(c.version).Matches(c.constraint); got != c.want {
			t.Errorf("%q.Matches(%q) = %v, beklenen %v", c.version, c.constraint, got, c.want)
		}
	}
}

func TestSplitSpec(t *testing.T) {
	cases := []struct{ in, name, constraint string }{
		{"php", "php", ""},
		{"php@8.3", "php", "8.3"},
		{"PHP@8.3.14", "php", "8.3.14"},
		{" node @ 20 ", "node", "20"},
	}
	for _, c := range cases {
		name, constraint := SplitSpec(c.in)
		if name != c.name || constraint != c.constraint {
			t.Errorf("SplitSpec(%q) = %q,%q; beklenen %q,%q", c.in, name, constraint, c.name, c.constraint)
		}
	}
}

// --- manifest -------------------------------------------------------------

func testPackage(url, sha string, size int64) Package {
	return Package{
		Name: "php", Version: "8.3.14", OS: "windows", Arch: "amd64",
		URL: url, SHA256: sha, Size: size, Archive: "zip",
		StripPrefix: "php-8.3.14-Win32",
		Bin:         map[string]string{"php": "php.exe", "php-cgi": "php-cgi.exe"},
	}
}

func manifestJSON(t *testing.T, pkgs ...Package) []byte {
	t.Helper()
	data, err := json.Marshal(Manifest{SchemaVersion: 1, Runtimes: pkgs})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestManifestSignatureRequired(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	data := manifestJSON(t, testPackage("https://ornek/php.zip", strings.Repeat("a", 64), 10))
	sig := Sign(data, priv)

	if _, err := ParseManifest(data, sig, pub); err != nil {
		t.Fatalf("geçerli imza reddedildi: %v", err)
	}

	// Manifest değiştirilirse imza tutmamalı. Aksi hâlde araya giren biri
	// kullanıcıya istediği ikiliyi kurdurabilir.
	tampered := bytes.Replace(data, []byte("8.3.14"), []byte("6.6.66"), 1)
	if _, err := ParseManifest(tampered, sig, pub); err != ErrUnsigned {
		t.Errorf("değiştirilmiş manifest kabul edildi: %v", err)
	}

	// Başka bir anahtarla imzalanmış manifest de reddedilmeli.
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := ParseManifest(data, Sign(data, other), pub); err != ErrUnsigned {
		t.Errorf("yabancı imza kabul edildi: %v", err)
	}

	// İmza yoksa ve anahtar bekleniyorsa reddedilmeli.
	if _, err := ParseManifest(data, nil, pub); err != ErrUnsigned {
		t.Errorf("imzasız manifest kabul edildi: %v", err)
	}
}

func TestManifestRejectsUnsafePackages(t *testing.T) {
	bad := []Package{
		{Name: "", Version: "1", OS: "windows", Arch: "amd64", URL: "https://x/a.zip", SHA256: strings.Repeat("a", 64), Archive: "zip"},
		{Name: "php", Version: "../../evil", OS: "windows", Arch: "amd64", URL: "https://x/a.zip", SHA256: strings.Repeat("a", 64), Archive: "zip"},
		{Name: "ph p", Version: "1", OS: "windows", Arch: "amd64", URL: "https://x/a.zip", SHA256: strings.Repeat("a", 64), Archive: "zip"},
		// Düz HTTP: SHA256 doğrulasak bile adresin değiştirilmesine kapı açar.
		{Name: "php", Version: "1", OS: "windows", Arch: "amd64", URL: "http://x/a.zip", SHA256: strings.Repeat("a", 64), Archive: "zip"},
		{Name: "php", Version: "1", OS: "windows", Arch: "amd64", URL: "https://x/a.zip", SHA256: "kısa", Archive: "zip"},
		{Name: "php", Version: "1", OS: "windows", Arch: "amd64", URL: "https://x/a.zip", SHA256: strings.Repeat("a", 64), Archive: "rar"},
		// İkili yolu kurulum dizininin dışını gösteremez.
		{Name: "php", Version: "1", OS: "windows", Arch: "amd64", URL: "https://x/a.zip", SHA256: strings.Repeat("a", 64), Archive: "zip",
			Bin: map[string]string{"php": "../../windows/system32/cmd.exe"}},
	}
	for i, p := range bad {
		if err := p.Validate(); err == nil {
			t.Errorf("%d. geçersiz paket kabul edildi: %+v", i, p)
		}
	}
}

func TestManifestSelectPrefersNewest(t *testing.T) {
	m := &Manifest{SchemaVersion: 1, Runtimes: []Package{
		{Name: "php", Version: "8.3.2", OS: "windows", Arch: "amd64"},
		{Name: "php", Version: "8.3.14", OS: "windows", Arch: "amd64"},
		{Name: "php", Version: "8.4.1", OS: "windows", Arch: "amd64"},
		{Name: "php", Version: "8.3.20", OS: "linux", Arch: "amd64"},
	}}

	got := m.Find("php", "8.3", "windows", "amd64")
	if len(got) != 2 || got[0].Version != "8.3.14" {
		t.Errorf("8.3 kısıtı = %v, beklenen en yeni 8.3.14", versionsOf(got))
	}
	if all := m.Find("php", "", "windows", "amd64"); len(all) != 3 || all[0].Version != "8.4.1" {
		t.Errorf("kısıtsız = %v, beklenen en yeni 8.4.1", versionsOf(all))
	}
	// Başka platformun paketi sızmamalı.
	for _, p := range m.Find("php", "", "windows", "amd64") {
		if p.OS != "windows" {
			t.Errorf("yanlış platform seçildi: %+v", p)
		}
	}
}

func versionsOf(pkgs []Package) []string {
	var out []string
	for _, p := range pkgs {
		out = append(out, p.Version)
	}
	return out
}

func TestManifestRejectsUnknownSchema(t *testing.T) {
	data := []byte(`{"schemaVersion":99,"runtimes":[]}`)
	if _, err := ParseManifest(data, nil, nil); err == nil {
		t.Error("bilinmeyen şema sürümü kabul edildi")
	}
	// Bilinmeyen alan, biçim değişikliğinin sessizce yutulmasını önler.
	if _, err := ParseManifest([]byte(`{"schemaVersion":1,"runtimes":[],"yeni":1}`), nil, nil); err == nil {
		t.Error("bilinmeyen alan kabul edildi")
	}
}

// --- arşiv güvenliği ------------------------------------------------------

// makeZip, verilen girdilerden bir zip üretir.
func makeZip(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, content := range entries {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func writeTemp(t *testing.T, data []byte, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// "Zip Slip": arşivdeki girdi adı dizinin dışını gösterirse, naif bir açıcı
// dosyayı hedefin dışına yazar. Arşivleri biz üretmediğimiz için bu denetim
// zorunlu.
func TestExtractRejectsZipSlip(t *testing.T) {
	attacks := []string{
		"../kacti.txt",
		"a/../../kacti.txt",
		`..\kacti.txt`,
		"/mutlak.txt",
	}
	for _, name := range attacks {
		archive := writeTemp(t, makeZip(t, map[string]string{name: "kötü"}), "kotu.zip")
		dest := t.TempDir()
		parent := filepath.Dir(dest)

		err := extract(archive, dest, "zip", "")
		if err == nil {
			t.Errorf("%q kabul edildi", name)
		}
		if fileExists(filepath.Join(parent, "kacti.txt")) {
			t.Fatalf("%q hedef dizinin dışına yazdı", name)
		}
	}
}

func TestExtractStripsPrefix(t *testing.T) {
	archive := writeTemp(t, makeZip(t, map[string]string{
		"php-8.3.14-Win32/php.exe":         "ikili",
		"php-8.3.14-Win32/ext/gd.dll":      "uzantı",
		"php-8.3.14-Win32/php.ini-develop": "ayar",
		"baska-dizin/atlanmali.txt":        "x",
	}), "php.zip")
	dest := t.TempDir()

	if err := extract(archive, dest, "zip", "php-8.3.14-Win32"); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if !fileExists(filepath.Join(dest, "php.exe")) {
		t.Error("php.exe kök dizine açılmadı")
	}
	if !fileExists(filepath.Join(dest, "ext", "gd.dll")) {
		t.Error("alt dizin korunmadı")
	}
	if fileExists(filepath.Join(dest, "atlanmali.txt")) || dirExists(filepath.Join(dest, "baska-dizin")) {
		t.Error("önek dışındaki girdi açıldı")
	}
}

// --- indirme --------------------------------------------------------------

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestDownloadVerifiesChecksum(t *testing.T) {
	payload := []byte("gerçek içerik")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("sahte içerik"))
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "dosya.zip")
	err := download(context.Background(), srv.Client(), srv.URL, dest, sha256Hex(payload), 0, nil)
	if err == nil {
		t.Fatal("SHA256 uyuşmadığı hâlde indirme başarılı sayıldı")
	}
	if !strings.Contains(err.Error(), "SHA256") {
		t.Errorf("hata mesajı sebebi anlatmıyor: %v", err)
	}
	if fileExists(dest) {
		t.Error("doğrulanamayan dosya diske kalıcı olarak yazıldı")
	}
	if fileExists(dest + ".part") {
		t.Error("bozuk parça dosyası silinmedi; sonraki denemeler de başarısız olurdu")
	}
}

func TestDownloadResumesAfterInterruption(t *testing.T) {
	payload := bytes.Repeat([]byte("DevBox"), 20000) // ~120 KB
	var requests atomic.Int32
	var sawRange atomic.Bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)

		if rangeHeader := r.Header.Get("Range"); rangeHeader != "" {
			sawRange.Store(true)
			var start int64
			fmt.Sscanf(rangeHeader, "bytes=%d-", &start)
			if start >= int64(len(payload)) {
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(payload)-1, len(payload)))
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)-int(start)))
			w.WriteHeader(http.StatusPartialContent)
			w.Write(payload[start:])
			return
		}

		if n == 1 {
			// İlk denemede bağlantı yarıda kopsun.
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.WriteHeader(http.StatusOK)
			w.Write(payload[:len(payload)/2])
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			panic(http.ErrAbortHandler)
		}
		w.Write(payload)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "buyuk.bin")
	sum := sha256Hex(payload)

	// İlk deneme başarısız olmalı ama parça dosyası kalmalı.
	if err := download(context.Background(), srv.Client(), srv.URL, dest, sum, int64(len(payload)), nil); err == nil {
		t.Fatal("kopan indirme başarılı sayıldı")
	}
	info, err := os.Stat(dest + ".part")
	if err != nil {
		t.Fatalf("parça dosyası tutulmamış: %v", err)
	}
	if info.Size() == 0 || info.Size() >= int64(len(payload)) {
		t.Fatalf("parça dosyası boyutu beklenmedik: %d", info.Size())
	}

	// İkinci deneme kaldığı yerden sürmeli.
	if err := download(context.Background(), srv.Client(), srv.URL, dest, sum, int64(len(payload)), nil); err != nil {
		t.Fatalf("sürdürülen indirme başarısız: %v", err)
	}
	if !sawRange.Load() {
		t.Error("Range isteği gönderilmedi; indirme baştan yapılmış")
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("sürdürülen dosya bozuk: %d bayt, beklenen %d", len(got), len(payload))
	}
}

func TestDownloadReportsProgress(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 600*1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(payload)
	}))
	defer srv.Close()

	var last Progress
	var calls int
	dest := filepath.Join(t.TempDir(), "d.bin")
	err := download(context.Background(), srv.Client(), srv.URL, dest, sha256Hex(payload), int64(len(payload)),
		func(p Progress) { calls++; last = p })
	if err != nil {
		t.Fatal(err)
	}
	if calls == 0 {
		t.Error("ilerleme hiç bildirilmedi")
	}
	if last.Downloaded != int64(len(payload)) {
		t.Errorf("son ilerleme %d, beklenen %d", last.Downloaded, len(payload))
	}
	if p := last.Percent(); p < 99.9 {
		t.Errorf("son yüzde %.1f", p)
	}
}

// --- depo -----------------------------------------------------------------

// phpArchive, kurulabilir sahte bir PHP dağıtımı üretir.
func phpArchive(t *testing.T) []byte {
	t.Helper()
	return makeZip(t, map[string]string{
		"php-8.3.14-Win32/php.exe":     "sahte php",
		"php-8.3.14-Win32/php-cgi.exe": "sahte php-cgi",
		"php-8.3.14-Win32/ext/gd.dll":  "sahte uzantı",
	})
}

// serveArchive, arşivi HTTPS üzerinden sunar.
//
// Düz HTTP kullanmıyoruz: Package.Validate indirme adresinin https olmasını
// şart koşuyor ve testin o doğrulamayı atlamasını istemiyoruz — atlarsak
// kurulumun gerçek yolu hiç sınanmamış olur.
func serveArchive(t *testing.T, data []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.Write(data)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestStoreInstall(t *testing.T) {
	archive := phpArchive(t)
	srv := serveArchive(t, archive)

	store := NewStore(t.TempDir()).WithClient(srv.Client())
	pkg := testPackage(srv.URL+"/php.zip", sha256Hex(archive), int64(len(archive)))
	inst, err := store.Install(context.Background(), pkg, nil)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	if !fileExists(filepath.Join(inst.Dir, "php.exe")) {
		t.Error("php.exe kurulmadı")
	}
	if !fileExists(filepath.Join(inst.Dir, "ext", "gd.dll")) {
		t.Error("uzantı dizini kurulmadı")
	}

	bin, err := inst.Bin("php-cgi")
	if err != nil {
		t.Fatalf("Bin: %v", err)
	}
	if !fileExists(bin) {
		t.Errorf("php-cgi yolu yanlış: %s", bin)
	}
	if _, err := inst.Bin("olmayan"); err == nil {
		t.Error("tanımsız ikili için hata dönmedi")
	}

	// Kurulum dizini arşivden çıktığı gibi kalmalı; üstveri yanında durur.
	entries, _ := os.ReadDir(inst.Dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			t.Errorf("üstveri kurulum dizinine sızmış: %s", e.Name())
		}
	}
}

func TestStoreInstallIsIdempotent(t *testing.T) {
	archive := phpArchive(t)
	srv := serveArchive(t, archive)
	store := NewStore(t.TempDir()).WithClient(srv.Client())
	pkg := testPackage(srv.URL, sha256Hex(archive), int64(len(archive)))

	first, err := store.Install(context.Background(), pkg, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Install(context.Background(), pkg, nil)
	if err != nil {
		t.Fatalf("ikinci kurulum: %v", err)
	}
	if first.Dir != second.Dir {
		t.Errorf("dizin değişti: %s → %s", first.Dir, second.Dir)
	}
	if !second.InstalledAt.Equal(first.InstalledAt) {
		t.Error("kurulu paket yeniden kuruldu")
	}
}

func TestStoreRejectsArchiveMissingDeclaredBinary(t *testing.T) {
	// Manifest php.exe vaat ediyor ama arşivde yok. İlk kullanımda
	// "dosya bulunamadı" almaktansa kurulumda öğrenmek gerekir.
	archive := makeZip(t, map[string]string{"php-8.3.14-Win32/okuma.txt": "yalnız belge"})
	srv := serveArchive(t, archive)
	store := NewStore(t.TempDir()).WithClient(srv.Client())
	pkg := testPackage(srv.URL, sha256Hex(archive), int64(len(archive)))

	_, err := store.Install(context.Background(), pkg, nil)
	if err == nil {
		t.Fatal("eksik ikiliye rağmen kurulum başarılı sayıldı")
	}
	// Hangi ikilinin önce denetlendiği harita sırasına bağlı; ikisinden
	// biri adıyla anılmalı.
	if !strings.Contains(err.Error(), "php.exe") && !strings.Contains(err.Error(), "php-cgi.exe") {
		t.Errorf("hata hangi dosyanın eksik olduğunu söylemiyor: %v", err)
	}
	// Yarım kurulum "kurulu" görünmemeli.
	if _, ok, _ := store.Resolve("php"); ok {
		t.Error("başarısız kurulum kurulu sayıldı")
	}
}

func TestStoreListAndResolve(t *testing.T) {
	store := NewStore(t.TempDir())
	for _, v := range []string{"8.2.10", "8.3.2", "8.3.14"} {
		fakeInstall(t, store, "php", v)
	}
	fakeInstall(t, store, "node", "20.11.0")

	all, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 {
		t.Fatalf("%d kurulum listelendi, beklenen 4", len(all))
	}

	inst, ok, err := store.Resolve("php@8.3")
	if err != nil || !ok {
		t.Fatalf("php@8.3 çözülemedi: %v", err)
	}
	if inst.Package.Version != "8.3.14" {
		t.Errorf("php@8.3 → %s, beklenen 8.3.14 (en yeni)", inst.Package.Version)
	}

	if inst, ok, _ := store.Resolve("php"); !ok || inst.Package.Version != "8.3.14" {
		t.Errorf("kısıtsız php → %+v", inst.Package.Version)
	}
	if _, ok, _ := store.Resolve("php@7"); ok {
		t.Error("kurulu olmayan sürüm çözüldü")
	}
	if _, ok, _ := store.Resolve("python"); ok {
		t.Error("kurulu olmayan runtime çözüldü")
	}
}

func TestStoreIgnoresDirectoryWithoutMetadata(t *testing.T) {
	// Elle kopyalanmış ya da yarım kalmış bir dizin "kurulu" sayılmamalı;
	// aksi hâlde üzerine düzgün kurulum gelemez.
	store := NewStore(t.TempDir())
	orphan := filepath.Join(store.Root(), "php", "8.3.14")
	if err := os.MkdirAll(orphan, 0o755); err != nil {
		t.Fatal(err)
	}
	if all, _ := store.List(); len(all) != 0 {
		t.Errorf("üstverisiz dizin listelendi: %+v", all)
	}
}

func TestStoreRemove(t *testing.T) {
	store := NewStore(t.TempDir())
	fakeInstall(t, store, "php", "8.3.14")
	fakeInstall(t, store, "php", "8.2.10")

	if err := store.Remove("php", "8.3.14"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if all, _ := store.List(); len(all) != 1 || all[0].Package.Version != "8.2.10" {
		t.Errorf("kaldırmadan sonra kalan: %+v", all)
	}
	if err := store.Remove("php", "8.3.14"); err == nil {
		t.Error("kurulu olmayan sürüm için hata dönmedi")
	}

	// Son sürüm de gidince boş ad dizini kalmamalı.
	if err := store.Remove("php", "8.2.10"); err != nil {
		t.Fatal(err)
	}
	if dirExists(filepath.Join(store.Root(), "php")) {
		t.Error("boş runtime dizini bırakıldı")
	}
}

func TestStorePruneCacheKeepsInstallations(t *testing.T) {
	archive := phpArchive(t)
	srv := serveArchive(t, archive)
	store := NewStore(t.TempDir()).WithClient(srv.Client())
	pkg := testPackage(srv.URL, sha256Hex(archive), int64(len(archive)))

	inst, err := store.Install(context.Background(), pkg, nil)
	if err != nil {
		t.Fatal(err)
	}

	freed, err := store.PruneCache()
	if err != nil {
		t.Fatal(err)
	}
	if freed == 0 {
		t.Error("önbellekten hiçbir şey silinmedi")
	}
	if !fileExists(filepath.Join(inst.Dir, "php.exe")) {
		t.Error("önbellek temizliği kurulumu bozdu")
	}
	if all, _ := store.List(); len(all) != 1 {
		t.Error("önbellek temizliği kurulum kaydını sildi")
	}
}

func TestStorePruneStale(t *testing.T) {
	store := NewStore(t.TempDir())
	stale := filepath.Join(store.tmpDir(), "php-8.3.14-yarim")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(store.tmpDir(), "php-8.4.0-suren")
	if err := os.MkdirAll(fresh, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := store.PruneStale(time.Hour); err != nil {
		t.Fatal(err)
	}
	if dirExists(stale) {
		t.Error("eski geçici dizin silinmedi")
	}
	if !dirExists(fresh) {
		t.Error("süren kurulumun geçici dizini silindi")
	}
}

// fakeInstall, indirme yapmadan kurulu bir sürüm oluşturur.
func fakeInstall(t *testing.T, s *Store, name, version string) {
	t.Helper()
	dir := s.versionDir(name, version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".exe"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	inst := Installed{
		Package: Package{
			Name: name, Version: version, OS: "windows", Arch: "amd64",
			Archive: "zip", Bin: map[string]string{name: name + ".exe"},
		},
		InstalledAt: time.Now().UTC(),
	}
	if err := s.writeMeta(inst); err != nil {
		t.Fatal(err)
	}
}
