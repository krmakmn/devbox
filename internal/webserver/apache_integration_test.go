package webserver

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// nginx tarafındaki gibi: üretilen yapılandırmayı gerçek httpd'nin kendi
// söz dizimi denetiminden geçiriyoruz. Apache kurulu değilse atlanıyor.
//
// Ubuntu'da çalıştırılabilirin adı apache2, diğer dağıtımlarda ve
// Windows'ta httpd; ikisini de arıyoruz.
func TestGeneratedApacheConfigIsAcceptedByHttpd(t *testing.T) {
	binary := ""
	for _, name := range []string{"apache2", "httpd"} {
		if path, err := exec.LookPath(name); err == nil {
			binary = path
			break
		}
	}
	if binary == "" {
		t.Skip("apache kurulu değil; söz dizimi doğrulaması atlanıyor")
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

	sites := []Site{{
		Name: "magaza", Domain: "magaza.test",
		Aliases:      []string{"www.magaza.test"},
		DocumentRoot: root, Listen: "127.0.0.1:8080",
		PHPBackends: []string{"127.0.0.1:9000", "127.0.0.1:9001"},
		LogDir:      logs,
	}}

	generated := filepath.Join(prefix, "devbox.conf")
	if err := (&Apache{}).Write(generated, sites); err != nil {
		t.Fatal(err)
	}

	// Üretilen vhost'un ihtiyaç duyduğu modülleri yükleyen asgari bir ana
	// yapılandırma. Modül yolu dağıtıma göre değişiyor; bulamazsak testi
	// atlıyoruz — amaç modül avlamak değil, söz dizimini doğrulamak.
	modDir := findModuleDir()
	if modDir == "" {
		t.Skip("apache modül dizini bulunamadı")
	}

	var sb strings.Builder
	sb.WriteString("ServerRoot \"" + prefix + "\"\n")
	sb.WriteString("PidFile \"" + filepath.Join(prefix, "httpd.pid") + "\"\n")
	sb.WriteString("ErrorLog \"" + filepath.Join(logs, "main-error.log") + "\"\n")
	for _, mod := range []string{
		"mpm_event_module/mod_mpm_event.so",
		"authz_core_module/mod_authz_core.so",
		"authz_host_module/mod_authz_host.so",
		"unixd_module/mod_unixd.so",
		"proxy_module/mod_proxy.so",
		"proxy_fcgi_module/mod_proxy_fcgi.so",
		"proxy_balancer_module/mod_proxy_balancer.so",
		"lbmethod_byrequests_module/mod_lbmethod_byrequests.so",
		"slotmem_shm_module/mod_slotmem_shm.so",
		"setenvif_module/mod_setenvif.so",
		"log_config_module/mod_log_config.so",
		"dir_module/mod_dir.so",
	} {
		name, file, _ := strings.Cut(mod, "/")
		path := filepath.Join(modDir, file)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		sb.WriteString("LoadModule " + name + " \"" + path + "\"\n")
	}
	sb.WriteString("Include \"" + generated + "\"\n")

	wrapper := filepath.Join(prefix, "httpd.conf")
	if err := os.WriteFile(wrapper, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.CommandContext(context.Background(), binary, "-t", "-f", wrapper)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("apache üretilen yapılandırmayı reddetti: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Syntax OK") {
		t.Errorf("beklenmedik apache çıktısı:\n%s", out)
	}
}

func findModuleDir() string {
	for _, dir := range []string{
		"/usr/lib/apache2/modules",
		"/usr/lib64/httpd/modules",
		"/usr/lib/httpd/modules",
	} {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}
	return ""
}
