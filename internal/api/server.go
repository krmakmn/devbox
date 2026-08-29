package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/krmakmn/devbox/internal/projects"
	"github.com/krmakmn/devbox/internal/runtime"
	"github.com/krmakmn/devbox/internal/supervisor"
)

// Version, API'nin sözleşme sürümü. İstemci uyumsuzluğu erken görülsün diye
// her yanıtta ve /v1/status'ta dönüyor.
const Version = "1"

// Server, devboxd'nin HTTP arayüzü.
type Server struct {
	token      string
	supervisor *supervisor.Supervisor
	runtimes   *runtime.Store
	projects   *projects.Runner
	logger     *slog.Logger
	startedAt  time.Time

	srv *http.Server
	ln  net.Listener
}

// Config, sunucu ayarları.
type Config struct {
	// Addr, dinlenecek adres. Boşsa 127.0.0.1:0 (işletim sistemi port seçer).
	//
	// Loopback dışına çıkmak, jetonu bilen herkese makinedeki süreçleri
	// yönetme yetkisi vermek demek. Bilerek yapılmadıkça değiştirilmemeli.
	Addr string

	// Token, istemcilerin göndermesi gereken jeton.
	Token string

	Supervisor *supervisor.Supervisor
	Runtimes   *runtime.Store

	// Projects, verilirse proje uç noktaları ve denetim paneli açılır.
	Projects *projects.Runner

	Logger *slog.Logger
}

// NewServer, sunucuyu kurar ama dinlemeye başlamaz.
func NewServer(cfg Config) (*Server, error) {
	if cfg.Token == "" {
		return nil, fmt.Errorf("api: jeton boş olamaz")
	}
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:0"
	}
	return &Server{
		token:      cfg.Token,
		supervisor: cfg.Supervisor,
		runtimes:   cfg.Runtimes,
		projects:   cfg.Projects,
		logger:     cfg.Logger,
		startedAt:  time.Now(),
	}, nil
}

// Handler, yönlendirmeleri ve ara katmanları kurar.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", s.handleStatus)
	mux.HandleFunc("GET /v1/services", s.handleServices)
	mux.HandleFunc("POST /v1/services/{name}/start", s.handleServiceStart)
	mux.HandleFunc("POST /v1/services/{name}/stop", s.handleServiceStop)
	mux.HandleFunc("GET /v1/services/{name}/logs", s.handleServiceLogs)
	mux.HandleFunc("GET /v1/services/{name}/logs/stream", s.handleServiceLogStream)
	mux.HandleFunc("GET /v1/runtimes", s.handleRuntimes)
	mux.HandleFunc("GET /v1/projects", s.handleProjects)
	mux.HandleFunc("GET /v1/projects/{name}", s.handleProject)
	mux.HandleFunc("POST /v1/projects/{name}/start", s.handleProjectStart)
	mux.HandleFunc("POST /v1/projects/{name}/stop", s.handleProjectStop)
	mux.HandleFunc("GET /v1/projects/{name}/config", s.handleProjectConfig)
	mux.HandleFunc("PUT /v1/projects/{name}/config", s.handleProjectConfigUpdate)
	mux.HandleFunc("GET /", s.handleUI)

	return s.requireLocalHost(s.requireAuth(mux))
}

// Start, dinlemeye başlar ve adresi döner.
func (s *Server) Start(addr string) (string, error) {
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", fmt.Errorf("api: dinlenemedi: %w", err)
	}
	s.ln = ln
	s.srv = &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
	}
	go s.srv.Serve(ln)
	return ln.Addr().String(), nil
}

// Close, sunucuyu kapatır.
func (s *Server) Close() error {
	if s.srv == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.srv.Shutdown(ctx)
}

// --- ara katmanlar ----------------------------------------------------------

// SessionCookie, tarayıcı oturumunu taşıyan çerezin adı.
const SessionCookie = "devbox_oturum"

