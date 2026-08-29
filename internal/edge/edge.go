// Package edge, 80 ve 443 numaralı portları tek başına dinleyip istekleri
// host adına göre arka uçlara dağıtan kenar sunucusudur.
//
// Laragon'da "Apache mı Nginx mi" seçimi mimari bir zorunluluk değil, sadece
// 80. portu tek sürecin dinleyebilmesinden kaynaklanıyor. Kenarı ayırınca bu
// kısıt ortadan kalkıyor: Apache 127.0.0.1:8080'de, Nginx 8081'de, Node
// uygulaması 3000'de durur; hangi isteğin nereye gideceğine kenar karar verir.
// Böylece .htaccess'e bağımlı bir WordPress ile Nginx isteyen bir Laravel aynı
// anda çalışabilir.
//
// TLS burada sonlandırılır: arka uçlar düz HTTP konuşur ve yalnız loopback'i
// dinler, dolayısıyla her proje için ayrı sertifika yapılandırması gerekmez.
package edge

import (
	"context"
	"crypto/tls"
	"fmt"
	"html"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// acmeChallengePrefix, HTTP'den HTTPS'e yönlendirmenin dışında tutulan tek
// yol. ACME HTTP-01 doğrulaması düz HTTP üzerinden yapılır; yönlendirirsek
// sertifika alınamaz.
const acmeChallengePrefix = "/.well-known/acme-challenge/"

// Edge, host adına göre yönlendirme yapan kenar sunucusu.
type Edge struct {
	mu       sync.RWMutex
	exact    map[string]*route
	wildcard map[string]*route // "magaza.test" → *.magaza.test

	// HTTPSPort, HTTP'den yönlendirirken kullanılacak port. 443 ise
	// URL'ye yazılmaz.
	HTTPSPort string

	Logger *slog.Logger
}

type route struct {
	host    string
	handler http.Handler
	target  string // tanılama için; ters vekil değilse boş
}

// New, boş bir kenar sunucusu oluşturur.
func New() *Edge {
	return &Edge{
		exact:     make(map[string]*route),
		wildcard:  make(map[string]*route),
		HTTPSPort: "443",
	}
}

// Handle, bir host adını doğrudan bir işleyiciye bağlar. PHP havuzu gibi
// DevBox içinde çalışan siteler için.
//
// "*.magaza.test" biçimi joker eşleşmedir ve yalnız bir seviye kapsar.
func (e *Edge) Handle(host string, h http.Handler) {
	e.add(host, &route{host: host, handler: h})
}

// Proxy, bir host adını başka bir adreste çalışan sunucuya bağlar
// (Apache, Nginx, Node, konteyner...).
func (e *Edge) Proxy(host, target string) error {
	handler, err := ProxyHandler(host, target, e.Logger)
	if err != nil {
		return err
	}
	e.add(host, &route{host: host, handler: handler, target: target})
	return nil
}

// ProxyHandler, tek bir hedefe ileten ters vekil işleyicisi döner.
//
// Yönlendirme tablosu olmadan, doğrudan bir http.Handler isteyenler için:
// bir Edge kurup içine tek yönlendirme koymak, o Edge'in kendi host
// eşleşmesini ikinci kez çalıştırması demek. Takma adlarla kullanıldığında
// dıştaki tablo isteği içtekine veriyor, içteki takma adı tanımıyor ve 404
// dönüyordu.
func ProxyHandler(host, target string, logger *slog.Logger) (http.Handler, error) {
	u, err := url.Parse(target)
	if err != nil {
		return nil, fmt.Errorf("edge: hedef adres çözülemedi: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("edge: hedef http ya da https olmalı: %q", target)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("edge: hedefte ana makine yok: %q", target)
	}

	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(u)
			// Özgün Host'u koruyoruz: Apache ve Nginx sanal sunucu
			// eşleşmesini buna göre yapar. SetURL bunu hedefin adıyla
			// değiştirdiği için elle geri alıyoruz.
			pr.Out.Host = pr.In.Host
			// X-Forwarded-For/Proto/Host: arka uçtaki çerçeve istemcinin
			// gerçek adresini ve şemasını böyle görür. SetXForwarded
			// gelen başlıkları da temizler, yani istemci bunları
			// uyduramaz.
			pr.SetXForwarded()
			if pr.In.TLS != nil {
				pr.Out.Header.Set("X-Forwarded-Proto", "https")
			}
		},
		ErrorHandler: proxyErrorHandler(host, target, logger),
	}, nil
}

func proxyErrorHandler(host, target string, logger *slog.Logger) func(http.ResponseWriter, *http.Request, error) {
	return func(w http.ResponseWriter, r *http.Request, err error) {
		if logger != nil {
			logger.Warn(fmt.Sprintf("%s → %s ulaşılamıyor: %v", host, target, err), "bileşen", "edge")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadGateway)
		// Çıplak bir 502 yerine ne olduğunu söyleyen bir sayfa: arka ucun
		// çalışmadığını anlamak için günlüğe bakmak gerekmesin.
		fmt.Fprintf(w, `<!doctype html><meta charset="utf-8">
<title>Arka uç yanıt vermiyor</title>
<h1>%s arka ucu yanıt vermiyor</h1>
<p>Kenar sunucu çalışıyor ama <code>%s</code> adresine bağlanamadı.</p>
<p>Bu adresteki servisin (Apache, Nginx, Node...) ayakta olduğunu doğrulayın.</p>
<pre>%s</pre>`, html.EscapeString(host), html.EscapeString(target), html.EscapeString(err.Error()))
	}
}

func (e *Edge) add(host string, r *route) {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	e.mu.Lock()
	defer e.mu.Unlock()
	if suffix, ok := strings.CutPrefix(host, "*."); ok {
		e.wildcard[suffix] = r
		return
	}
	e.exact[host] = r
}

