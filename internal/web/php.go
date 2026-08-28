// Package web, HTTP isteklerini FastCGI isteklerine çevirir.
//
// Görünürde basit bir iş: URL'yi diskteki bir .php dosyasına eşle, CGI
// değişkenlerini doldur, yanıtı geri aktar. Tuzaklar ayrıntıda:
//
//   - SCRIPT_FILENAME'e var olmayan bir yol geçirmek, php-cgi'nin
//     cgi.fix_pathinfo davranışıyla birleşince klasik uzaktan kod çalıştırma
//     açığını doğurur (/yuklenen.jpg/x.php). Bu yüzden yalnız diskte gerçekten
//     var olan, düzenli bir .php dosyası çalıştırılır.
//   - Gelen "Proxy" başlığını HTTP_PROXY olarak aktarmak httpoxy açığıdır
//     (CVE-2016-5385): PHP kütüphaneleri o değişkeni giden isteklerde vekil
//     sunucu diye kullanır. Bu başlık aktarılmaz.
//   - Nokta ile başlayan yolları statik sunmak .env ve .git sızdırır.
package web

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/krmakmn/devbox/internal/fastcgi"
)

// Requester, isteği çalıştıracak FastCGI kaynağıdır (pratikte *phppool.Pool).
type Requester interface {
	Do(ctx context.Context, params map[string]string, stdin io.Reader) (*fastcgi.Response, error)
}

// Handler, bir siteyi PHP üzerinden sunar.
type Handler struct {
	// Pool, isteklerin gönderileceği php-cgi havuzu.
	Pool Requester

	// DocumentRoot, sitenin kök dizini (mutlak yol). Laravel'de public/.
	DocumentRoot string

	// FrontController, diskte karşılığı olmayan yolların yönlendirileceği
	// betik. Boşsa "index.php". Çerçeve yönlendiricileri bunu bekler.
	FrontController string

	// ServerName, SERVER_NAME değişkeni ve varsayılan Host.
	ServerName string

	// ServerPort, SERVER_PORT değişkeni.
	ServerPort string

	// HTTPS, istek TLS ile geldiyse true. HTTPS=on değişkenini belirler;
	// çerçeveler mutlak URL üretirken buna bakar.
	HTTPS bool

	// SoftwareName, SERVER_SOFTWARE değeri.
	SoftwareName string

	Logger *slog.Logger
}

func (h *Handler) frontController() string {
	if h.FrontController == "" {
		return "index.php"
	}
	return h.FrontController
}

func (h *Handler) software() string {
	if h.SoftwareName == "" {
		return "DevBox"
	}
	return h.SoftwareName
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	root, err := filepath.Abs(h.DocumentRoot)
	if err != nil {
		h.fail(w, r, http.StatusInternalServerError, fmt.Errorf("belge kökü çözülemedi: %w", err))
		return
	}

	tgt, err := resolveTarget(root, r.URL.Path, h.frontController())
	switch {
	case err == errForbiddenPath:
		http.Error(w, "403 erişim engellendi", http.StatusForbidden)
		return
	case err == errNotFound:
		http.NotFound(w, r)
		return
	case err != nil:
		h.fail(w, r, http.StatusInternalServerError, err)
		return
	}

	if tgt.staticFile != "" {
		http.ServeFile(w, r, tgt.staticFile)
		return
	}

	h.runPHP(w, r, root, tgt)
}

