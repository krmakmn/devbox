package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/krmakmn/devbox/internal/supervisor"
)

// Client, devboxd'ye konuşan istemci. CLI ve ileride GUI aynı bunu kullanır.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewClient, verilen adres ve jetonla bir istemci kurar.
func NewClient(addr, token string) *Client {
	return &Client{
		baseURL: "http://" + addr,
		token:   token,
		// Zaman aşımı yok: günlük akışı süresiz açık kalır. Tek tek
		// çağrılar kendi bağlamlarını taşıyor.
		http: &http.Client{},
	}
}

// WriteEndpoint, devboxd'nin dinlediği adresi diske yazar; CLI onu böyle
// buluyor.
func WriteEndpoint(path, addr string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(addr+"\n"), 0o600)
}

// ReadEndpoint, devboxd'nin adresini okur.
func ReadEndpoint(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("api: devboxd adresi okunamadı (çalışıyor mu?): %w", err)
	}
	addr := strings.TrimSpace(string(data))
	if addr == "" {
		return "", errors.New("api: adres dosyası boş")
	}
	return addr, nil
}

// Status, devboxd'nin durumunu döner.
func (c *Client) Status(ctx context.Context) (StatusResponse, error) {
	var out StatusResponse
	err := c.do(ctx, http.MethodGet, "/v1/status", &out)
	return out, err
}

// Services, tüm servislerin durumunu döner.
func (c *Client) Services(ctx context.Context) ([]supervisor.Status, error) {
	var out []supervisor.Status
	err := c.do(ctx, http.MethodGet, "/v1/services", &out)
	return out, err
}

// StartService, servisi başlatır ve yeni durumunu döner.
func (c *Client) StartService(ctx context.Context, name string) (supervisor.Status, error) {
	var out supervisor.Status
	err := c.do(ctx, http.MethodPost, "/v1/services/"+name+"/start", &out)
	return out, err
}

// StopService, servisi durdurur.
func (c *Client) StopService(ctx context.Context, name string) (supervisor.Status, error) {
	var out supervisor.Status
	err := c.do(ctx, http.MethodPost, "/v1/services/"+name+"/stop", &out)
	return out, err
}

// Runtimes, kurulu runtime'ları döner.
func (c *Client) Runtimes(ctx context.Context) ([]RuntimeInfo, error) {
	var out []RuntimeInfo
	err := c.do(ctx, http.MethodGet, "/v1/runtimes", &out)
	return out, err
}

// Logs, servisin birikmiş günlüğünü döner.
func (c *Client) Logs(ctx context.Context, name string) (string, error) {
	resp, err := c.request(ctx, http.MethodGet, "/v1/services/"+name+"/logs")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp); err != nil {
		return "", err
	}
	data, err := io.ReadAll(resp.Body)
	return string(data), err
}

// StreamLogs, canlı günlük akışını okur ve her satır için onLine'ı çağırır.
// Bağlam iptal edilene ya da sunucu bağlantıyı kapatana kadar döner değil.
func (c *Client) StreamLogs(ctx context.Context, name string, onLine func(string)) error {
	resp, err := c.request(ctx, http.MethodGet, "/v1/services/"+name+"/logs/stream")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp); err != nil {
		return err
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		// ":" ile başlayan satırlar SSE yorumu (bağlantıyı canlı tutan ping).
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			onLine(data)
		}
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

func (c *Client) do(ctx context.Context, method, path string, out any) error {
	resp, err := c.request(ctx, method, path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp); err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) request(ctx context.Context, method, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("api: devboxd'ye ulaşılamadı: %w", err)
	}
	return resp, nil
}

// checkStatus, hata yanıtlarını sunucunun verdiği mesajla birlikte döndürür.
func checkStatus(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))

	var errResp ErrorResponse
	if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
		return fmt.Errorf("api: %s: %s", resp.Status, errResp.Error)
	}
	return fmt.Errorf("api: %s: %s", resp.Status, strings.TrimSpace(string(bytes.TrimSpace(body))))
}

// WaitReady, devboxd yanıt verene kadar bekler.
func (c *Client) WaitReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		_, err := c.Status(checkCtx)
		cancel()
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("api: devboxd %s içinde yanıt vermedi: %w", timeout, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
