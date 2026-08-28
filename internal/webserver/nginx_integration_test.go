package webserver

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Altın dosya testi üretilen metnin beklediğimiz gibi olduğunu söyler ama
// nginx'in onu kabul edeceğini söylemez. Bu test, gerçekten kurulu bir
// nginx'in kendi söz dizimi denetiminden geçiriyor.
//
// nginx yoksa atlanıyor: geliştirici makinesinde kurulu olmayabilir. CI'nın
// Linux işinde kuruluyor, dolayısıyla her değişiklikte koşuyor.
func TestGeneratedNginxConfigIsAcceptedByNginx(t *testing.T) {
	binary, err := exec.LookPath("nginx")
	if err != nil {
		t.Skip("nginx kurulu değil; söz dizimi doğrulaması atlanıyor")
	}

	prefix := t.TempDir()
	root := filepath.Join(prefix, "public")
	logs := filepath.Join(prefix, "log")
	for _, dir := range []string{root, logs} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "index.php"), []byte("<?php"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Üretilen dosya http bağlamına dahil ediliyor; map yönergesi orada
	// geçerli.
	sites := []Site{
		{
			Name: "magaza", Domain: "magaza.test",
			Aliases:      []string{"www.magaza.test", "*.magaza.test"},
			DocumentRoot: root, Listen: "127.0.0.1:8080",
			PHPBackends: []string{"127.0.0.1:9000", "127.0.0.1:9001"},
			LogDir:      logs,
		},
		{
			Name: "belgeler", Domain: "belgeler.test",
			DocumentRoot: root, Listen: "127.0.0.1:8080",
			LogDir: logs,
		},
	}

	generated := filepath.Join(prefix, "devbox.conf")
	if err := (&Nginx{}).Write(generated, sites); err != nil {
		t.Fatal(err)
	}

	// fastcgi_params, üretilen yapılandırmanın include ettiği dosya.
	// Sistemdeki kopyayı prefix'e alıyoruz ki nginx onu bulabilsin.
	if data, err := os.ReadFile("/etc/nginx/fastcgi_params"); err == nil {
		if err := os.WriteFile(filepath.Join(prefix, "fastcgi_params"), data, 0o644); err != nil {
			t.Fatal(err)
		}
	} else {
		t.Skip("fastcgi_params bulunamadı; nginx kurulumu eksik")
	}

	// pid ve günlük yollarını prefix'e alıyoruz. "nginx -t" yalnız söz
	// dizimini denetlemiyor; pid dosyasını da açmayı deniyor ve
	// varsayılan /run/nginx.pid root olmayan bir kullanıcı için yazılabilir
	// değil. Bu ayarlar olmadan test, üretilen yapılandırma kusursuz olsa
	// bile ortam yüzünden düşer.
	wrapper := filepath.Join(prefix, "nginx.conf")
	content := "pid " + filepath.Join(prefix, "nginx.pid") + ";\n" +
		"error_log " + filepath.Join(logs, "nginx-error.log") + ";\n" +
		"events {}\n" +
		"http {\n" +
		"    client_body_temp_path " + filepath.Join(prefix, "body") + ";\n" +
		"    proxy_temp_path " + filepath.Join(prefix, "proxy") + ";\n" +
		"    fastcgi_temp_path " + filepath.Join(prefix, "fastcgi") + ";\n" +
		"    include " + generated + ";\n" +
		"}\n"
	if err := os.WriteFile(wrapper, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.CommandContext(context.Background(), binary, "-t", "-c", wrapper, "-p", prefix)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("nginx üretilen yapılandırmayı reddetti: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "syntax is ok") {
		t.Errorf("beklenmedik nginx çıktısı:\n%s", out)
	}
}
