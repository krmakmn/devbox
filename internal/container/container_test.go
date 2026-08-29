package container

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestContainerNameIsPredictable(t *testing.T) {
	spec := Spec{Project: "magaza", Name: "api"}
	if got := spec.ContainerName(); got != "devbox-magaza-api" {
		t.Errorf("ad = %q", got)
	}
	// Ad öngörülebilir olmalı: kapanışta arkada kalmış konteyneri
	// bulmak buna dayanıyor. Güvensiz karakterler temizlenmeli.
	weird := Spec{Project: "mağaza/prod", Name: "api v2"}
	name := weird.ContainerName()
	if strings.ContainsAny(name, "/ ") {
		t.Errorf("ad güvensiz karakter içeriyor: %q", name)
	}
	if weird.ContainerName() != name {
		t.Error("ad iki çağrı arasında değişti")
	}
}

// Port yalnız geri döngüye yayınlanmalı: "-p 8080:80" konteyneri
// makinenin tüm arayüzlerinde açar ve aynı ağdaki herkes geliştirme
// veritabanına bağlanabilir.
func TestPortIsPublishedToLoopbackOnly(t *testing.T) {
	spec := Spec{Project: "p", Name: "s", Image: "img", ContainerPort: 80, HostPort: 18080}
	args := strings.Join(spec.Args(), " ")
	if !strings.Contains(args, "127.0.0.1:18080:80") {
		t.Errorf("port geri döngüye bağlanmamış: %s", args)
	}
	for _, tehlikeli := range []string{"-p 18080:80", "0.0.0.0:"} {
		if strings.Contains(args, tehlikeli) {
			t.Errorf("port tüm arayüzlere açık: %s", args)
		}
	}
}

func TestArgsAreDeterministic(t *testing.T) {
	spec := Spec{
		Project: "p", Name: "s", Image: "img", ContainerPort: 80, HostPort: 1,
		Env: map[string]string{"Z": "1", "A": "2", "M": "3"},
	}
	first := strings.Join(spec.Args(), " ")
	for i := 0; i < 20; i++ {
		if got := strings.Join(spec.Args(), " "); got != first {
			t.Fatalf("argümanlar değişti:\n%s\n%s", first, got)
		}
	}
	// Sıralı olmalı ki günlükteki komut okunabilir kalsın.
	if strings.Index(first, "A=2") > strings.Index(first, "Z=1") {
		t.Errorf("ortam değişkenleri sıralı değil: %s", first)
	}
}

func TestRelativeVolumesResolveAgainstWorkDir(t *testing.T) {
	// Yollar sabit yazılmıyor, işletim sisteminden alınıyor. Sabit POSIX
	// yolu yazmak testi Windows'ta hem anlamsız hem de yanıltıcı
	// kılıyordu: orada "/mutlak" mutlak sayılmaz (sürücü harfi yok), o
	// yüzden test gerçek bir kusuru değil kendi varsayımını ölçüyordu.
	//
	// t.TempDir Windows'ta sürücü harfli bir yol veriyor; bu, ana makine
	// yolunun iki nokta üst üsteden yanlış bölünmesi kusurunu da doğal
	// olarak kapsıyor.
	calisma := t.TempDir()
	mutlak := t.TempDir()

	spec := Spec{
		Project: "p", Name: "s", Image: "img", ContainerPort: 80, HostPort: 1,
		Volumes: []string{"./veri:/data", mutlak + ":/m"},
		WorkDir: calisma,
	}
	args := strings.Join(spec.Args(), " ")

	beklenen := filepath.Join(calisma, "veri") + ":/data"
	if !strings.Contains(args, beklenen) {
		t.Errorf("göreli bağlama çözülmemiş: %q bekleniyordu\n%s", beklenen, args)
	}
	if !strings.Contains(args, mutlak+":/m") {
		t.Errorf("mutlak bağlama bozulmuş: %q bekleniyordu\n%s", mutlak+":/m", args)
	}
}

