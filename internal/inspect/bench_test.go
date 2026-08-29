package inspect

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Denetleyici kenarın en dışında duruyor; kapalıyken maliyeti tek bir
// bayrak okuması olmalı. Bu iki ölçüm arasındaki fark, "öntanımlı açık"
// kararının bedelini gösteriyor.
func BenchmarkRecorderDisabled(b *testing.B) {
	r := NewRecorder(0, 0)
	benchRecorder(b, r)
}

func BenchmarkRecorderEnabled(b *testing.B) {
	r := NewRecorder(0, 0)
	r.SetEnabled(true)
	benchRecorder(b, r)
}

func benchRecorder(b *testing.B, r *Recorder) {
	handler := r.Middleware(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		io.WriteString(w, `{"tamam":true,"veri":[1,2,3,4,5]}`)
	}))
	req := httptest.NewRequest("GET", "http://magaza.test/api/urunler", nil)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}
}
