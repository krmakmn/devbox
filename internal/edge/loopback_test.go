package edge

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Kenar 80/443'ü tüm arayüzlerde dinliyor; posta kutusu ve denetleyici
// bunun arkasında ağa açılmamalı.
func TestLoopbackOnlyRejectsRemoteClients(t *testing.T) {
	handler := LoopbackOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("gizli"))
	}))

	cases := map[string]bool{
		"127.0.0.1:5000":    true,
		"[::1]:5000":        true,
		"127.0.0.53:5000":   true,
		"192.168.1.42:5000": false,
		"10.0.0.7:5000":     false,
		"[2001:db8::1]:443": false,
		"bozuk":             false,
	}
	for remote, izinli := range cases {
		req := httptest.NewRequest("GET", "http://mail.magaza.test/", nil)
		req.RemoteAddr = remote
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if izinli && rec.Code != http.StatusOK {
			t.Errorf("%s reddedildi (%d)", remote, rec.Code)
		}
		if !izinli {
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s kabul edildi (%d)", remote, rec.Code)
			}
			if rec.Body.String() == "gizli" {
				t.Errorf("%s içeriğe ulaştı", remote)
			}
		}
	}
}

// Host başlığıyla karar vermek atlatılabilirdi; karar soketin karşı
// ucuna dayanmalı.
func TestLoopbackOnlyIgnoresSpoofedHostHeader(t *testing.T) {
	handler := LoopbackOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("gizli"))
	}))
	req := httptest.NewRequest("GET", "http://mail.magaza.test/", nil)
	req.Host = "127.0.0.1"
	req.Header.Set("X-Forwarded-For", "127.0.0.1")
	req.RemoteAddr = "192.168.1.42:5000"

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("sahte başlıklarla geçildi: %d", rec.Code)
	}
}
