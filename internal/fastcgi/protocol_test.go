package fastcgi

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestParamsRoundTrip(t *testing.T) {
	// 127 baytı aşan değer, 4 baytlık uzunluk kodlamasını zorlar.
	long := strings.Repeat("x", 5000)
	in := map[string]string{
		"REQUEST_METHOD":         "POST",
		"SCRIPT_FILENAME":        `C:\projeler\magaza\public\index.php`,
		"QUERY_STRING":           "",
		"HTTP_COOKIE":            long,
		strings.Repeat("K", 200): "uzun-isim",
	}

	out, err := decodeParams(encodeParams(in))
	if err != nil {
		t.Fatalf("decodeParams: %v", err)
	}
	if len(out) != len(in) {
		t.Fatalf("çift sayısı %d, beklenen %d", len(out), len(in))
	}
	for k, v := range in {
		if out[k] != v {
			t.Errorf("%q = %q, beklenen %q", k, out[k], v)
		}
	}
}

func TestEncodeParamsDeterministic(t *testing.T) {
	in := map[string]string{"B": "2", "A": "1", "C": "3"}
	first := encodeParams(in)
	for i := 0; i < 20; i++ {
		if !bytes.Equal(first, encodeParams(in)) {
			t.Fatal("encodeParams aynı girdi için farklı çıktı üretti")
		}
	}
}

func TestRecordPaddingAndRoundTrip(t *testing.T) {
	cases := []int{0, 1, 7, 8, 9, 63, 1024, maxRecordBody}
	for _, size := range cases {
		body := bytes.Repeat([]byte{0xAB}, size)
		var buf bytes.Buffer
		if err := writeRecord(&buf, typeStdin, body); err != nil {
			t.Fatalf("size=%d writeRecord: %v", size, err)
		}
		// Kayıt uzunluğu 8'in katı olmalı: başlık 8 + gövde + dolgu.
		if buf.Len()%8 != 0 {
			t.Errorf("size=%d: kayıt %d bayt, 8'in katı değil", size, buf.Len())
		}
		rec, err := readRecord(&buf)
		if err != nil {
			t.Fatalf("size=%d readRecord: %v", size, err)
		}
		if rec.Type != typeStdin || rec.RequestID != requestID {
			t.Errorf("size=%d: tür/istek kimliği yanlış: %d/%d", size, rec.Type, rec.RequestID)
		}
		if !bytes.Equal(rec.Body, body) {
			t.Errorf("size=%d: gövde bozuldu", size)
		}
		if buf.Len() != 0 {
			t.Errorf("size=%d: dolgu tüketilmedi, %d bayt kaldı", size, buf.Len())
		}
	}
}

func TestWriteRecordRejectsOversizedBody(t *testing.T) {
	err := writeRecord(io.Discard, typeStdin, make([]byte, maxRecordBody+1))
	if err != errRecordTooLarge {
		t.Fatalf("hata = %v, beklenen errRecordTooLarge", err)
	}
}

func TestCopyStreamSplitsAndTerminates(t *testing.T) {
	// 64 KiB sınırının iki katından biraz fazlası: 3 dolu kayıt + boş kayıt.
	payload := bytes.Repeat([]byte("z"), 2*maxRecordBody+13)
	var buf bytes.Buffer
	if err := copyStream(&buf, typeStdin, bytes.NewReader(payload)); err != nil {
		t.Fatalf("copyStream: %v", err)
	}

	var got []byte
	var records int
	for buf.Len() > 0 {
		rec, err := readRecord(&buf)
		if err != nil {
			t.Fatalf("readRecord: %v", err)
		}
		records++
		if len(rec.Body) == 0 {
			break
		}
		got = append(got, rec.Body...)
	}
	if records != 4 {
		t.Errorf("kayıt sayısı %d, beklenen 4 (3 dolu + 1 akış sonu)", records)
	}
	if !bytes.Equal(got, payload) {
		t.Error("bölünüp birleştirilen gövde özgün veriyle aynı değil")
	}
	if buf.Len() != 0 {
		t.Errorf("akış sonu kaydından sonra %d bayt arttı", buf.Len())
	}
}

func TestCopyStreamNilReaderWritesEmptyRecord(t *testing.T) {
	var buf bytes.Buffer
	if err := copyStream(&buf, typeStdin, nil); err != nil {
		t.Fatalf("copyStream: %v", err)
	}
	rec, err := readRecord(&buf)
	if err != nil {
		t.Fatalf("readRecord: %v", err)
	}
	if len(rec.Body) != 0 {
		t.Errorf("gövde %d bayt, beklenen 0", len(rec.Body))
	}
}

func TestParseStatusLine(t *testing.T) {
	ok := map[string]int{"200": 200, "404 Not Found": 404, "500 Internal Server Error": 500}
	for in, want := range ok {
		got, err := parseStatusLine(in)
		if err != nil || got != want {
			t.Errorf("parseStatusLine(%q) = %d, %v; beklenen %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "abc", "99", "600", "20x"} {
		if _, err := parseStatusLine(in); err == nil {
			t.Errorf("parseStatusLine(%q) hata vermedi", in)
		}
	}
}