// requireAuth, isteğin yetkili olduğunu doğrular.
//
// İki yol var. Komut satırı istemcisi jetonu Authorization başlığıyla
// gönderiyor. Tarayıcı gönderemiyor — bir sayfayı açarken başlık
// ekleyemezsiniz — bu yüzden denetim paneli için çerez tabanlı bir oturum
// var: "devbox ui" adresi ?jeton=... ile açıyor, sunucu çerezi kuruyor ve
// adresi jetonsuz hâline yönlendiriyor. Jeton böylece adres çubuğunda ve
// tarayıcı geçmişinde kalmıyor.
//
// # Çerezin getirdiği risk ve kapatılışı
//
// Çerez, tarayıcıdaki her sayfanın bu API'ye kimliği doğrulanmış istek
// atabilmesi demek olurdu (CSRF). Üç kat var: çerez SameSite=Strict, yani
// başka bir siteden gelen istekle gönderilmiyor; durum değiştiren
// isteklerde Origin başlığı kendi adresimiz olmak zorunda; ve Host
// denetimi zaten yerinde. Jeton başlığıyla gelen isteklere Origin şartı
// uygulanmıyor: onu gönderebilen zaten jetonu okuyabilmiş demektir.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if token, ok := strings.CutPrefix(header, "Bearer "); ok {
			if tokenMatches(s.token, strings.TrimSpace(token)) {
				next.ServeHTTP(w, r)
				return
			}
			writeError(w, http.StatusUnauthorized, "geçersiz jeton")
			return
		}

		// Adres çubuğundan gelen ilk açılış: çerezi kur, jetonu adresten
		// düşür.
		if q := r.URL.Query().Get("jeton"); q != "" && r.Method == http.MethodGet {
			if !tokenMatches(s.token, q) {
				writeError(w, http.StatusUnauthorized, "geçersiz jeton")
				return
			}
			http.SetCookie(w, &http.Cookie{
				Name:     SessionCookie,
				Value:    s.token,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteStrictMode,
			})
			target := *r.URL
			query := target.Query()
			query.Del("jeton")
			target.RawQuery = query.Encode()
			http.Redirect(w, r, target.RequestURI(), http.StatusSeeOther)
			return
		}

		cookie, err := r.Cookie(SessionCookie)
		if err != nil || !tokenMatches(s.token, cookie.Value) {
			writeError(w, http.StatusUnauthorized, "geçersiz ya da eksik jeton")
			return
		}
		if !safeOrigin(r) {
			writeError(w, http.StatusForbidden,
				"beklenmeyen Origin; durum değiştiren istek yalnız denetim panelinden gelebilir")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// safeOrigin, çerezle gelen durum değiştiren isteğin kendi sayfamızdan
// geldiğini doğrular.
func safeOrigin(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		// Tarayıcılar durum değiştiren isteklerde Origin gönderiyor;
		// yoksa isteği tarayıcı dışı bir istemci atmış demektir ve o da
		// jetonu başlıkla göndermeliydi.
		return false
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// requireLocalHost, Host başlığının loopback olduğunu doğrular.
//
// DNS yeniden bağlama saldırısında saldırgan kendi alan adını 127.0.0.1'e
// çözdürür ve kurbanın tarayıcısından bize istek attırır; tarayıcı isteği
// aynı köken saydığı için engellemez. Host başlığını denetlemek bunu keser:
// saldırganın alan adı Host'ta görünür.
func (s *Server) requireLocalHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		host = strings.Trim(strings.ToLower(host), "[]")

		if host != "127.0.0.1" && host != "localhost" && host != "::1" {
			writeError(w, http.StatusForbidden,
				fmt.Sprintf("beklenmeyen Host başlığı %q; API yalnız loopback üzerinden kullanılır", r.Host))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// --- işleyiciler ------------------------------------------------------------

// StatusResponse, /v1/status yanıtı.
type StatusResponse struct {
	APIVersion string    `json:"apiVersion"`
	StartedAt  time.Time `json:"startedAt"`
	Uptime     string    `json:"uptime"`
	Services   int       `json:"services"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	resp := StatusResponse{
		APIVersion: Version,
		StartedAt:  s.startedAt,
		Uptime:     time.Since(s.startedAt).Round(time.Second).String(),
	}
	if s.supervisor != nil {
		resp.Services = len(s.supervisor.Status())
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleServices(w http.ResponseWriter, r *http.Request) {
	if s.supervisor == nil {
		writeJSON(w, http.StatusOK, []supervisor.Status{})
		return
	}
	writeJSON(w, http.StatusOK, s.supervisor.Status())
}

func (s *Server) handleServiceStart(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.service(w, r)
	if !ok {
		return
	}
	// Başlatma, hazır olma ölçütü yüzünden uzun sürebilir; istekle birlikte
	// gelen bağlam iptal edilirse başlatmayı da bırakıyoruz.
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	if err := svc.Start(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, svc.Status())
}

func (s *Server) handleServiceStop(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.service(w, r)
	if !ok {
		return
	}
	svc.Stop()
	writeJSON(w, http.StatusOK, svc.Status())
}

func (s *Server) handleServiceLogs(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.service(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(svc.Logs().Bytes())
}

// handleServiceLogStream, günlüğü Sunucu Gönderimli Olaylar (SSE) ile akıtır.
//
// Yol haritası WebSocket diyordu; günlük akışı tek yönlü olduğu için SSE
// yetiyor ve standart kütüphaneyle yazılabiliyor. WebSocket bir bağımlılık
// getirecekti. Çift yönlü bir kanal gerektiğinde (etkileşimli konsol) konu
// yeniden açılır.
func (s *Server) handleServiceLogStream(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.service(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "akış desteklenmiyor")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	// Önce birikmiş günlük, sonra canlı akış: istemci bağlandığı anda
	// bağlamı görsün.
	for _, line := range strings.Split(string(svc.Logs().Bytes()), "\n") {
		if line != "" {
			writeSSE(w, line)
		}
	}
	flusher.Flush()

	lines, unsubscribe := svc.Logs().Subscribe()
	defer unsubscribe()

	// Aracı sunucular ve tarayıcılar sessiz bağlantıları kapatır; düzenli
	// yorum satırı bağlantıyı canlı tutuyor.
	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case line, ok := <-lines:
			if !ok {
				return
			}
			for _, l := range strings.Split(strings.TrimRight(string(line), "\n"), "\n") {
				writeSSE(w, l)
			}
			flusher.Flush()
		}
	}
}

// writeSSE, tek bir olay satırı yazar.
func writeSSE(w http.ResponseWriter, line string) {
	// SSE'de satır sonu olayı bitirir; gövdedeki her satır ayrı "data:"
	// alanı olmak zorunda.
	fmt.Fprintf(w, "data: %s\n\n", line)
}

// RuntimeInfo, /v1/runtimes yanıtındaki tek kayıt.
type RuntimeInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Dir     string `json:"dir"`
}

func (s *Server) handleRuntimes(w http.ResponseWriter, r *http.Request) {
	if s.runtimes == nil {
		writeJSON(w, http.StatusOK, []RuntimeInfo{})
		return
	}
	installed, err := s.runtimes.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]RuntimeInfo, 0, len(installed))
	for _, inst := range installed {
		out = append(out, RuntimeInfo{
			Name:    inst.Package.Name,
			Version: inst.Package.Version,
			Dir:     inst.Dir,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// service, yoldaki addan servisi bulur; bulamazsa 404 yazıp false döner.
func (s *Server) service(w http.ResponseWriter, r *http.Request) (*supervisor.Service, bool) {
	name := r.PathValue("name")
	if s.supervisor == nil {
		writeError(w, http.StatusNotFound, "servis yok")
		return nil, false
	}
	svc, ok := s.supervisor.Get(name)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("%q diye bir servis yok", name))
		return nil, false
	}
	return svc, true
}

// --- yanıt yardımcıları -----------------------------------------------------

// ErrorResponse, hata yanıtlarının gövdesi.
type ErrorResponse struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Devbox-Api", Version)
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, ErrorResponse{Error: msg})
}

// --- proje uç noktaları -----------------------------------------------------

func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	if s.projects == nil {
		writeJSON(w, http.StatusOK, []projects.Status{})
		return
	}
	list, err := s.projects.Statuses()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleProject(w http.ResponseWriter, r *http.Request) {
	if s.projects == nil {
		writeError(w, http.StatusNotFound, "proje yok")
		return
	}
	st, err := s.projects.Status(r.PathValue("name"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleProjectStart(w http.ResponseWriter, r *http.Request) {
	if s.projects == nil {
		writeError(w, http.StatusNotFound, "proje yok")
		return
	}
	// Başlatma hazır olma ölçütü yüzünden uzun sürebilir; isteğin bağlamı
	// iptal edilirse başlatmayı da bırakıyoruz.
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()

	st, err := s.projects.Start(ctx, r.PathValue("name"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleProjectStop(w http.ResponseWriter, r *http.Request) {
	if s.projects == nil {
		writeError(w, http.StatusNotFound, "proje yok")
		return
	}
	st, err := s.projects.Stop(r.PathValue("name"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// handleUI, denetim panelini sunar.
func (s *Server) handleUI(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		writeError(w, http.StatusNotFound, "bulunamadı: "+r.URL.Path)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Panel tek dosya: dış kaynak yok, satır içi betik ve stil var.
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; "+
			"img-src 'self' data:; frame-src 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	io.WriteString(w, panelHTML)
}
