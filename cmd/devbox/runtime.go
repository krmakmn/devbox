package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/krmakmn/devbox/internal/paths"
	"github.com/krmakmn/devbox/internal/runtime"
)

// manifestPublicKey, runtime manifestinin imzasını doğrulayan açık anahtar
// (base64, ed25519).
//
// Şu an boş: yayın altyapısı kurulmadı, dolayısıyla imzalayacak bir özel
// anahtar da yok. Boş olduğu sürece **uzaktan manifest reddedilir** —
// doğrulanmamış bir liste, saldırganın istediği ikiliyi kurdurabilmesi
// demektir. Yerel dosyadan okunan manifestler, kullanıcının kendi diskinden
// geldiği için imza istemez.
//
// Yayın altyapısı hazır olduğunda burası doldurulacak ve manifest yanında
// .sig dosyasıyla yayımlanacak.
const manifestPublicKey = ""

func runtimeStore() *runtime.Store {
	return runtime.NewStore(filepath.Join(paths.DataDir(), "runtimes"))
}

func runRuntime(args []string) error {
	fs := flag.NewFlagSet("runtime", flag.ContinueOnError)
	manifestSrc := fs.String("manifest", "", "manifest dosyası ya da https adresi")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Kullanım: devbox runtime <alt komut> [seçenekler]

  list                 kurulu runtime'ları listele
  available            manifestteki kurulabilir sürümleri listele
  install <php@8.3>    indir, doğrula ve kur
  remove <php@8.3.14>  kurulu bir sürümü kaldır
  path <php@8.3>       kurulum dizinini yazdır
  prune                indirme önbelleğini ve yarım kalmışları temizle

`)
		fs.PrintDefaults()
	}

	sub, rest := splitSubcommand(args, "list")
	if err := fs.Parse(rest); err != nil {
		return err
	}

	store := runtimeStore()

	switch sub {
	case "list":
		return runtimeList(store)
	case "available":
		return runtimeAvailable(*manifestSrc)
	case "install":
		return runtimeInstall(store, *manifestSrc, fs.Args())
	case "remove":
		return runtimeRemove(store, fs.Args())
	case "path":
		return runtimePath(store, fs.Args())
	case "prune":
		return runtimePrune(store)
	default:
		fs.Usage()
		return fmt.Errorf("bilinmeyen alt komut %q", sub)
	}
}

func runtimeList(store *runtime.Store) error {
	installed, err := store.List()
	if err != nil {
		return err
	}
	if len(installed) == 0 {
		fmt.Println("kurulu runtime yok (devbox runtime install php@8.3)")
		return nil
	}
	for _, inst := range installed {
		fmt.Printf("%-24s %s\n", inst.Package.ID(), inst.Dir)
	}
	return nil
}

func runtimeAvailable(src string) error {
	m, err := loadManifest(src)
	if err != nil {
		return err
	}
	for _, name := range m.Names() {
		pkgs := m.Find(name, "", goos(), goarch())
		if len(pkgs) == 0 {
			continue
		}
		versions := make([]string, 0, len(pkgs))
		for _, p := range pkgs {
			versions = append(versions, p.Version)
		}
		fmt.Printf("%-10s %s\n", name, strings.Join(versions, ", "))
	}
	return nil
}

func runtimeInstall(store *runtime.Store, src string, args []string) error {
	if len(args) == 0 {
		return errors.New("kurulacak runtime belirtilmeli, ör. devbox runtime install php@8.3")
	}
	m, err := loadManifest(src)
	if err != nil {
		return err
	}

	for _, spec := range args {
		pkg, err := m.Select(spec)
		if err != nil {
			return err
		}
		fmt.Printf("%s kuruluyor…\n", pkg.ID())

		inst, err := store.Install(context.Background(), pkg, progressPrinter(pkg.ID()))
		if err != nil {
			return err
		}
		fmt.Printf("\r%s kuruldu: %s\n", pkg.ID(), inst.Dir)
		if pkg.Notes != "" {
			fmt.Println("  not:", pkg.Notes)
		}
	}
	return nil
}

// progressPrinter, indirme ilerlemesini tek satırda gösterir.
func progressPrinter(id string) func(runtime.Progress) {
	return func(p runtime.Progress) {
		if pct := p.Percent(); pct >= 0 {
			fmt.Printf("\r  %s indiriliyor: %%%.0f (%s)", id, pct, humanSize(p.Downloaded))
			return
		}
		fmt.Printf("\r  %s indiriliyor: %s", id, humanSize(p.Downloaded))
	}
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

func runtimeRemove(store *runtime.Store, args []string) error {
	if len(args) == 0 {
		return errors.New("kaldırılacak runtime belirtilmeli, ör. devbox runtime remove php@8.3.14")
	}
	for _, spec := range args {
		inst, ok, err := store.Resolve(spec)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%s kurulu değil", spec)
		}
		if err := store.Remove(inst.Package.Name, inst.Package.Version); err != nil {
			return err
		}
		fmt.Printf("%s kaldırıldı\n", inst.Package.ID())
	}
	return nil
}

func runtimePath(store *runtime.Store, args []string) error {
	if len(args) == 0 {
		return errors.New("runtime belirtilmeli, ör. devbox runtime path php@8.3")
	}
	inst, ok, err := store.Resolve(args[0])
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s kurulu değil", args[0])
	}
	fmt.Println(inst.Dir)
	return nil
}

func runtimePrune(store *runtime.Store) error {
	if err := store.PruneStale(time.Hour); err != nil {
		return err
	}
	freed, err := store.PruneCache()
	if err != nil {
		return err
	}
	fmt.Printf("%s serbest bırakıldı\n", humanSize(freed))
	return nil
}

// loadManifest, manifesti yerel dosyadan ya da https adresinden okur.
func loadManifest(src string) (*runtime.Manifest, error) {
	if src == "" {
		return nil, errors.New("-manifest ile bir manifest dosyası ya da adresi verin\n" +
			"(gömülü varsayılan manifest henüz yok; yayın altyapısı kurulunca gelecek)")
	}

	pub, err := manifestKey()
	if err != nil {
		return nil, err
	}

	if strings.HasPrefix(src, "https://") {
		if len(pub) == 0 {
			return nil, errors.New(
				"uzaktan manifest reddedildi: imza doğrulaması için açık anahtar yapılandırılmamış\n" +
					"doğrulanmamış bir manifest, istenmeyen bir ikilinin kurulmasına yol açabilir\n" +
					"manifesti indirip -manifest <dosya> ile verin")
		}
		return fetchManifest(src, pub)
	}
	if strings.HasPrefix(src, "http://") {
		return nil, errors.New("manifest adresi https olmalı")
	}

	data, err := os.ReadFile(src)
	if err != nil {
		return nil, fmt.Errorf("manifest okunamadı: %w", err)
	}
	// Yerel dosyada imza varsa doğrulanır, yoksa dosya kullanıcının kendi
	// diskinden geldiği için imza aranmaz.
	sig, _ := os.ReadFile(src + ".sig")
	if len(sig) == 0 {
		pub = nil
	}
	return runtime.ParseManifest(data, sig, pub)
}

func fetchManifest(url string, pub ed25519.PublicKey) (*runtime.Manifest, error) {
	data, err := fetch(url)
	if err != nil {
		return nil, err
	}
	sig, err := fetch(url + ".sig")
	if err != nil {
		return nil, fmt.Errorf("manifest imzası alınamadı: %w", err)
	}
	return runtime.ParseManifest(data, sig, pub)
}

func fetch(url string) ([]byte, error) {
	resp, err := runtime.DefaultClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s → %s", url, resp.Status)
	}
	// Manifest küçük bir dosya; sınırsız okumak bellek tüketimi için açık kapı.
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

func manifestKey() (ed25519.PublicKey, error) {
	if manifestPublicKey == "" {
		return nil, nil
	}
	raw, err := base64.StdEncoding.DecodeString(manifestPublicKey)
	if err != nil {
		return nil, fmt.Errorf("gömülü açık anahtar çözülemedi: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, errors.New("gömülü açık anahtarın boyutu geçersiz")
	}
	return ed25519.PublicKey(raw), nil
}
