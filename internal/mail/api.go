package mail

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Handler, yakalanan postaları gösteren HTTP arayüzü.
//
// # HTML postaları neden korumalı çerçevede
//
// Yakalanan posta güvenilmez içeriktir: uygulamanın gönderdiği HTML'de
// kullanıcıdan gelen veri olabilir. Onu doğrudan sayfaya basmak, postayı
// tetikleyen kişiye DevBox arayüzünde betik çalıştırma imkânı verir — ve o
// arayüz aynı köken üzerinden API'ye erişebilir. Bu yüzden HTML gövde ayrı
// bir uç noktadan (/api/messages/{id}/html), kendi katı güvenlik ilkesiyle ve
// sandbox'lı bir iframe içinde gösteriliyor; bkz. htmlBody.
type Handler struct {
	Store *Store

	// SMTPAddr, arayüzde gösterilecek SMTP adresi.
	SMTPAddr string
}

// ServeHTTP, yönlendirmeleri kurar.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux().ServeHTTP(w, r)
}

func (h *Handler) mux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/messages", h.list)
	mux.HandleFunc("DELETE /api/messages", h.clear)
	mux.HandleFunc("GET /api/messages/{id}", h.get)
	mux.HandleFunc("DELETE /api/messages/{id}", h.delete)
	mux.HandleFunc("GET /api/messages/{id}/raw", h.raw)
	mux.HandleFunc("GET /api/messages/{id}/html", h.htmlBody)
	mux.HandleFunc("GET /api/messages/{id}/attachments/{index}", h.attachment)
	mux.HandleFunc("GET /api/stream", h.stream)
	mux.HandleFunc("GET /api/latest", h.latest)
	mux.HandleFunc("GET /", h.index)
	return mux
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.Store.Search(r.URL.Query().Get("q")))
}

func (h *Handler) latest(w http.ResponseWriter, r *http.Request) {
	msg, ok := h.Store.Latest()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "henüz posta yok"})
		return
	}
	writeJSON(w, http.StatusOK, msg)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	msg, ok := h.Store.Get(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "posta bulunamadı"})
		return
	}
	writeJSON(w, http.StatusOK, msg)
}

func (h *Handler) raw(w http.ResponseWriter, r *http.Request) {
	msg, ok := h.Store.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(msg.Raw)
}

// htmlBody, postanın HTML gövdesini ayrı bir belge olarak sunar.
//
// Neden iframe srcdoc değil: srcdoc belgesi üst sayfanın güvenlik ilkesini
// devralır. Bizim arayüzümüz tek dosya olduğu için ilkesinde satır içi betik
// açık — yani posta HTML'i, ilkeyi devralınca yalnızca iframe'in sandbox
// özniteliğine bağlı kalıyordu. Ayrı bir yanıt olarak sunulunca gövde kendi
// katı ilkesini taşıyor: betik yok, dış kaynak yok. Başlıktaki sandbox
// yönergesi de belgeyi adressiz kökene taşıyor; bu, birisi adresi doğrudan
// adres çubuğuna yapıştırsa bile geçerli.
//
// Uzak içerik bilerek engelli: yakalanan postadaki takip pikselleri
// geliştiricinin makinesinden dışarıya istek atmasın.
func (h *Handler) htmlBody(w http.ResponseWriter, r *http.Request) {
	msg, ok := h.Store.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy",
		"sandbox; default-src 'none'; img-src data:; style-src 'unsafe-inline'; font-src data:")
	io.WriteString(w, msg.HTML)
}

func (h *Handler) attachment(w http.ResponseWriter, r *http.Request) {
	msg, ok := h.Store.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	index, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || index < 0 || index >= len(msg.Attachments) {
		http.NotFound(w, r)
		return
	}
	att := msg.Attachments[index]

	// Ek indirilirken tarayıcıda açılmasın: yakalanan bir HTML ya da SVG
	// eki, açıldığında bu köken üzerinde betik çalıştırabilir.
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=%q", safeFilename(att.Filename)))
	w.Write(att.Data)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	if !h.Store.Delete(r.PathValue("id")) {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) clear(w http.ResponseWriter, r *http.Request) {
	h.Store.Clear()
	w.WriteHeader(http.StatusNoContent)
}

// stream, yeni postaları Sunucu Gönderimli Olaylar (SSE) ile duyurur.
func (h *Handler) stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "akış desteklenmiyor", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	messages, unsubscribe := h.Store.Subscribe()
	defer unsubscribe()

	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case msg, ok := <-messages:
			if !ok {
				return
			}
			data, err := json.Marshal(msg.Summary())
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

func (h *Handler) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Kendi sayfamız için katı bir içerik güvenliği ilkesi: satır içi betik
	// ve stil kendi kaynağımızdan, dış kaynak yok.
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; "+
			"img-src 'self' data:; frame-src 'self'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	fmt.Fprintf(w, indexHTML, h.SMTPAddr)
}

// safeFilename, indirme adını temizler: yol ayracı ve tırnak içeren bir ad,
// Content-Disposition başlığını bozabilir.
func safeFilename(name string) string {
	name = strings.NewReplacer("/", "_", `\`, "_", `"`, "_", "\r", "", "\n", "").Replace(name)
	if name == "" {
		return "ek"
	}
	if len(name) > 200 {
		name = name[:200]
	}
	return name
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(body)
}