func (h *Handler) runPHP(w http.ResponseWriter, r *http.Request, root string, tgt target) {
	params := h.buildParams(r, root, tgt)

	resp, err := h.Pool.Do(r.Context(), params, r.Body)
	if err != nil {
		h.fail(w, r, http.StatusBadGateway, err)
		return
	}
	defer resp.Body.Close()

	dst := w.Header()
	for k, vs := range resp.Header {
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	if _, err := io.Copy(w, resp.Body); err != nil {
		h.logf("yanıt gövdesi aktarılamadı: %v", err)
	}
	if stderr := resp.Stderr(); len(stderr) > 0 {
		h.logf("PHP stderr (%s): %s", tgt.scriptName, strings.TrimSpace(string(stderr)))
	}
}

// buildParams, CGI/1.1 değişkenlerini üretir (RFC 3875).
func (h *Handler) buildParams(r *http.Request, root string, tgt target) map[string]string {
	remoteHost, remotePort := splitHostPort(r.RemoteAddr)

	params := map[string]string{
		"GATEWAY_INTERFACE": "CGI/1.1",
		"SERVER_SOFTWARE":   h.software(),
		"SERVER_PROTOCOL":   r.Proto,
		"REQUEST_METHOD":    r.Method,
		"REQUEST_URI":       r.URL.RequestURI(),
		"QUERY_STRING":      r.URL.RawQuery,
		"DOCUMENT_ROOT":     root,
		"DOCUMENT_URI":      tgt.scriptName,
		"SCRIPT_NAME":       tgt.scriptName,
		"SCRIPT_FILENAME":   tgt.scriptFile,
		"REMOTE_ADDR":       remoteHost,
		"REMOTE_PORT":       remotePort,
		"SERVER_NAME":       h.serverName(r),
		"SERVER_PORT":       h.ServerPort,
		"REQUEST_SCHEME":    schemeOf(h.HTTPS),
	}

	if tgt.pathInfo != "" {
		params["PATH_INFO"] = tgt.pathInfo
		params["PATH_TRANSLATED"] = filepath.Join(root, filepath.FromSlash(tgt.pathInfo))
	}
	if h.HTTPS {
		params["HTTPS"] = "on"
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		params["CONTENT_TYPE"] = ct
	}
	if r.ContentLength >= 0 {
		params["CONTENT_LENGTH"] = fmt.Sprint(r.ContentLength)
	}

	for name, values := range r.Header {
		key := httpParamName(name)
		if key == "" {
			continue
		}
		// Aynı başlığın birden çok değeri virgülle birleşir (RFC 3875 §4.1.18).
		params[key] = strings.Join(values, ", ")
	}
	return params
}

// httpParamName, HTTP başlığını CGI değişken adına çevirir; aktarılmaması
// gereken başlıklar için boş döner.
func httpParamName(name string) string {
	switch http.CanonicalHeaderKey(name) {
	case "Proxy":
		// httpoxy (CVE-2016-5385): HTTP_PROXY'yi dışarıdan belirlenebilir
		// yapmak, PHP'nin giden isteklerini saldırganın vekiline yönlendirir.
		return ""
	case "Content-Type", "Content-Length":
		// Bunların kendi CGI değişkenleri var; ikinci kez HTTP_ önekiyle
		// göndermek bazı çerçevelerde çift sayıma yol açıyor.
		return ""
	}
	return "HTTP_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
}

func (h *Handler) serverName(r *http.Request) string {
	if h.ServerName != "" {
		return h.ServerName
	}
	host, _ := splitHostPort(r.Host)
	return host
}

func schemeOf(https bool) string {
	if https {
		return "https"
	}
	return "http"
}

func splitHostPort(addr string) (host, port string) {
	if addr == "" {
		return "", ""
	}
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		return addr, ""
	}
	return h, p
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, code int, err error) {
	h.logf("%s %s: %v", r.Method, r.URL.Path, err)
	http.Error(w, http.StatusText(code), code)
}

func (h *Handler) logf(format string, args ...any) {
	if h.Logger == nil {
		return
	}
	h.Logger.Error(fmt.Sprintf(format, args...))
}

// --- yol çözümleme ----------------------------------------------------------

var (
	errForbiddenPath = fmt.Errorf("web: yol yasak")
	errNotFound      = fmt.Errorf("web: bulunamadı")
)

// target, bir isteğin diskteki karşılığıdır.
type target struct {
	scriptFile string // çalıştırılacak .php dosyasının disk yolu
	scriptName string // aynı betiğin URL yolu (SCRIPT_NAME)
	pathInfo   string // betikten sonra kalan URL parçası
	staticFile string // PHP değil, doğrudan sunulacak dosya
}

