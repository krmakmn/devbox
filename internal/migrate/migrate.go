// Package migrate, Laragon ve XAMPP kurulumlarındaki siteleri DevBox'a
// taşır.
//
// # Neden bu, benimsemenin en kritik parçası
//
// Bir geliştirme ortamını değiştirmek, çalışan on beş projeyi yeniden
// kurmak demek. Çoğu kişi bunu yapmaz — araç ne kadar iyi olursa olsun.
// Göç aracı bu bedeli ortadan kaldırıyor: var olan siteler bulunuyor,
// her biri için devbox.yaml üretiliyor ve kayda ekleniyor.
//
// # Neden dosyalar kopyalanmıyor
//
// Proje dizinleri olduğu yerde kalıyor; DevBox onları bulunduğu yerden
// sunuyor. Kopyalamak, gigabaytlarca veriyi ikizlemek ve hangisinin
// gerçek olduğu belirsiz iki kopya bırakmak demekti. Laragon'un www
// klasörü kuralına da bağlı kalmıyoruz: kayıt yalnız bir işaretçi.
//
// # Neden var olan kurulum değiştirilmiyor
//
// Göç tek yönlü ve geri alınabilir olmalı: Laragon'un yapılandırmasına
// dokunmuyoruz. Kullanıcı DevBox'ı denerken eski ortamı çalışır
// durumda kalıyor; beğenmezse geri dönebiliyor.
package migrate

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Source, göç kaynağı.
type Source string

const (
	SourceLaragon Source = "laragon"
	SourceXAMPP   Source = "xampp"
	SourceWAMP    Source = "wamp"
)

// Site, bulunan bir site.
type Site struct {
	// Name, önerilen proje adı.
	Name string `json:"name"`

	// Dir, sitenin dizini.
	Dir string `json:"dir"`

	// DocumentRoot, belge kökü (Dir'e göre göreli, boşsa Dir'in kendisi).
	DocumentRoot string `json:"documentRoot,omitempty"`

	// Domain, kaynaktaki alan adı. Boşsa <ad>.test önerilir.
	Domain string `json:"domain,omitempty"`

	// Aliases, ek alan adları.
	Aliases []string `json:"aliases,omitempty"`

	// Source, nereden bulunduğu.
	Source Source `json:"source"`

	// Framework, algılanan çerçeve. Göç sırasında dolduruluyor.
	Framework string `json:"framework,omitempty"`

	// Notes, kullanıcıya söylenecekler.
	Notes []string `json:"notes,omitempty"`
}

// Installation, bulunan bir kurulum.
type Installation struct {
	Source Source `json:"source"`
	Root   string `json:"root"`
	Sites  []Site `json:"sites"`
}

// laragonRoots, Laragon'un olağan kurulum yerleri.
func laragonRoots() []string {
	return []string{
		`C:\laragon`, `D:\laragon`, `C:\Laragon`,
	}
}

// xamppRoots, XAMPP'ın olağan kurulum yerleri.
func xamppRoots() []string {
	return []string{
		`C:\xampp`, `D:\xampp`, `/opt/lampp`, `/Applications/XAMPP/xamppfiles`,
	}
}

// wampRoots, WAMP'ın olağan kurulum yerleri.
func wampRoots() []string {
	return []string{`C:\wamp64`, `C:\wamp`}
}

// Detect, verilen köklerde kurulum arar.
//
// extra, kullanıcının elle verdiği kökler; olağan yerler bulunamazsa
// ya da kurulum başka bir diskteyse gerekiyor.
func Detect(extra ...string) []Installation {
	var found []Installation

	check := func(source Source, root string) {
		if root == "" {
			return
		}
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			return
		}
		sites, err := scan(source, root)
		if err != nil || len(sites) == 0 {
			return
		}
		found = append(found, Installation{Source: source, Root: root, Sites: sites})
	}

	for _, root := range laragonRoots() {
		check(SourceLaragon, root)
	}
	for _, root := range xamppRoots() {
		check(SourceXAMPP, root)
	}
	for _, root := range wampRoots() {
		check(SourceWAMP, root)
	}
	for _, root := range extra {
		// Elle verilen kökün türünü içeriğinden anlıyoruz.
		check(guessSource(root), root)
	}
	return found
}

// guessSource, dizin içeriğinden kurulum türünü tahmin eder.
func guessSource(root string) Source {
	if _, err := os.Stat(filepath.Join(root, "laragon.exe")); err == nil {
		return SourceLaragon
	}
	if _, err := os.Stat(filepath.Join(root, "www")); err == nil {
		return SourceLaragon
	}
	if _, err := os.Stat(filepath.Join(root, "htdocs")); err == nil {
		return SourceXAMPP
	}
	return SourceXAMPP
}

