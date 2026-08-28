package fastcgi

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
)

// Response, FastCGI uygulamasının ürettiği CGI yanıtıdır.
//
// Body akış hâlinde gelir: yanıt gövdesi belleğe toplanmaz, kayıtlar
// okundukça aktarılır. Büyük dosya indirmelerinde bellek şişmesin diye
// böyle; çağıran Body'yi kapatmakla yükümlüdür.
type Response struct {
	StatusCode int
	Header     http.Header
	Body       io.ReadCloser

	// Stderr, uygulamanın STDERR akışına yazdıklarıdır. PHP'de bu ölümcül
	// hatalar ve uyarılar demek; günlüğe basmak için Body tüketildikten
	// sonra okunur.
	Stderr func() []byte
}

// Do, açık bir bağlantı üzerinden tek bir FastCGI isteği çalıştırır.
//
// Bağlantı yanıt gövdesi kapatılınca kapanır; çağıran conn'u ayrıca
// kapatmamalıdır. KEEP_CONN kullanmıyoruz: php-cgi zaten aynı anda tek
// istek işler, dolayısıyla bağlantıyı saklamanın kazancı yok, buna karşılık
// yarım kalmış istek sonrası bağlantıyı yeniden kullanmanın riski var.
func Do(ctx context.Context, conn net.Conn, params map[string]string, stdin io.Reader) (*Response, error) {
	// Bağlam iptal edilirse bağlantıyı kapatarak okuma/yazmayı serbest
	// bırakıyoruz; php-cgi'nin kendi zaman aşımı yok, bu tek kaldıraç.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()

	// İstek yazımı ayrı bir goroutine'de: büyük bir gövde yüklerken
	// uygulama aynı anda yanıt yazmaya başlarsa, biz okumadığımız için
	// TCP tamponları dolar ve iki taraf da kilitlenir.
	writeErr := make(chan error, 1)
	go func() {
		writeErr <- writeRequest(conn, params, stdin)
	}()

	sr := &streamReader{
		src:    bufio.NewReaderSize(conn, 32*1024),
		stderr: &bytes.Buffer{},
	}
	br := bufio.NewReader(sr)

	resp, err := parseCGIResponse(br)
	if err != nil {
		conn.Close()
		// Yazma tarafı da hata verdiyse asıl sebep büyük ihtimalle odur.
		if werr := <-writeErr; werr != nil {
			return nil, fmt.Errorf("fastcgi: istek yazılamadı: %w", werr)
		}
		return nil, err
	}

	resp.Body = &responseBody{
		r:        br,
		conn:     conn,
		stream:   sr,
		writeErr: writeErr,
	}
	resp.Stderr = func() []byte { return sr.stderr.Bytes() }
	return resp, nil
}

func writeRequest(w io.Writer, params map[string]string, stdin io.Reader) error {
	bw := bufio.NewWriterSize(w, 32*1024)

	if err := writeRecord(bw, typeBeginRequest, beginRequestBody(roleResponder, 0)); err != nil {
		return err
	}
	if err := writeStream(bw, typeParams, encodeParams(params)); err != nil {
		return err
	}
	if err := copyStream(bw, typeStdin, stdin); err != nil {
		return err
	}
	return bw.Flush()
}

// streamReader, kayıt akışını düz bir bayt akışına indirger: STDOUT
// gövdelerini sırayla verir, STDERR'i yan tampona biriktirir, END_REQUEST
// görünce io.EOF döner.
type streamReader struct {
	src    *bufio.Reader
	buf    []byte
	stderr *bytes.Buffer

	ended     bool
	appStatus uint32
	protoErr  error
}

