package edge

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Kenar vekili her isteğin geçtiği yer: buradaki maliyet her isteğe
// ekleniyor. Ölçüm, bir değişikliğin yönlendirmeyi yavaşlatıp
// yavaşlatmadığını söylüyor.
func BenchmarkEdgeRouting(b *testing.B) {
	e := New()
	// Gerçekçi bir yük: on projeli bir makine.
	for _, host := range []string{
		"magaza.test", "blog.test", "api.test", "forum.test", "dukkan.test",
		"panel.test", "admin.test", "cdn.test", "arama.test", "odeme.test",
	} {
		e.Handle(host, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, "tamam")
		}))
	}
	e.Handle("*.magaza.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	req := httptest.NewRequest("GET", "http://odeme.test/sepet", nil)
	req.Host = "odeme.test"

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
	}
}

// Joker eşleşmesi tam eşleşmeden sonra deneniyor; maliyeti ayrı
// ölçülüyor.
func BenchmarkEdgeWildcardRouting(b *testing.B) {
	e := New()
	e.Handle("*.magaza.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	req := httptest.NewRequest("GET", "http://alt.magaza.test/", nil)
	req.Host = "alt.magaza.test"

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
	}
}
