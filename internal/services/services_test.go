package services

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/krmakmn/devbox/internal/ports"
	"github.com/krmakmn/devbox/internal/supervisor"
)

func TestParseSpec(t *testing.T) {
	cases := map[string]Spec{
		"redis":       {Kind: KindRedis},
		" Redis ":     {Kind: KindRedis},
		"redis@7":     {Kind: KindRedis, Version: "7"},
		"valkey":      {Kind: KindRedis},
		"meilisearch": {Kind: KindMeilisearch},
		"meili@1.5":   {Kind: KindMeilisearch, Version: "1.5"},
		"minio":       {Kind: KindMinIO},
		"s3":          {Kind: KindMinIO},
	}
	for in, want := range cases {
		got, err := ParseSpec(in)
		if err != nil {
			t.Errorf("ParseSpec(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseSpec(%q) = %+v, beklenen %+v", in, got, want)
		}
	}

	for _, bad := range []string{"", "rabbitmq", "elasticsearch", "redis-server"} {
		if _, err := ParseSpec(bad); err == nil {
			t.Errorf("bilinmeyen servis kabul edildi: %q", bad)
		}
	}
}

// Veritabanları services altında değil "devbox db" ile yönetiliyor; yanlış
// yere yazan kullanıcı doğru komuta yönlendirilmeli.
func TestParseSpecPointsDatabasesToDbCommand(t *testing.T) {
	for _, name := range []string{"postgres", "mysql", "mariadb"} {
		_, err := ParseSpec(name)
		if err == nil {
			t.Fatalf("%q services olarak kabul edildi", name)
		}
		if !strings.Contains(err.Error(), "devbox db") {
			t.Errorf("%q hatası doğru komuta yönlendirmiyor: %v", name, err)
		}
	}
}

// İkili bulunamadığında hata, nasıl kurulacağını söylemeli: sessizce
// atlamak, uygulama bağlanamayınca anlaşılması dakikalar süren bir hataya
// dönüşür.
func TestMissingBinaryExplainsHowToInstall(t *testing.T) {
	sup, err := supervisor.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer sup.Close()

	m := &Manager{
		Root:       t.TempDir(),
		Supervisor: sup,
		Alloc:      ports.New("127.0.0.1"),
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	// Var olmayan bir ikili arayan sahte bir tür yerine, gerçekten kurulu
	// olmayan bir servisi kullanıyoruz.
	if _, err := exec.LookPath("meilisearch"); err == nil {
		t.Skip("meilisearch kurulu; bu test onun yokluğuna dayanıyor")
	}
	_, err = m.Start(context.Background(), Spec{Kind: KindMeilisearch})
	if err == nil {
		t.Fatal("kurulu olmayan servis başlatıldı")
	}
	if !strings.Contains(err.Error(), "Kurulum:") {
		t.Errorf("hata kurulum yolunu söylemiyor: %v", err)
	}
}

func TestDriverEnvironment(t *testing.T) {
	redis := (&Redis{}).Env(&Service{Port: 6390})
	if redis["REDIS_URL"] != "redis://127.0.0.1:6390" {
		t.Errorf("REDIS_URL = %q", redis["REDIS_URL"])
	}
	minio := (&MinIO{}).Env(&Service{Port: 9010, ConsolePort: 9011})
	if minio["AWS_ENDPOINT"] != "http://127.0.0.1:9010" {
		t.Errorf("AWS_ENDPOINT = %q", minio["AWS_ENDPOINT"])
	}
	// Laravel'in S3 sürücüsü yol biçimli adres olmadan MinIO'ya bağlanamaz.
	if minio["AWS_USE_PATH_STYLE_ENDPOINT"] != "true" {
		t.Error("MinIO için yol biçimli adres açık değil")
	}
	meili := (&Meilisearch{}).Env(&Service{Port: 7710})
	if meili["MEILISEARCH_HOST"] != "http://127.0.0.1:7710" {
		t.Errorf("MEILISEARCH_HOST = %q", meili["MEILISEARCH_HOST"])
	}
}

// Servisler yalnız loopback'i dinlemeli: parolasız bir Redis'i ağa açmak,
// makinedeki her şeyi okunur yazılır hâle getirir.
func TestServicesBindToLoopbackOnly(t *testing.T) {
	cases := map[string][]string{
		"redis":       (&Redis{}).ServiceConfig(&Service{Port: 1, DataDir: "d"}).Args,
		"meilisearch": (&Meilisearch{}).ServiceConfig(&Service{Port: 1, DataDir: "d"}).Args,
		"minio":       (&MinIO{}).ServiceConfig(&Service{Port: 1, ConsolePort: 2, DataDir: "d"}).Args,
	}
	for name, args := range cases {
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "127.0.0.1") {
			t.Errorf("%s loopback'e bağlanmıyor: %s", name, joined)
		}
		if strings.Contains(joined, "0.0.0.0") {
			t.Errorf("%s tüm arayüzlere açık: %s", name, joined)
		}
	}
}

// Gerçek motor testi: Redis kuruluysa gerçekten ayağa kalkmalı ve
// bağlantı kabul etmeli.
func TestRedisStartsAndAnswers(t *testing.T) {
	if _, err := exec.LookPath("redis-server"); err != nil {
		t.Skip("redis-server kurulu değil")
	}

	sup, err := supervisor.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer sup.Close()

	m := &Manager{
		Root:       t.TempDir(),
		Supervisor: sup,
		Alloc:      ports.New("127.0.0.1"),
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	svc, err := m.Start(ctx, Spec{Kind: KindRedis})
	if err != nil {
		t.Fatalf("redis başlatılamadı: %v", err)
	}

	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+itoa(svc.Port), 5*time.Second)
	if err != nil {
		t.Fatalf("redis'e bağlanılamadı: %v", err)
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte("PING\r\n")); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(line) != "+PONG" {
		t.Errorf("PING yanıtı = %q", line)
	}

	// Ortam değişkenleri çalışan servisi göstermeli.
	if got := m.Env()["REDIS_PORT"]; got != itoa(svc.Port) {
		t.Errorf("REDIS_PORT = %q, beklenen %d", got, svc.Port)
	}
}
