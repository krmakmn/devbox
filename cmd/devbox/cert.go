package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/krmakmn/devbox/internal/certs"
	"github.com/krmakmn/devbox/internal/paths"
)

func runCert(args []string) error {
	fs := flag.NewFlagSet("cert", flag.ContinueOnError)
	format := fs.String("format", "text", "çıktı biçimi: text ya da json")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Kullanım: devbox cert <alt komut> [seçenekler]

  list            verilmiş sertifikaları listele
  issue <alan>    sertifika üret (varsa yeniler)
  show <alan>     sertifikanın ayrıntılarını göster
  remove <alan>   sertifikayı sil (sonraki istekte yeniden üretilir)
  root            kök sertifikanın yolunu yazdır

Sertifikalar 90 gün ömürlü ve bitişine 30 gün kalınca kendiliğinden
yenileniyor; bu komutlar elle müdahale içindir.

`)
		fs.PrintDefaults()
	}

	sub, rest := splitSubcommand(args, "list")
	name, flags := splitNameAndFlags(rest)
	if err := fs.Parse(flags); err != nil {
		return err
	}

	store, err := certs.Open(paths.CertsDir())
	if err != nil {
		return fmt.Errorf("sertifika deposu açılamadı: %w", err)
	}

	switch sub {
	case "list":
		list, err := store.List()
		if err != nil {
			return err
		}
		if *format == "json" {
			return json.NewEncoder(os.Stdout).Encode(list)
		}
		if len(list) == 0 {
			fmt.Println("verilmiş sertifika yok")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ALAN ADI\tBİTİŞ\tKALAN\tDURUM")
		for _, info := range list {
			durum := "geçerli"
			if info.Expired {
				durum = "SÜRESİ DOLDU"
			} else if info.NeedsRenewal {
				durum = "yenilenecek"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
				info.Name, info.NotAfter.Format("2006-01-02"),
				time.Until(info.NotAfter).Round(24*time.Hour), durum)
		}
		return w.Flush()

	case "issue":
		if name == "" {
			fs.Usage()
			return fmt.Errorf("alan adı gerekli")
		}
		if _, err := store.Certificate(name); err != nil {
			return err
		}
		fmt.Printf("sertifika hazır: %s\n", name)
		return nil

	case "show":
		if name == "" {
			fs.Usage()
			return fmt.Errorf("alan adı gerekli")
		}
		list, err := store.List()
		if err != nil {
			return err
		}
		for _, info := range list {
			if info.Name != name {
				continue
			}
			if *format == "json" {
				return json.NewEncoder(os.Stdout).Encode(info)
			}
			fmt.Printf("Alan adı : %s\n", info.Name)
			fmt.Printf("Geçerli  : %s → %s\n",
				info.NotBefore.Format(time.RFC3339), info.NotAfter.Format(time.RFC3339))
			fmt.Printf("Kapsam   : %v\n", info.DNSNames)
			fmt.Printf("Dosya    : %s\n", info.Path)
			return nil
		}
		return fmt.Errorf("%s için sertifika yok (devbox cert issue %s)", name, name)

	case "remove":
		if name == "" {
			fs.Usage()
			return fmt.Errorf("alan adı gerekli")
		}
		if err := store.Remove(name); err != nil {
			return err
		}
		fmt.Printf("silindi: %s\n", name)
		return nil

	case "root":
		fmt.Println(store.RootCertPath())
		return nil

	default:
		fs.Usage()
		return fmt.Errorf("bilinmeyen alt komut %q", sub)
	}
}