// Scan, verilen kökteki siteleri bulur.
func Scan(source Source, root string) ([]Site, error) { return scan(source, root) }

func scan(source Source, root string) ([]Site, error) {
	switch source {
	case SourceLaragon:
		return scanDirectorySites(source, root, "www")
	case SourceWAMP:
		return scanDirectorySites(source, root, "www")
	case SourceXAMPP:
		sites, err := scanDirectorySites(source, root, "htdocs")
		if err != nil {
			return nil, err
		}
		// XAMPP'ta sanal konaklar ayrı bir dosyada; oradaki adlar
		// klasör adından daha doğru.
		vhosts := parseVirtualHosts(filepath.Join(root, "apache", "conf", "extra", "httpd-vhosts.conf"))
		return mergeVirtualHosts(sites, vhosts, source), nil
	default:
		return nil, fmt.Errorf("migrate: bilinmeyen kaynak %q", source)
	}
}

// scanDirectorySites, kök altındaki her alt dizini bir site sayar.
func scanDirectorySites(source Source, root, sub string) ([]Site, error) {
	dir := filepath.Join(root, sub)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var sites []Site
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		// XAMPP'ın kendi araçları site değil.
		switch strings.ToLower(entry.Name()) {
		case "dashboard", "img", "webalizer", "xampp", "forbidden", "favicon.ico":
			continue
		}
		site := Site{
			Name:   suggestName(entry.Name()),
			Dir:    filepath.Join(dir, entry.Name()),
			Source: source,
		}
		site.DocumentRoot, site.Notes = detectDocumentRoot(site.Dir)
		sites = append(sites, site)
	}
	sort.Slice(sites, func(i, j int) bool { return sites[i].Name < sites[j].Name })
	return sites, nil
}

// detectDocumentRoot, Laragon'un "public varsa onu kullan" davranışını
// taklit eder.
//
// Laragon bunu otomatik yapıyor ve kullanıcılar buna alışkın; göçte
// aynısını yapmazsak Laravel siteleri dizin listesi gösterir.
func detectDocumentRoot(dir string) (string, []string) {
	for _, aday := range []string{"public", "public_html", "web", "htdocs"} {
		if info, err := os.Stat(filepath.Join(dir, aday)); err == nil && info.IsDir() {
			return aday, nil
		}
	}
	return "", nil
}

// vhost, sanal konak dosyasından okunan kayıt.
type vhost struct {
	DocumentRoot string
	ServerName   string
	Aliases      []string
}

var (
	vhostBlock     = regexp.MustCompile(`(?is)<VirtualHost[^>]*>(.*?)</VirtualHost>`)
	documentRootRe = regexp.MustCompile(`(?im)^\s*DocumentRoot\s+"?([^"\n\r]+?)"?\s*$`)
	serverNameRe   = regexp.MustCompile(`(?im)^\s*ServerName\s+([^\s\n\r]+)\s*$`)
	serverAliasRe  = regexp.MustCompile(`(?im)^\s*ServerAlias\s+([^\n\r]+?)\s*$`)
)

// parseVirtualHosts, Apache sanal konak dosyasını okur.
//
// Tam bir Apache ayrıştırıcısı değil: yalnız DocumentRoot, ServerName
// ve ServerAlias. Göçün ihtiyacı bu üçü; gerisini kullanıcı zaten
// devbox.yaml'da yeniden ifade edecek.
func parseVirtualHosts(path string) []vhost {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []vhost
	for _, block := range vhostBlock.FindAllStringSubmatch(string(data), -1) {
		body := block[1]
		v := vhost{}
		if m := documentRootRe.FindStringSubmatch(body); m != nil {
			v.DocumentRoot = filepath.Clean(strings.TrimSpace(m[1]))
		}
		if m := serverNameRe.FindStringSubmatch(body); m != nil {
			v.ServerName = strings.TrimSpace(m[1])
		}
		for _, m := range serverAliasRe.FindAllStringSubmatch(body, -1) {
			v.Aliases = append(v.Aliases, strings.Fields(m[1])...)
		}
		if v.DocumentRoot != "" || v.ServerName != "" {
			out = append(out, v)
		}
	}
	return out
}