// TestSplitVolume, ayırmanın kendisini doğrudan sınıyor. Sürücü harfli
// durum yalnız Windows'ta farklı davranıyor ama tablo her yerde koşuyor:
// filepath.VolumeName Unix'te boş döndüğü için "c:/veri" orada adı "c"
// olan göreli dizin olarak okunmalı.
func TestSplitVolume(t *testing.T) {
	durumlar := []struct {
		giris     string
		host      string
		rest      string
		ok        bool
		yalnizWin bool
	}{
		{giris: "./veri:/data", host: "./veri", rest: "/data", ok: true},
		{giris: "/mutlak:/m", host: "/mutlak", rest: "/m", ok: true},
		{giris: "/veri:/data:ro", host: "/veri", rest: "/data:ro", ok: true},
		{giris: "hedefsiz", ok: false},
		{giris: `C:\kod\veri:/data`, host: `C:\kod\veri`, rest: "/data", ok: true, yalnizWin: true},
		{giris: `C:/kod/veri:/data:ro`, host: `C:/kod/veri`, rest: "/data:ro", ok: true, yalnizWin: true},
	}

	for _, d := range durumlar {
		if d.yalnizWin && runtime.GOOS != "windows" {
			continue
		}
		host, rest, ok := splitVolume(d.giris)
		if ok != d.ok {
			t.Errorf("splitVolume(%q) ok=%v, %v bekleniyordu", d.giris, ok, d.ok)
			continue
		}
		if !ok {
			continue
		}
		if host != d.host || rest != d.rest {
			t.Errorf("splitVolume(%q) = (%q, %q), (%q, %q) bekleniyordu",
				d.giris, host, rest, d.host, d.rest)
		}
	}
}

func TestEndpoint(t *testing.T) {
	spec := Spec{HostPort: 1234}
	if got := spec.Endpoint(); got != "http://127.0.0.1:1234" {
		t.Errorf("uç nokta = %q", got)
	}
}

// Çalıştırıcı yoksa hata kurulum yolunu söylemeli.
func TestNoRuntimeErrorExplainsInstall(t *testing.T) {
	err := ErrNoRuntime{}
	if !strings.Contains(err.Error(), "docker") || !strings.Contains(err.Error(), "kurulum") &&
		!strings.Contains(err.Error(), "install") {
		t.Errorf("hata kurulum yolunu söylemiyor: %v", err)
	}
}

// Gerçek motor testi: docker varsa konteyner gerçekten açılıp
// kapanmalı.
func TestRealContainerLifecycle(t *testing.T) {
	runtime, err := FindRuntime()
	if err != nil {
		t.Skip("docker/podman yok")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, runtime, "info").Run(); err != nil {
		t.Skip("konteyner çalıştırıcısı erişilebilir değil")
	}

	const image = "devbox-deneme:1"
	if !ImageExists(ctx, runtime, image) {
		t.Skipf("%s imajı yok; bu test yerelde üretilen imaja dayanıyor", image)
	}

	spec := Spec{
		Project: "test", Name: "web", Image: image,
		ContainerPort: 8080, HostPort: 0,
	}
	// Boş bir port bul.
	spec.HostPort = freePort(t)

	// Kalıntı varsa temizle.
	Remove(ctx, runtime, spec.ContainerName())

	cmd := exec.CommandContext(ctx, runtime, spec.Args()...)
	if err := cmd.Start(); err != nil {
		t.Fatalf("konteyner başlatılamadı: %v", err)
	}
	defer func() {
		Remove(context.Background(), runtime, spec.ContainerName())
		cmd.Process.Kill()
		cmd.Wait()
	}()

	// Portun açılmasını bekle.
	if !waitPort(spec.HostPort, 30*time.Second) {
		t.Fatal("konteyner portu açılmadı")
	}

	// Temizlik gerçekten siliyor mu?
	if err := Remove(ctx, runtime, spec.ContainerName()); err != nil {
		t.Fatalf("konteyner silinemedi: %v", err)
	}
	out, _ := exec.CommandContext(ctx, runtime, "ps", "-a", "--format", "{{.Names}}").Output()
	if strings.Contains(string(out), spec.ContainerName()) {
		t.Error("silinen konteyner hâlâ listede")
	}

	// Olmayan bir konteyneri silmek hata değil.
	if err := Remove(ctx, runtime, "devbox-yok-boyle-bir-sey"); err != nil {
		t.Errorf("olmayan konteynerin silinmesi hata verdi: %v", err)
	}
}