func (s *streamReader) Read(p []byte) (int, error) {
	for len(s.buf) == 0 {
		if s.ended {
			if s.protoErr != nil {
				return 0, s.protoErr
			}
			return 0, io.EOF
		}
		rec, err := readRecord(s.src)
		if err != nil {
			if err == io.EOF {
				// Uygulama END_REQUEST göndermeden bağlantıyı kapattı:
				// php-cgi çöktüğünde tipik olarak böyle olur.
				return 0, io.ErrUnexpectedEOF
			}
			return 0, err
		}
		switch rec.Type {
		case typeStdout:
			s.buf = rec.Body
		case typeStderr:
			s.stderr.Write(rec.Body)
		case typeEndRequest:
			appStatus, protoStatus, err := parseEndRequest(rec.Body)
			if err != nil {
				return 0, err
			}
			s.appStatus = appStatus
			s.ended = true
			s.protoErr = protocolStatusError(protoStatus)
		default:
			// Bilinmeyen kayıt türleri sessizce atlanır (spec 3.3).
		}
	}
	n := copy(p, s.buf)
	s.buf = s.buf[n:]
	return n, nil
}

func protocolStatusError(status uint8) error {
	switch status {
	case statusRequestComplete:
		return nil
	case statusCantMpxConn:
		return errors.New("fastcgi: uygulama bağlantı çoğullamayı desteklemiyor")
	case statusOverloaded:
		return errors.New("fastcgi: uygulama aşırı yüklü")
	case statusUnknownRole:
		return errors.New("fastcgi: uygulama Responder rolünü tanımıyor")
	default:
		return fmt.Errorf("fastcgi: bilinmeyen protokol durumu %d", status)
	}
}

// responseBody, gövdeyi okuturken bağlantının ve yazma goroutine'inin
// düzgün kapanmasını da üstlenir.
type responseBody struct {
	r        *bufio.Reader
	conn     net.Conn
	stream   *streamReader
	writeErr <-chan error
	closed   bool
}

func (b *responseBody) Read(p []byte) (int, error) { return b.r.Read(p) }

func (b *responseBody) Close() error {
	if b.closed {
		return nil
	}
	b.closed = true
	// Kalanı boşalt ki uygulama yazmayı bitirebilsin, sonra bağlantıyı kapat.
	io.Copy(io.Discard, b.r)
	err := b.conn.Close()
	if werr := <-b.writeErr; werr != nil && err == nil {
		err = werr
	}
	return err
}

// parseCGIResponse, STDOUT akışının başındaki CGI başlıklarını çözer.
//
// CGI'de durum kodu ayrı bir alan değil, "Status: 404 Not Found" başlığıdır.
// Status yoksa ama Location varsa 302, o da yoksa 200 (RFC 3875 §6.2).
func parseCGIResponse(br *bufio.Reader) (*Response, error) {
	mimeHeader, err := textproto.NewReader(br).ReadMIMEHeader()
	if err != nil {
		if err == io.ErrUnexpectedEOF || err == io.EOF {
			return nil, errors.New("fastcgi: uygulama yanıt üretmeden kapandı")
		}
		return nil, fmt.Errorf("fastcgi: CGI başlıkları çözülemedi: %w", err)
	}

	resp := &Response{
		StatusCode: http.StatusOK,
		Header:     http.Header(mimeHeader),
	}

	if raw := resp.Header.Get("Status"); raw != "" {
		resp.Header.Del("Status")
		code, err := parseStatusLine(raw)
		if err != nil {
			return nil, err
		}
		resp.StatusCode = code
	} else if resp.Header.Get("Location") != "" {
		resp.StatusCode = http.StatusFound
	}

	return resp, nil
}

// parseStatusLine, "404" ya da "404 Not Found" biçimini kabul eder.
func parseStatusLine(raw string) (int, error) {
	field := raw
	if i := strings.IndexByte(field, ' '); i >= 0 {
		field = field[:i]
	}
	code, err := strconv.Atoi(field)
	if err != nil || code < 100 || code > 599 {
		return 0, fmt.Errorf("fastcgi: geçersiz Status başlığı %q", raw)
	}
	return code, nil
}
