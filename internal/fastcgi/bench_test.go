package fastcgi

import (
	"testing"
)

// FastCGI kayıt çerçeveleme her PHP isteğinde onlarca kez çalışıyor.
func BenchmarkWriteRecord(b *testing.B) {
	w := &countingWriter{}
	payload := make([]byte, 1024)

	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := writeRecord(w, typeStdin, payload); err != nil {
			b.Fatal(err)
		}
	}
}

// countingWriter, yazılanı sayar ama tutmaz.
type countingWriter struct{ n int64 }

func (w *countingWriter) Write(p []byte) (int, error) {
	w.n += int64(len(p))
	return len(p), nil
}
