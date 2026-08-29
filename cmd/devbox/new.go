package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/krmakmn/devbox/internal/project"
	"github.com/krmakmn/devbox/internal/scaffold"
)

// runNew, yeni bir proje kurar: iskelet, devbox.yaml ve kayıt.
//
// Üç adımı tek komutta birleştirmesinin sebebi, Laragon'da en çok
// vakit alan işin bu üçlü olması: klasör aç, çerçeveyi kur, sanal
// konağı elle tanımla. Burada "devbox new laravel magaza" sonunda
// çalıştırılabilir bir proje ve tarayıcıda açılacak bir adres var.
func runNew(args []string) error {
	fs := flag.NewFlagSet("new", flag.ContinueOnError)
	var (
		dir      = fs.String("dir", "", "kurulacak dizin (boşsa bulunulan dizinin altında proje adıyla)")
		domain   = fs.String("domain", "", "alan adı (boşsa <ad>.test)")
		noAdd    = fs.Bool("no-register", false, "kayda ekleme")
		quiet    = fs.Bool("quiet", false, "kurucunun çıktısını gizle")
		listOnly = fs.Bool("list", false, "şablonları listele ve çık")
	)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Kullanım: devbox new <şablon> <ad> [seçenekler]

Yeni bir proje kurar: çerçevenin kendi kurucusunu çalıştırır, devbox.yaml
üretir ve projeyi kayda ekler.

Şablonlar için: devbox new -list

`)
		fs.PrintDefaults()
	}

	// "devbox new laravel magaza -domain x" — iki konumsal argüman.
	tmplName, rest := splitSubcommand(args, "")
	name, flags := splitNameAndFlags(rest)
	if err := fs.Parse(flags); err != nil {
		return err
	}

	if *listOnly || tmplName == "-list" {
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ŞABLON\tAÇIKLAMA\tGEREKEN ARAÇ")
		for _, t := range scaffold.Templates() {
			tool := "-"
			if t.NeedsTool() {
				tool = t.Tool
			}
			fmt.Fprintf(w, "%s\t%s\t%s\n", t.Name, t.Description, tool)
		}
		return w.Flush()
	}

	if tmplName == "" || name == "" {
		fs.Usage()
		return fmt.Errorf("şablon ve proje adı gerekli, ör. devbox new laravel magaza")
	}

	tmpl, err := scaffold.Get(tmplName)
	if err != nil {
		return err
	}

	projectName := sanitizeName(name)
	if projectName == "" {
		return fmt.Errorf("geçersiz proje adı: %q", name)
	}

	target := *dir
	if target == "" {
		target = projectName
	}
	absDir, err := filepath.Abs(target)
	if err != nil {
		return err
	}

	fmt.Printf("%s şablonundan %s kuruluyor…\n", tmpl.Name, projectName)
	if tmpl.NeedsTool() && !*quiet {
		fmt.Printf("  (%s çalıştırılıyor; ilk kurulumda birkaç dakika sürebilir)\n\n", tmpl.Tool)
	}

	// Ağdan paket indiren kurucular için cömert bir süre; yine de
	// sonsuza kadar asılı kalmasın.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	opts := scaffold.Options{Dir: absDir}
	if !*quiet {
		opts.Output = os.Stderr
	}
	if err := tmpl.Create(ctx, opts); err != nil {
		return err
	}

	// Kurulan iskeleti tanı: çerçeve kurucusu ne bıraktıysa ona göre
	// yapılandırma üret. Şablon adına güvenmiyoruz — kurucu sürüm
	// atladığında dosya düzeni değişebilir, algılama gerçeği görür.
	detected := project.Detect(absDir)
	cfg := detected.Config
	cfg.Name = projectName
	cfg.Domain = projectName + ".test"
	if *domain != "" {
		cfg.Domain = *domain
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("üretilen yapılandırma geçersiz: %w", err)
	}
	if err := cfg.Save(absDir); err != nil {
		return err
	}

	fmt.Printf("\nKuruldu   : %s\n", absDir)
	fmt.Printf("Algılanan : %s\n", detected.Framework)
	fmt.Printf("  ad        : %s\n", cfg.Name)
	fmt.Printf("  alan adı  : %s\n", cfg.Domain)
	fmt.Printf("  sunucu    : %s\n", cfg.Server)
	if cfg.Root != "" {
		fmt.Printf("  belge kökü: %s\n", cfg.Root)
	}
	for _, note := range detected.Notes {
		fmt.Printf("\n  • %s\n", note)
	}

	if !*noAdd {
		reg, err := openRegistry()
		if err != nil {
			return err
		}
		if _, err := reg.Add(absDir); err != nil {
			// Kayıt başarısız olsa da proje kuruldu; kullanıcıyı
			// yarım bir sonuçla bırakmıyoruz.
			fmt.Printf("\n  ! kayda eklenemedi: %v\n", err)
		} else {
			fmt.Printf("\n  kayda eklendi (devbox ui ile görünür)\n")
		}
	}

	fmt.Printf("\nBaşlatmak için:\n  cd %s\n  devbox up\n", target)
	return nil
}
