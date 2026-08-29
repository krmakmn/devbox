package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// laragonKur, gerçekçi bir Laragon kurulumu kurgular.
func laragonKur(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mkdirs(t, root,
		"www/magaza/public", "www/magaza/app",
		"www/blog", "www/eski-site", "www/.gizli")
	write(t, filepath.Join(root, "laragon.exe"), "")
	write(t, filepath.Join(root, "www/magaza/composer.json"), `{"require":{"laravel/framework":"^11.0"}}`)
	write(t, filepath.Join(root, "www/magaza/artisan"), "")
	write(t, filepath.Join(root, "www/magaza/public/index.php"), "<?php")
	write(t, filepath.Join(root, "www/blog/index.php"), "<?php")
	write(t, filepath.Join(root, "www/eski-site/index.html"), "<html>")
	return root
}

func TestScanLaragonFindsSitesAndDocumentRoots(t *testing.T) {
	root := laragonKur(t)
	sites, err := Scan(SourceLaragon, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(sites) != 3 {
		t.Fatalf("%d site bulundu, 3 bekleniyordu: %+v", len(sites), sites)
	}

	byName := map[string]Site{}
	for _, s := range sites {
		byName[s.Name] = s
	}
	// Laragon "public varsa onu kullan" davranışını gösteriyor;
	// aynısını yapmazsak Laravel siteleri dizin listesi gösterir.
	if byName["magaza"].DocumentRoot != "public" {
		t.Errorf("magaza belge kökü = %q", byName["magaza"].DocumentRoot)
	}
	if byName["blog"].DocumentRoot != "" {
		t.Errorf("blog belge kökü = %q", byName["blog"].DocumentRoot)
	}
	// Nokta ile başlayan dizinler site değil.
	if _, ok := byName["gizli"]; ok {
		t.Error("gizli dizin site sayıldı")
	}
}

// XAMPP'ın kendi araçları site değil.
func TestScanXAMPPSkipsBundledTools(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "htdocs/dukkan", "htdocs/dashboard", "htdocs/img", "htdocs/webalizer")
	write(t, filepath.Join(root, "htdocs/dukkan/index.php"), "<?php")

	sites, err := Scan(SourceXAMPP, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(sites) != 1 || sites[0].Name != "dukkan" {
		t.Errorf("siteler = %+v", sites)
	}
}

func TestVirtualHostsProvideNamesAndAliases(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "htdocs/dukkan/public", "apache/conf/extra")
	write(t, filepath.Join(root, "htdocs/dukkan/public/index.php"), "<?php")

	conf := `
<VirtualHost *:80>
    DocumentRoot "` + filepath.Join(root, "htdocs/dukkan/public") + `"
    ServerName dukkan.local
    ServerAlias www.dukkan.local dukkan.dev
</VirtualHost>
`
	write(t, filepath.Join(root, "apache/conf/extra/httpd-vhosts.conf"), conf)

	sites, err := Scan(SourceXAMPP, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(sites) != 1 {
		t.Fatalf("%d site: %+v", len(sites), sites)
	}
	s := sites[0]
	if s.Domain != "dukkan.local" {
		t.Errorf("alan adı = %q", s.Domain)
	}
	if len(s.Aliases) != 2 {
		t.Errorf("takma adlar = %v", s.Aliases)
	}
	if s.DocumentRoot != "public" {
		t.Errorf("belge kökü = %q", s.DocumentRoot)
	}
}

// htdocs dışında bir dizini gösteren sanal konak göçte kaybolmamalı;
// üstelik proje dizini belge kökünün kendisi değil, üstü olmalı.
func TestVirtualHostOutsideDocumentRootBecomesItsOwnSite(t *testing.T) {
	root := t.TempDir()
	disari := filepath.Join(t.TempDir(), "api")
	mkdirs(t, root, "htdocs", "apache/conf/extra")
	if err := os.MkdirAll(filepath.Join(disari, "public"), 0o755); err != nil {
		t.Fatal(err)
	}

	conf := `
<VirtualHost *:80>
    DocumentRoot "` + filepath.Join(disari, "public") + `"
    ServerName api.local
</VirtualHost>
`
	write(t, filepath.Join(root, "apache/conf/extra/httpd-vhosts.conf"), conf)

	sites, err := Scan(SourceXAMPP, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(sites) != 1 {
		t.Fatalf("%d site: %+v", len(sites), sites)
	}
	if sites[0].Dir != disari {
		t.Errorf("proje dizini = %q, %q bekleniyordu", sites[0].Dir, disari)
	}
	if sites[0].DocumentRoot != "public" {
		t.Errorf("belge kökü = %q", sites[0].DocumentRoot)
	}
	if len(sites[0].Notes) == 0 {
		t.Error("olağandışı konum kullanıcıya söylenmiyor")
	}
}

// DevBox .test son ekine kurulu; başka bir son ek varsa değiştirilmeli
// ama eskisi takma ad olarak korunmalı — eski bağlantılar çalışsın.
func TestDomainForKeepsOldDomainAsAlias(t *testing.T) {
	cases := []struct {
		site        Site
		wantDomain  string
		wantAliases []string
	}{
		{Site{Name: "magaza"}, "magaza.test", nil},
		{Site{Name: "magaza", Domain: "magaza.test"}, "magaza.test", nil},
		{Site{Name: "dukkan", Domain: "dukkan.local"}, "dukkan.test", []string{"dukkan.local"}},
		{Site{Name: "api", Domain: "api.dev", Aliases: []string{"www.api.dev"}},
			"api.test", []string{"www.api.dev", "api.dev"}},
	}
	for _, c := range cases {
		domain, aliases := DomainFor(c.site)
		if domain != c.wantDomain {
			t.Errorf("%+v: alan adı = %q, %q bekleniyordu", c.site, domain, c.wantDomain)
		}
		if strings.Join(aliases, ",") != strings.Join(c.wantAliases, ",") {
			t.Errorf("%+v: takma adlar = %v, %v bekleniyordu", c.site, aliases, c.wantAliases)
		}
	}
}

func TestSuggestNameCleansDirectoryNames(t *testing.T) {
	cases := map[string]string{
		"Magaza":        "magaza",
		"my site v2":    "my-site-v2",
		"proje_1":       "proje_1",
		"--kenar--":     "kenar",
		"":              "proje",
		"benim-mağazam": "benim-mağazam",
	}
	for in, want := range cases {
		if got := suggestName(in); got != want {
			t.Errorf("suggestName(%q) = %q, %q bekleniyordu", in, got, want)
		}
	}
}

// Sanal konak dosyası olmayan ya da bozuk bir kurulum, taramayı
// düşürmemeli.
func TestMissingOrBrokenVhostFileIsTolerated(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "htdocs/site", "apache/conf/extra")
	write(t, filepath.Join(root, "apache/conf/extra/httpd-vhosts.conf"),
		"<VirtualHost *:80>\n  bu satır anlamsız\n")

	sites, err := Scan(SourceXAMPP, root)
	if err != nil {
		t.Fatalf("bozuk sanal konak dosyası taramayı düşürdü: %v", err)
	}
	if len(sites) != 1 {
		t.Errorf("%d site", len(sites))
	}
}

func TestDetectFindsInstallationFromExplicitRoot(t *testing.T) {
	root := laragonKur(t)
	found := Detect(root)
	if len(found) != 1 {
		t.Fatalf("%d kurulum bulundu", len(found))
	}
	if found[0].Source != SourceLaragon {
		t.Errorf("kaynak = %q", found[0].Source)
	}
	if len(found[0].Sites) != 3 {
		t.Errorf("%d site", len(found[0].Sites))
	}
}

func TestDetectIgnoresNonexistentRoots(t *testing.T) {
	if found := Detect(filepath.Join(t.TempDir(), "yok")); len(found) != 0 {
		t.Errorf("olmayan kök için kurulum bulundu: %+v", found)
	}
}

func mkdirs(t *testing.T, root string, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(d)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