// Remove, bir yönlendirmeyi kaldırır.
func (e *Edge) Remove(host string) {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	e.mu.Lock()
	defer e.mu.Unlock()
	if suffix, ok := strings.CutPrefix(host, "*."); ok {
		delete(e.wildcard, suffix)
		return
	}
	delete(e.exact, host)
}

// Hosts, tanımlı host adlarını sıralı olarak döner.
func (e *Edge) Hosts() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	hosts := make([]string, 0, len(e.exact)+len(e.wildcard))
	for h := range e.exact {
		hosts = append(hosts, h)
	}
	for h := range e.wildcard {
		hosts = append(hosts, "*."+h)
	}
	sort.Strings(hosts)
	return hosts
}

// lookup, host adına uyan yönlendirmeyi bulur.
//
// Sıra önemli: tam eşleşme jokeri geçer. admin.magaza.test için hem
// "admin.magaza.test" hem "*.magaza.test" tanımlıysa, özel olan kazanır.
func (e *Edge) lookup(host string) *route {
	host = normalizeHost(host)

	e.mu.RLock()
	defer e.mu.RUnlock()

	if r, ok := e.exact[host]; ok {
		return r
	}
	if i := strings.IndexByte(host, '.'); i >= 0 {
		if r, ok := e.wildcard[host[i+1:]]; ok {
			return r
		}
	}
	return nil
}

// normalizeHost, Host başlığını eşleşmeye hazırlar: portu, sondaki noktayı
// ve büyük harfleri atar.
func normalizeHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.TrimSuffix(host, ".")
}

func (e *Edge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r.Host = strings.TrimSuffix(r.Host, ".")
	if route := e.lookup(r.Host); route != nil {
		route.handler.ServeHTTP(w, r)
		return
	}
	e.unknownHost(w, r)
}

// unknownHost, tanımsız bir host için tanımlı siteleri listeleyen bir sayfa
// gösterir. "Neden açılmıyor" sorusunun cevabı çoğu zaman burada.
func (e *Edge) unknownHost(w http.ResponseWriter, r *http.Request) {
	hosts := e.Hosts()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)

	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8">
<title>DevBox — site tanımlı değil</title>
<h1>%s için tanımlı bir site yok</h1>`, html.EscapeString(normalizeHost(r.Host)))

	if len(hosts) == 0 {
		fmt.Fprint(w, `<p>Henüz hiçbir site tanımlanmamış.</p>`)
		return
	}
	fmt.Fprint(w, `<p>Tanımlı siteler:</p><ul>`)
	for _, h := range hosts {
		safe := html.EscapeString(h)
		if strings.HasPrefix(h, "*.") {
			fmt.Fprintf(w, `<li>%s</li>`, safe)
			continue
		}
		fmt.Fprintf(w, `<li><a href="https://%s/">%s</a></li>`, safe, safe)
	}
	fmt.Fprint(w, `</ul>`)
}

// RedirectHandler, 80. portta çalışan ve HTTPS'e yönlendiren işleyicidir.
//
// ACME doğrulama yolu yönlendirilmez: HTTP-01 sınaması düz HTTP ister,
// yönlendirirsek sertifika alınamaz.
func (e *Edge) RedirectHandler(acme http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, acmeChallengePrefix) {
			if acme != nil {
				acme.ServeHTTP(w, r)
				return
			}
			http.NotFound(w, r)
			return
		}

		host := normalizeHost(r.Host)
		if e.HTTPSPort != "" && e.HTTPSPort != "443" {
			host = net.JoinHostPort(host, e.HTTPSPort)
		}
		target := "https://" + host + r.URL.RequestURI()
		http.Redirect(w, r, target, http.StatusMovedPermanently)
	})
}

func (e *Edge) logf(format string, args ...any) {
	if e.Logger == nil {
		return
	}
	e.Logger.Warn(fmt.Sprintf(format, args...), "bileşen", "edge")
}

// Server, kenarı 80 ve 443'te çalıştırır.
type Server struct {
	Edge      *Edge
	HTTPAddr  string
	HTTPSAddr string

	// TLSConfig, sertifika sağlayıcısı (certs.Store.TLSConfig()).
	TLSConfig *tls.Config

	httpSrv  *http.Server
	httpsSrv *http.Server
}

// ListenAndServe, iki dinleyiciyi de açar ve bağlam iptal edilene kadar
// hizmet verir.
func (s *Server) ListenAndServe(ctx context.Context) error {
	s.httpSrv = &http.Server{
		Addr:              s.HTTPAddr,
		Handler:           s.Edge.RedirectHandler(nil),
		ReadHeaderTimeout: 15 * time.Second,
	}
	s.httpsSrv = &http.Server{
		Addr:              s.HTTPSAddr,
		Handler:           s.Edge,
		TLSConfig:         s.TLSConfig,
		ReadHeaderTimeout: 15 * time.Second,
	}

	errc := make(chan error, 2)
	go func() {
		if err := s.httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errc <- fmt.Errorf("edge: HTTP dinleyici: %w", err)
		}
	}()
	go func() {
		if err := s.httpsSrv.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			errc <- fmt.Errorf("edge: HTTPS dinleyici: %w", err)
		}
	}()

	select {
	case err := <-errc:
		s.Close()
		return err
	case <-ctx.Done():
		return s.Close()
	}
}

// Close, dinleyicileri kapatır.
func (s *Server) Close() error {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if s.httpSrv != nil {
		s.httpSrv.Shutdown(shutdownCtx)
	}
	if s.httpsSrv != nil {
		s.httpsSrv.Shutdown(shutdownCtx)
	}
	return nil
}
