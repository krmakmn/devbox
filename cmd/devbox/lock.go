package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/krmakmn/devbox/internal/container"
	"github.com/krmakmn/devbox/internal/lockfile"
	"github.com/krmakmn/devbox/internal/project"
	"github.com/krmakmn/devbox/internal/services"
)

func runLock(args []string) error {
	fs := flag.NewFlagSet("lock", flag.ContinueOnError)
	dir := fs.String("dir", ".", "proje dizini")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Kullanım: devbox lock <alt komut> [seçenekler]

  write    makinede çalışan sürümleri devbox.lock'a yaz
  check    devbox.lock ile makinedeki sürümleri karşılaştır
  show     kilit dosyasını göster

devbox.yaml niyeti anlatır ("PHP 8.3 istiyorum"), devbox.lock
gerçekleşeni ("8.3.14 çalıştı"). İkisini de depoya ekleyin: ekip
arkadaşınız "devbox lock check" ile farkı görür.

Kilit bir rapor, zorlayıcı değil: eksik bir sürümü indirmek imzalı
manifest altyapısına bağlı ve o henüz yok.

`)
		fs.PrintDefaults()
	}

	sub, rest := splitSubcommand(args, "show")
	if err := fs.Parse(rest); err != nil {
		return err
	}

	absDir := mustAbs(*dir)
	cfg, err := project.Load(absDir)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%s bulunamadı; önce \"devbox init\" çalıştırın", project.FileName)
		}
		return err
	}

	switch sub {
	case "write":
		lock := collectLock(cfg)
		if err := lock.Save(absDir); err != nil {
			return err
		}
		fmt.Printf("yazıldı: %s (%d bileşen)\n", lockfile.Path(absDir), len(lock.Entries))
		return nil

	case "check":
		expected, err := lockfile.Load(absDir)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("%s yok; önce \"devbox lock write\" çalıştırın", lockfile.FileName)
			}
			return err
		}
		actual := collectLock(cfg)
		diffs := lockfile.Compare(expected, actual)
		if len(diffs) == 0 {
			fmt.Printf("sürümler kilitle uyuşuyor (%d bileşen)\n", len(expected.Entries))
			return nil
		}
		fmt.Printf("%d fark bulundu:\n\n", len(diffs))
		for _, d := range diffs {
			fmt.Printf("  • %s\n", d)
		}
		fmt.Printf("\nKilit %s tarihinde %s üzerinde alınmış.\n",
			expected.GeneratedAt.Format("2006-01-02"), expected.Platform)
		// Fark bir hata değil, bir rapor: çıkış kodu 0 kalıyor ki
		// betiklerde "uyuşmuyor" durumu kararı çağırana bıraksın.
		return nil

	case "show":
		lock, err := lockfile.Load(absDir)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("%s yok; önce \"devbox lock write\" çalıştırın", lockfile.FileName)
			}
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "TÜR\tAD\tİSTENEN\tÇALIŞAN\tKAYNAK")
		for _, e := range lock.Entries {
			istenen := e.Requested
			if istenen == "" {
				istenen = "-"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", e.Kind, e.Name, istenen, e.Resolved, e.Source)
		}
		return w.Flush()

	default:
		fs.Usage()
		return fmt.Errorf("bilinmeyen alt komut %q", sub)
	}
}

// collectLock, makinede gerçekten ne çalıştığını toplar.
func collectLock(cfg *project.Config) *lockfile.Lock {
	lock := lockfile.New(cfg.Name, runtime.GOOS+"/"+runtime.GOARCH)

	if cfg.UsesPHP() {
		if exe, err := findPHPCGI("", cfg.PHP.Version); err == nil {
			lock.Add(lockfile.Entry{
				Kind: "php", Name: "php",
				Requested: cfg.PHP.Version,
				Resolved:  toolVersion(exe, "-v"),
				Source:    exe,
			})
		}
	}

	for _, svc := range cfg.Services {
		switch svc.Driver {
		case project.DriverDocker:
			lock.Add(lockfile.Entry{
				Kind: "container", Name: svc.Name,
				Requested: svc.Image,
				Resolved:  imageDigest(svc.Image),
				Source:    svc.Image,
			})
		default:
			spec, err := services.ParseSpec(svc.Kind)
			if err != nil {
				continue
			}
			if exe, ok := findServiceBinary(spec.Kind); ok {
				lock.Add(lockfile.Entry{
					Kind: "service", Name: svc.Name,
					Requested: svc.Version,
					Resolved:  toolVersion(exe, "--version"),
					Source:    exe,
				})
			}
		}
	}
	return lock
}

// findServiceBinary, yerel bir servisin çalıştırılabilirini bulur.
func findServiceBinary(kind services.Kind) (string, bool) {
	var adaylar []string
	switch kind {
	case services.KindRedis:
		adaylar = []string{"redis-server", "valkey-server", "memurai"}
	case services.KindMeilisearch:
		adaylar = []string{"meilisearch"}
	case services.KindMinIO:
		adaylar = []string{"minio"}
	}
	for _, ad := range adaylar {
		if path, err := exec.LookPath(ad); err == nil {
			return path, true
		}
	}
	return "", false
}

// versionPattern, çıktıdan sürüm numarasını çıkarır.
var versionPattern = regexp.MustCompile(`\d+\.\d+(\.\d+)?`)

// toolVersion, aracın sürümünü öğrenir.
//
// Çıktının tamamı değil yalnız sürüm numarası saklanıyor: derleme
// tarihi ve derleyici gibi alanlar her makinede farklı olur ve kilit
// dosyası her yerde farklı görünürdü.
func toolVersion(exe string, arg string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, exe, arg).CombinedOutput()
	if err != nil && len(out) == 0 {
		return "bilinmiyor"
	}
	if m := versionPattern.FindString(string(out)); m != "" {
		return m
	}
	return strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
}

// imageDigest, konteyner imajının içerik özetini döner.
//
// Etiket ("nginx:alpine") zamanla farklı bir imaja işaret edebiliyor;
// özet değişmez. Kilit dosyasının anlamı tam olarak bu.
func imageDigest(image string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	rt, err := container.FindRuntime()
	if err != nil {
		return "bilinmiyor"
	}
	out, err := exec.CommandContext(ctx, rt, "image", "inspect",
		"--format", "{{.Id}}", image).Output()
	if err != nil {
		return "bilinmiyor"
	}
	return strings.TrimSpace(string(out))
}
