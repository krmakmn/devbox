package inspect

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// ReplayResult, tekrar gönderilen isteğin sonucu.
type ReplayResult struct {
	Status   int                 `json:"status"`
	Headers  map[string][]string `json:"headers"`
	Body     string              `json:"body"`
	Duration string              `json:"duration"`
	Size     int64               `json:"size"`
}

// Replay, kayıtlı bir isteği yeniden gönderir.
//
// # Neden kendi kenarımıza geri gönderiyoruz
//
// İsteği arka uca doğrudan göndermek, kenarın eklediği başlıkları
// (X-Forwarded-Proto gibi) ve yönlendirme kurallarını atlardı — yani
// tekrar edilen istek aslında farklı bir istek olurdu. Kenara geri
// göndermek, "aynı isteği bir daha yap" sözünü gerçekten tutuyor.
//
// Tekrar edilen istek de kaydediliyor: sonucu listede görünüyor ve
// "değiştirip tekrar gönder" akışının doğal parçası oluyor.
func Replay(ctx context.Context, ex *Exchange, edgeAddr string, rootPool *tls.Config) (*ReplayResult, error) {
	if ex == nil {
		return nil, fmt.Errorf("inspect: tekrar edilecek istek yok")
	}

	url := "https://" + ex.Host + ex.Path
	if ex.Query != "" {
		url += "?" + ex.Query
	}

	req, err := http.NewRequestWithContext(ctx, ex.Method, url, strings.NewReader(ex.RequestBody))
	if err != nil {
		return nil, err
	}
	for key, values := range ex.RequestHeaders {
		// Uzunluk ve bağlantı başlıkları yeniden hesaplanmalı; eskisini
		// taşımak bozuk bir istek üretir.
		switch strings.ToLower(key) {
		case "content-length", "connection", "transfer-encoding", "host":
			continue
		}
		for _, v := range values {
			req.Header.Add(key, v)
		}
	}
	req.Host = ex.Host

	dialer := &net.Dialer{Timeout: 10 * time.Second}
	tlsConfig := &tls.Config{}
	if rootPool != nil {
		tlsConfig = rootPool.Clone()
	}
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			// Alan adı çözümlemesini atlayıp doğrudan kenara gidiyoruz:
			// tekrar, çözücü kurulu olmasa da çalışmalı.
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, edgeAddr)
			},
			TLSClientConfig:   tlsConfig,
			DisableKeepAlives: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("inspect: tekrar gönderilemedi: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, DefaultBodyLimit))
	if err != nil {
		return nil, err
	}
	extra, _ := io.Copy(io.Discard, resp.Body)

	return &ReplayResult{
		Status:   resp.StatusCode,
		Headers:  cloneHeader(resp.Header),
		Body:     string(body),
		Duration: time.Since(start).Round(time.Microsecond).String(),
		Size:     int64(len(body)) + extra,
	}, nil
}
