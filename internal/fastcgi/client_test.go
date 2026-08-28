package fastcgi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/fcgi"
	"strings"
	"testing"
	"time"
)

// serveFCGI, standart kütüphanenin FastCGI sunucu tarafını (yani bir
// "uygulama"yı) ayağa kaldırır ve adresini döner.
//
// Böylece istemcimiz, bizden bağımsız yazılmış gerçek bir protokol
// uygulamasına karşı sınanmış olur. php-cgi burada yok ama tel üzerindeki
// protokol birebir aynı.
func serveFCGI(t *testing.T, h http.Handler) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("dinleyici açılamadı: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go fcgi.Serve(ln, h)
	return ln.Addr().String()
}

func baseParams(method, uri string) map[string]string {
	return map[string]string{
		"REQUEST_METHOD":  method,
		"SERVER_PROTOCOL": "HTTP/1.1",
		"REQUEST_URI":     uri,
		"SCRIPT_NAME":     "/index.php",
		"QUERY_STRING":    "",
		"HTTP_HOST":       "magaza.test",
		"CONTENT_LENGTH":  "0",
	}
}

func doRequest(t *testing.T, addr string, params map[string]string, stdin io.Reader) (*Response, []byte) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("bağlanılamadı: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	resp, err := Do(ctx, conn, params, stdin)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("gövde okunamadı: %v", err)
	}
	return resp, body
}

func TestDoSimpleGet(t *testing.T) {
	addr := serveFCGI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "yöntem=%s sorgu=%s", r.Method, r.URL.RawQuery)
	}))

	params := baseParams("GET", "/index.php?a=1&b=2")
	params["QUERY_STRING"] = "a=1&b=2"
	resp, body := doRequest(t, addr, params, nil)

	if resp.StatusCode != 200 {
		t.Errorf("durum %d, beklenen 200", resp.StatusCode)
	}
	if got := string(body); got != "yöntem=GET sorgu=a=1&b=2" {
		t.Errorf("gövde = %q", got)
	}
}

func TestDoStatusAndHeaders(t *testing.T) {
	addr := serveFCGI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Devbox", "deneme")
		w.Header().Add("Set-Cookie", "a=1")
		w.Header().Add("Set-Cookie", "b=2")
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, "bulunamadı")
	}))

	resp, body := doRequest(t, addr, baseParams("GET", "/yok.php"), nil)

	if resp.StatusCode != 404 {
		t.Errorf("durum %d, beklenen 404", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Devbox"); got != "deneme" {
		t.Errorf("X-Devbox = %q", got)
	}
	if got := resp.Header.Values("Set-Cookie"); len(got) != 2 {
		t.Errorf("Set-Cookie sayısı %d, beklenen 2: %v", len(got), got)
	}
	// Status başlığı HTTP yanıtına sızmamalı; o bir CGI ayrıntısı.
	if resp.Header.Get("Status") != "" {
		t.Error("Status başlığı temizlenmemiş")
	}
	if string(body) != "bulunamadı" {
		t.Errorf("gövde = %q", body)
	}
}

func TestDoImplicitRedirectStatus(t *testing.T) {
	// CGI'de Status yoksa ama Location varsa 302 (RFC 3875 §6.2.3).
	addr := serveFCGI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/giris")
		w.WriteHeader(http.StatusFound)
	}))
	resp, _ := doRequest(t, addr, baseParams("GET", "/"), nil)
	if resp.StatusCode != 302 {
		t.Errorf("durum %d, beklenen 302", resp.StatusCode)
	}
}

func TestDoLargePostBody(t *testing.T) {
	// 64 KiB'lık kayıt sınırının üstünde bir gövde: hem STDIN bölmesini
	// hem de "yazarken aynı anda okuma" kilitlenme korumasını sınar.
	payload := bytes.Repeat([]byte("aBcD"), 200*1024) // 800 KiB

	addr := serveFCGI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		fmt.Fprintf(w, "%d", len(got))
		if !bytes.Equal(got, payload) {
			io.WriteString(w, " BOZUK")
		}
	}))

	params := baseParams("POST", "/yukle.php")
	params["CONTENT_LENGTH"] = fmt.Sprint(len(payload))
	params["CONTENT_TYPE"] = "application/octet-stream"

	_, body := doRequest(t, addr, params, bytes.NewReader(payload))
	if got, want := string(body), fmt.Sprint(len(payload)); got != want {
		t.Errorf("uygulamanın aldığı gövde = %q, beklenen %q", got, want)
	}
}

