package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/krmakmn/devbox/internal/migrate"
	"github.com/krmakmn/devbox/internal/project"
)

func runMigrate(args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	var (
		from  = fs.String("from", "", "kurulum kökü (boşsa olağan yerlerde aranır)")
		apply = fs.Bool("apply", false, "devbox.yaml dosyalarını gerçekten yaz")
		add   = fs.Bool("register", true, "göç eden projeleri kayda ekle")
		only  = fs.String("only", "", "yalnız bu adı taşıyan siteyi taşı")
		force = fs.Bool("force", false, "var olan devbox.yaml'ın üzerine yaz")
	)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Kullanım: devbox migrate [seçenekler]

Laragon, XAMPP ya da WAMP kurulumundaki siteleri bulur ve her biri için
devbox.yaml üretir.

Öntanımlı olarak yalnız NE YAPACAĞINI YAZAR, hiçbir şey değiştirmez.
Uygulamak için -apply verin.

  devbox migrate                        ne bulduğunu göster
  devbox migrate -apply                 devbox.yaml'ları yaz ve kayda ekle
  devbox migrate -from D:\laragon -apply

Var olan kurulumunuza dokunulmaz: Laragon'un yapılandırması, sanal
konakları ve dosyaları olduğu gibi kalır. Proje dizinleri de
kopyalanmaz; DevBox onları bulundukları yerden sunar.

`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	var kurulumlar []migrate.Installation
	if *from != "" {
		kurulumlar = migrate.Detect(*from)
		if len(kurulumlar) == 0 {
			return fmt.Errorf("%s içinde site bulunamadı", *from)
		}
	} else {
		kurulumlar = migrate.Detect()
		if len(kurulumlar) == 0 {
			return fmt.Errorf("Laragon, XAMPP ya da WAMP kurulumu bulunamadı.\n" +
				"  Kökü elle verin: devbox migrate -from C:\\laragon")
		}
	}

	toplam := 0
	for _, k := range kurulumlar {
		fmt.Printf("\n%s: %s (%d site)\n\n", strings.ToUpper(string(k.Source)), k.Root, len(k.Sites))

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "  PROJE\tALAN ADI\tBELGE KÖKÜ\tDİZİN")
		for _, site := range k.Sites {
			if *only != "" && site.Name != *only {
				continue
			}
			domain, aliases := migrate.DomainFor(site)
			kok := site.DocumentRoot
			if kok == "" {
				kok = "."
			}
			ad := domain
			if len(aliases) > 0 {
				ad += " (+" + strings.Join(aliases, ", ") + ")"
			}
			fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n", site.Name, ad, kok, site.Dir)
			toplam++
		}
		w.Flush()
	}

	if toplam == 0 {
		fmt.Println("\ntaşınacak site bulunamadı")
		return nil
	}

	if !*apply {
		fmt.Printf("\n%d site bulundu. Hiçbir şey değiştirilmedi.\n", toplam)
		fmt.Println("Uygulamak için: devbox migrate -apply")
		return nil
	}

	fmt.Println()
	reg, err := openRegistry()
	if err != nil {
		return err
	}

	yazilan, atlanan := 0, 0
	for _, k := range kurulumlar {
		for _, site := range k.Sites {
			if *only != "" && site.Name != *only {
				continue
			}
			yol := filepath.Join(site.Dir, project.FileName)
			if _, err := os.Stat(yol); err == nil && !*force {
				fmt.Printf("  atlandı  : %s (%s zaten var)\n", site.Name, project.FileName)
				atlanan++
				continue
			}

			cfg, notlar := configFor(site)
			if err := cfg.Validate(); err != nil {
				fmt.Printf("  atlandı  : %s (%v)\n", site.Name, err)
				atlanan++
				continue
			}
			if err := cfg.Save(site.Dir); err != nil {
				fmt.Printf("  atlandı  : %s (%v)\n", site.Name, err)
				atlanan++
				continue
			}
			fmt.Printf("  yazıldı  : %s → %s (%s)\n", site.Name, yol, cfg.Domain)
			for _, not := range notlar {
				fmt.Printf("             • %s\n", not)
			}
			yazilan++

			if *add {
				if _, err := reg.Add(site.Dir); err != nil {
					fmt.Printf("             ! kayda eklenemedi: %v\n", err)
				}
			}
		}
	}

	fmt.Printf("\n%d site taşındı, %d atlandı.\n", yazilan, atlanan)
	if yazilan > 0 {
		fmt.Println("\nSıradaki adımlar:")
		fmt.Println("  devbox trust install     kök sertifikayı güven depolarına kur")
		fmt.Println("  devbox dns install       *.test çözümlemesini aç")
		fmt.Println("  devbox ui                projeleri panelden başlat")
	}
	return nil
}

// configFor, bulunan siteden bir yapılandırma üretir.
//
// Algılama önce çalışıyor: kaynaktaki bilgi (belge kökü, alan adı)
// üstüne yazılıyor. Böylece Laravel'in kuyruk notu gibi çerçeveye özgü
// öneriler de geliyor.
func configFor(site migrate.Site) (*project.Config, []string) {
	detected := project.Detect(site.Dir)
	cfg := detected.Config

	cfg.Name = site.Name
	domain, aliases := migrate.DomainFor(site)
	cfg.Domain = domain
	cfg.Aliases = uniqueStrings(append(cfg.Aliases, aliases...))

	if site.DocumentRoot != "" {
		cfg.Root = site.DocumentRoot
	}

	notlar := append([]string(nil), detected.Notes...)
	notlar = append(notlar, site.Notes...)
	if len(aliases) > 0 {
		notlar = append(notlar,
			"Eski alan adı takma ad olarak korundu: "+strings.Join(aliases, ", "))
	}
	return cfg, notlar
}

func uniqueStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