// mergeVirtualHosts, sanal konak bilgisini dizin taramasıyla
// birleştirir.
func mergeVirtualHosts(sites []Site, vhosts []vhost, source Source) []Site {
	index := make(map[string]int, len(sites))
	for i, s := range sites {
		index[normalize(s.Dir)] = i
	}

	for _, v := range vhosts {
		if v.DocumentRoot == "" {
			continue
		}
		root := normalize(v.DocumentRoot)

		// Sanal konak, taranan bir siteyi mi gösteriyor?
		matched := -1
		for dir, i := range index {
			if root == dir || strings.HasPrefix(root, dir+string(filepath.Separator)) ||
				strings.HasPrefix(root, dir+"/") {
				matched = i
				break
			}
		}
		if matched >= 0 {
			if v.ServerName != "" {
				sites[matched].Domain = v.ServerName
			}
			sites[matched].Aliases = append(sites[matched].Aliases, v.Aliases...)
			// Belge kökü sanal konakta daha kesin.
			if rel, err := filepath.Rel(sites[matched].Dir, v.DocumentRoot); err == nil &&
				rel != "." && !strings.HasPrefix(rel, "..") {
				sites[matched].DocumentRoot = filepath.ToSlash(rel)
			}
			continue
		}

		// www/htdocs dışında bir dizini gösteren sanal konak: o da bir
		// site ve göçte kaybolmamalı.
		//
		// Sanal konak belge kökünü gösteriyor, proje dizinini değil.
		// "…/api/public" gören biri projenin "…/api" olduğunu bilir;
		// aynısını yapmazsak proje dizini public/ olur ve composer.json,
		// artisan gibi dosyalar algılamanın dışında kalır.
		projectDir, docRoot := splitDocumentRoot(v.DocumentRoot)

		name := suggestName(filepath.Base(projectDir))
		if v.ServerName != "" {
			name = suggestName(strings.SplitN(v.ServerName, ".", 2)[0])
		}
		site := Site{
			Name:         name,
			Dir:          projectDir,
			DocumentRoot: docRoot,
			Domain:       v.ServerName,
			Aliases:      v.Aliases,
			Source:       source,
			Notes:        []string{"Bu site www/htdocs dışında; sanal konak dosyasından bulundu."},
		}
		sites = append(sites, site)
	}

	sort.Slice(sites, func(i, j int) bool { return sites[i].Name < sites[j].Name })
	return sites
}

// documentRootNames, proje dizini değil belge kökü olan klasör adları.
var documentRootNames = map[string]bool{
	"public": true, "public_html": true, "web": true, "htdocs": true, "www": true,
}

// splitDocumentRoot, belge kökünden proje dizinini ve göreli kökü
// ayırır.
func splitDocumentRoot(docRoot string) (projectDir, relative string) {
	base := strings.ToLower(filepath.Base(docRoot))
	if !documentRootNames[base] {
		return docRoot, ""
	}
	parent := filepath.Dir(docRoot)
	if parent == "" || parent == docRoot {
		return docRoot, ""
	}
	return parent, filepath.Base(docRoot)
}

func normalize(p string) string {
	return strings.TrimRight(filepath.Clean(p), `\/`)
}

// suggestName, dizin adından geçerli bir proje adı üretir.
var invalidName = regexp.MustCompile(`[^\p{L}\p{N}_-]+`)

func suggestName(raw string) string {
	name := invalidName.ReplaceAllString(strings.TrimSpace(raw), "-")
	name = strings.Trim(name, "-")
	if name == "" {
		return "proje"
	}
	return strings.ToLower(name)
}

// DomainFor, siteye önerilecek alan adı.
//
// Laragon `.test` kullanıyor, XAMPP genellikle `.local` ya da düz ad.
// DevBox `.test` üzerine kurulu (çözücü o son eki sahipleniyor), bu
// yüzden başka bir son ek varsa değiştiriliyor ve eskisi takma ad
// olarak korunuyor — eski bağlantılar çalışmaya devam etsin.
func DomainFor(site Site) (domain string, aliases []string) {
	aliases = append(aliases, site.Aliases...)

	if site.Domain == "" {
		return site.Name + ".test", aliases
	}
	if strings.HasSuffix(site.Domain, ".test") {
		return site.Domain, aliases
	}

	base := site.Domain
	if i := strings.LastIndex(base, "."); i > 0 {
		base = base[:i]
	}
	aliases = append(aliases, site.Domain)
	return base + ".test", aliases
}

// ReadLine, bir dosyanın ilk eşleşen satırını döner (test yardımcısı
// değil; küçük yapılandırma dosyaları için).
func ReadLine(path string, match func(string) bool) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if match(scanner.Text()) {
			return scanner.Text(), true
		}
	}
	return "", false
}