func TestDoLargeResponseBody(t *testing.T) {
	payload := bytes.Repeat([]byte("q"), 3*maxRecordBody+7)
	addr := serveFCGI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(payload)
	}))
	_, body := doRequest(t, addr, baseParams("GET", "/buyuk.php"), nil)
	if !bytes.Equal(body, payload) {
		t.Errorf("gövde uzunluğu %d, beklenen %d", len(body), len(payload))
	}
}

// serveScripted, isteği okuyup elle yazılmış bir yanıt döndüren asgari bir
// FastCGI uygulamasıdır. Standart kütüphanenin sunucu tarafı STDERR akışına
// yazmayı dışarı açmadığı için, o akışı ancak böyle sınayabiliyoruz.
func serveScripted(t *testing.T, reply func(w io.Writer)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("dinleyici açılamadı: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		br := bufio.NewReader(conn)
		// İsteği STDIN akışı bitene kadar tüket.
		for {
			rec, err := readRecord(br)
			if err != nil {
				return
			}
			if rec.Type == typeStdin && len(rec.Body) == 0 {
				break
			}
		}
		reply(conn)
	}()
	return ln.Addr().String()
}

func endRequest(appStatus uint32, protocolStatus uint8) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint32(b[0:4], appStatus)
	b[4] = protocolStatus
	return b
}

func TestDoCapturesStderr(t *testing.T) {
	// PHP'nin uyarıları ve ölümcül hataları STDERR'e düşer; onları yutmak
	// hata ayıklamayı imkânsız hâle getirir.
	const warning = "PHP Warning: bir şey ters gitti in index.php on line 12"
	addr := serveScripted(t, func(w io.Writer) {
		writeRecord(w, typeStderr, []byte(warning))
		writeStream(w, typeStdout, []byte("Content-Type: text/plain\r\n\r\ntamam"))
		writeRecord(w, typeEndRequest, endRequest(0, statusRequestComplete))
	})

	resp, body := doRequest(t, addr, baseParams("GET", "/uyari.php"), nil)
	if string(body) != "tamam" {
		t.Errorf("gövde = %q", body)
	}
	if got := string(resp.Stderr()); !strings.Contains(got, "ters gitti") {
		t.Errorf("STDERR yakalanmadı: %q", got)
	}
}

func TestDoReportsProtocolStatus(t *testing.T) {
	// Uygulama "aşırı yüklüyüm" derse bunu sessizce boş gövdeye çevirmemeliyiz.
	addr := serveScripted(t, func(w io.Writer) {
		writeStream(w, typeStdout, []byte("Content-Type: text/plain\r\n\r\n"))
		writeRecord(w, typeEndRequest, endRequest(0, statusOverloaded))
	})

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := Do(context.Background(), conn, baseParams("GET", "/"), nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if _, err := io.ReadAll(resp.Body); err == nil {
		t.Fatal("aşırı yük durumu hata olarak yüzeye çıkmadı")
	}
}

func TestDoApplicationClosesWithoutResponse(t *testing.T) {
	// php-cgi çöktüğünde tipik davranış: bağlantı yanıt üretmeden kapanır.
	// Beklenen: makul bir hata, kilitlenme değil.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			c.Close()
		}
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := Do(ctx, conn, baseParams("GET", "/"), nil); err == nil {
		t.Fatal("uygulama yanıtsız kapandığı hâlde hata dönmedi")
	}
}

func TestDoRespectsContextCancellation(t *testing.T) {
	// Yanıt yazmayan, sadece bekleyen bir uygulama: iptal olmazsa test asılır.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			accepted <- c
		}
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := Do(ctx, conn, baseParams("GET", "/"), nil); err == nil {
		t.Fatal("bağlam iptal edildiği hâlde hata dönmedi")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("iptal %v sürdü, bağlantı zamanında kapatılmıyor", elapsed)
	}
	if c := <-accepted; c != nil {
		c.Close()
	}
}
