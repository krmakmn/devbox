package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/krmakmn/devbox/internal/api"
	"github.com/krmakmn/devbox/internal/paths"
	"github.com/krmakmn/devbox/internal/projects"
)

func registryPath() string { return filepath.Join(paths.DataDir(), "projeler.json") }

func openRegistry() (*projects.Registry, error) { return projects.Open(registryPath()) }

func runProject(args []string) error {
	fs := flag.NewFlagSet("project", flag.ContinueOnError)
	dir := fs.String("dir", ".", "proje dizini (add için)")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Kullanım: devbox project <alt komut> [seçenekler]

  add      bulunulan (ya da -dir ile verilen) dizini kayda ekle
  list     kayıtlı projeleri listele
  remove   projeyi kayıttan çıkar (dizine dokunmaz)

Kayıt, denetim panelinin ve "devbox ui"nin hangi projeleri göstereceğini
belirler. Proje dizini istediğiniz yerde durabilir; kayıt yalnız bir
işaretçidir ve ad, alan adı gibi bilgiler her okumada devbox.yaml'dan
tazelenir.

`)
		fs.PrintDefaults()
	}

	sub, rest := splitSubcommand(args, "list")
	name, flags := splitNameAndFlags(rest)
	if err := fs.Parse(flags); err != nil {
		return err
	}

	reg, err := openRegistry()
	if err != nil {
		return err
	}

	switch sub {
	case "add":
		target := *dir
		if name != "" {
			target = name
		}
		p, err := reg.Add(target)
		if err != nil {
			return err
		}
		fmt.Printf("eklendi: %s (%s)\n  %s\n", p.Name, p.Domain, p.Dir)
		return nil

	case "list":
		list, err := reg.List()
		if err != nil {
			return err
		}
		if len(list) == 0 {
			fmt.Println("kayıtlı proje yok (devbox project add)")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "PROJE\tALAN ADI\tSUNUCU\tDİZİN")
		for _, p := range list {
			note := p.Dir
			if p.Missing {
				note += "  (dizin yok)"
			} else if p.Error != "" {
				note += "  (yapılandırma okunamıyor)"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", p.Name, p.Domain, p.Server, note)
		}
		return w.Flush()

	case "remove":
		if name == "" {
			fs.Usage()
			return fmt.Errorf("proje adı gerekli")
		}
		if err := reg.Remove(name); err != nil {
			return err
		}
		fmt.Printf("kayıttan çıkarıldı: %s\n", name)
		return nil

	default:
		fs.Usage()
		return fmt.Errorf("bilinmeyen alt komut %q", sub)
	}
}

// runUI, denetim panelini tarayıcıda açar.
func runUI(args []string) error {
	fs := flag.NewFlagSet("ui", flag.ContinueOnError)
	printOnly := fs.Bool("print", false, "tarayıcıyı açma, yalnız adresi yaz")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Kullanım: devbox ui [seçenekler]

Çalışan çekirdek sürecin denetim panelini tarayıcıda açar. Panel
projeleri listeler, tek tıkla başlatıp durdurur ve günlükleri canlı
gösterir.

Çekirdek süreç çalışmıyorsa önce onu başlatın: devbox daemon

`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	addr, err := api.ReadEndpoint(endpointPath())
	if err != nil {
		return fmt.Errorf("%w\n(çekirdek süreç çalışmıyor olabilir: devbox daemon)", err)
	}
	token, err := api.ReadToken(tokenPath())
	if err != nil {
		return err
	}

	// Jeton adres çubuğunda kalmıyor: sunucu çerezi kurup jetonsuz adrese
	// yönlendiriyor.
	url := fmt.Sprintf("http://%s/?jeton=%s", addr, token)

	if *printOnly {
		fmt.Println(url)
		return nil
	}
	fmt.Printf("Denetim paneli: http://%s/\n", addr)
	if err := openBrowser(url); err != nil {
		fmt.Printf("Tarayıcı açılamadı (%v).\nAdresi elle açın:\n  %s\n", err, url)
	}
	return nil
}

// openBrowser, adresi işletim sisteminin varsayılan tarayıcısında açar.
func openBrowser(url string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	switch runtime.GOOS {
	case "windows":
		// rundll32 yerine cmd start: start kabuk yerleşiği olduğu için
		// /c ile çağrılıyor. İlk tırnaklı argüman pencere başlığı sayılır,
		// bu yüzden boş bir başlık veriliyor.
		return exec.CommandContext(ctx, "cmd", "/c", "start", "", url).Run()
	case "darwin":
		return exec.CommandContext(ctx, "open", url).Run()
	default:
		return exec.CommandContext(ctx, "xdg-open", url).Run()
	}
}

// devboxExecutable, çekirdek sürecin projeleri başlatmak için
// çağıracağı çalıştırılabilirin yolu.
func devboxExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("devbox çalıştırılabilirinin yolu bulunamadı: %w", err)
	}
	// Sembolik bağ üzerinden çağrıldıysa gerçek yolu kullan: alt süreç
	// bağın hedefinden bağımsız çalışmalı.
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if strings.TrimSpace(exe) == "" {
		return "", fmt.Errorf("devbox çalıştırılabilirinin yolu boş")
	}
	return exe, nil
}
