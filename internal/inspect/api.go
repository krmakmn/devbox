package inspect

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Handler, denetleyicinin HTTP arayüzü.
//
// Kenarın üstünde ayrı bir konakta (inspect.<alan-adı>) duruyor: posta
// kutusunda olduğu gibi, ek bir port ve ek bir sertifika gerektirmiyor.
type Handler struct {
	Recorder *Recorder

	// EdgeAddr, tekrar isteğinin gönderileceği adres.
	EdgeAddr string

	// TLSConfig, kendi kökümüze güvenen istemci ayarı.
	TLSConfig *tls.Config

	// Domain, arayüzde gösterilecek proje alan adı.
	Domain string
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux().ServeHTTP(w, r)
}

func (h *Handler) mux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/exchanges", h.list)
	mux.HandleFunc("DELETE /api/exchanges", h.clear)
	mux.HandleFunc("GET /api/exchanges/{id}", h.get)
	mux.HandleFunc("POST /api/exchanges/{id}/replay", h.replay)
	mux.HandleFunc("GET /api/stream", h.stream)
	mux.HandleFunc("GET /api/state", h.state)
	mux.HandleFunc("POST /api/state", h.setState)
	mux.HandleFunc("GET /", h.index)
	return mux
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.Recorder.List())
}

func (h *Handler) clear(w http.ResponseWriter, r *http.Request) {
	h.Recorder.Clear()
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	ex, ok := h.Recorder.Get(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "kayıt bulunamadı"})
		return
	}
	writeJSON(w, http.StatusOK, ex)
}

func (h *Handler) state(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": h.Recorder.Enabled(),
		"count":   h.Recorder.Count(),
		"domain":  h.Domain,
	})
}

func (h *Handler) setState(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "gövde çözülemedi"})
		return
	}
	h.Recorder.SetEnabled(payload.Enabled)
	h.state(w, r)
}

func (h *Handler) replay(w http.ResponseWriter, r *http.Request) {
	ex, ok := h.Recorder.Get(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "kayıt bulunamadı"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()

	result, err := Replay(ctx, ex, h.EdgeAddr, h.TLSConfig)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// stream, yeni kayıtları SSE ile duyurur.
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

	items, unsubscribe := h.Recorder.Subscribe()
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
		case item, ok := <-items:
			if !ok {
				return
			}
			data, err := json.Marshal(item)
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
	// Kaydedilen gövdeler güvenilmez: incelenen uygulamanın ürettiği
	// HTML olabilir. Arayüz onları hiçbir zaman HTML olarak basmıyor,
	// yalnız metin olarak gösteriyor; ilke de bunu pekiştiriyor.
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; "+
			"img-src 'self' data:; frame-src 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	io.WriteString(w, indexHTML)
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(body)
}