// resolveTarget, URL yolunu diskteki bir dosyaya eşler.
//
// Sıra nginx'in yaygın "try_files $uri $uri/ /index.php" düzeniyle aynıdır:
// yolda bir .php varsa o betik, yoksa diskte karşılığı olan dosya, o da yoksa
// ön denetleyici.
func resolveTarget(root, urlPath, frontController string) (target, error) {
	if strings.ContainsRune(urlPath, 0) {
		return target{}, errForbiddenPath
	}
	if urlPath == "" {
		urlPath = "/"
	}
	// path.Clean ".." parçalarını çözer; kök dışına çıkan bir yol kalmaz.
	clean := path.Clean("/" + strings.TrimPrefix(urlPath, "/"))

	if hasHiddenSegment(clean) {
		// .env, .git, .htaccess... Bunları sunmak sır sızdırmaktır.
		// .well-known ACME doğrulaması için gerekli, o hariç.
		return target{}, errForbiddenPath
	}

	// 1) Yolda .php varsa: ilk .php parçası betiktir, gerisi PATH_INFO.
	if scriptName, pathInfo, ok := splitScriptPath(clean); ok {
		file, err := safeJoin(root, scriptName)
		if err != nil {
			return target{}, err
		}
		if !isRegularFile(file) {
			// Var olmayan bir betiği SCRIPT_FILENAME yapmak, php-cgi'nin
			// yolu kendi başına yeniden yorumlamasına kapı açar.
			return target{}, errNotFound
		}
		return target{scriptFile: file, scriptName: scriptName, pathInfo: pathInfo}, nil
	}

	// 2) Diskte birebir karşılığı olan bir dosya varsa statik sun.
	file, err := safeJoin(root, clean)
	if err != nil {
		return target{}, err
	}
	if isRegularFile(file) {
		return target{staticFile: file}, nil
	}

	// 3) Dizin ise içindeki ön denetleyiciye bak.
	if isDir(file) {
		indexName := path.Join(clean, frontController)
		indexFile, err := safeJoin(root, indexName)
		if err != nil {
			return target{}, err
		}
		if isRegularFile(indexFile) {
			return target{scriptFile: indexFile, scriptName: indexName}, nil
		}
	}

	// 4) Kalan her şey ön denetleyiciye gider; 404'ü çerçeve üretsin.
	frontName := "/" + strings.TrimPrefix(frontController, "/")
	frontFile, err := safeJoin(root, frontName)
	if err != nil {
		return target{}, err
	}
	if !isRegularFile(frontFile) {
		return target{}, errNotFound
	}
	pathInfo := ""
	if clean != "/" {
		pathInfo = clean
	}
	return target{scriptFile: frontFile, scriptName: frontName, pathInfo: pathInfo}, nil
}

// splitScriptPath, yoldaki ilk .php parçasını bulur.
func splitScriptPath(clean string) (scriptName, pathInfo string, ok bool) {
	rest := strings.TrimPrefix(clean, "/")
	if rest == "" {
		return "", "", false
	}
	segments := strings.Split(rest, "/")
	for i, seg := range segments {
		if strings.HasSuffix(strings.ToLower(seg), ".php") {
			scriptName = "/" + strings.Join(segments[:i+1], "/")
			if i+1 < len(segments) {
				pathInfo = "/" + strings.Join(segments[i+1:], "/")
			}
			return scriptName, pathInfo, true
		}
	}
	return "", "", false
}

func hasHiddenSegment(clean string) bool {
	for _, seg := range strings.Split(strings.TrimPrefix(clean, "/"), "/") {
		if seg == ".well-known" {
			continue
		}
		if strings.HasPrefix(seg, ".") && seg != "" {
			return true
		}
	}
	return false
}

// safeJoin, URL yolunu kök dizine ekler ve sonucun kökün içinde kaldığını
// doğrular. URL zaten temizlenmiş olsa da bu denetim ikinci savunma hattıdır.
func safeJoin(root, urlPath string) (string, error) {
	joined := filepath.Join(root, filepath.FromSlash(urlPath))
	if joined != root && !strings.HasPrefix(joined, root+string(filepath.Separator)) {
		return "", errForbiddenPath
	}
	return joined, nil
}

func isRegularFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}

func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}
