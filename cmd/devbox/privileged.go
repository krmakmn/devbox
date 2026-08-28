package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/krmakmn/devbox/internal/elevate"
	"github.com/krmakmn/devbox/internal/hostsfile"
	"github.com/krmakmn/devbox/internal/nrpt"
)

// runPrivileged, yönetici hakkı gerektiren işlemleri yürütür.
//
// Bu komut normalde kullanıcı tarafından elle çağrılmaz: devbox kendini
// yükseltilmiş olarak yeniden başlatırken çağırır. Yine de her işlem
// girdisini burada yeniden doğruluyor — çağıran kim olursa olsun.
//
// Kasıtlı olarak yok: "şu komutu çalıştır" tarzı genel bir işlem. Böyle bir
// uç nokta, yükseltilmiş bir süreçte keyfi kod çalıştırma demektir.
func runPrivileged(args []string) error {
	sub, rest := splitSubcommand(args, "")
	if sub == "" {
		printPrivilegedUsage()
		return errors.New("işlem belirtilmedi")
	}

	if !elevate.IsElevated() {
		return fmt.Errorf("%q yönetici hakkı gerektiriyor; bu komut doğrudan çağrılmamalı", sub)
	}

	switch sub {
	case "dns-install":
		return privilegedDNSInstall(rest)
	case "dns-uninstall":
		return privilegedDNSUninstall(rest)
	case "hosts-apply":
		return privilegedHostsApply(rest)
	case "hosts-remove":
		return hostsfile.Remove(hostsfile.Path())
	default:
		printPrivilegedUsage()
		return fmt.Errorf("bilinmeyen işlem %q", sub)
	}
}

func printPrivilegedUsage() {
	fmt.Fprint(os.Stderr, `Kullanım: devbox privileged <işlem> [seçenekler]

Yönetici hakkı gerektiren işlemler. Normalde devbox bunları kendini
yükselterek çağırır; elle çağırmanız gerekmez.

  dns-install    NRPT kuralı ekle
  dns-uninstall  NRPT kuralını kaldır
  hosts-apply    hosts dosyasındaki DevBox bloğunu yaz
  hosts-remove   hosts dosyasındaki DevBox bloğunu kaldır
`)
}

func privilegedDNSInstall(args []string) error {
	fs := flag.NewFlagSet("dns-install", flag.ContinueOnError)
	namespace := fs.String("namespace", "", "ad alanı, ör. .test")
	server := fs.String("server", "", "çözücünün IP adresi")
	if err := fs.Parse(args); err != nil {
		return err
	}

	rule := nrpt.Rule{
		Namespace: *namespace,
		Servers:   []string{*server},
		Comment:   nrpt.DefaultComment,
	}
	// Doğrulama burada da yapılıyor: bu süreç yönetici hakkıyla koşuyor,
	// girdiye güvenmek için hiçbir sebep yok.
	if err := rule.Validate(); err != nil {
		return err
	}
	return nrpt.Add(rule)
}

func privilegedDNSUninstall(args []string) error {
	fs := flag.NewFlagSet("dns-uninstall", flag.ContinueOnError)
	namespace := fs.String("namespace", "", "ad alanı, ör. .test")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return nrpt.Remove(*namespace)
}

func privilegedHostsApply(args []string) error {
	fs := flag.NewFlagSet("hosts-apply", flag.ContinueOnError)
	var entries serviceList // "IP=ad1,ad2" biçimini toplamak için yeniden kullanılıyor
	fs.Var(&entries, "entry", "IP=ad1,ad2 (birden çok kez verilebilir)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(entries) == 0 {
		return errors.New("en az bir -entry gerekli")
	}

	var parsed []hostsfile.Entry
	for _, spec := range entries {
		ip, names, _ := strings.Cut(spec, "=")
		list := splitList(names)
		if len(list) == 0 {
			return fmt.Errorf("%q için alan adı yok", ip)
		}
		for _, name := range list {
			if !validHostName(name) {
				return fmt.Errorf("geçersiz alan adı: %q", name)
			}
		}
		parsed = append(parsed, hostsfile.Entry{IP: strings.TrimSpace(ip), Names: list})
	}
	return hostsfile.Apply(hostsfile.Path(), parsed)
}

// validHostName, hosts dosyasına yazılacak adı denetler.
//
// Dosyaya satır yazıyoruz; boşluk ya da satır sonu içeren bir ad, dosyaya
// istediğimiz dışında girdi eklemek demek.
func validHostName(name string) bool {
	if name == "" || len(name) > 253 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.' || c == '-' || c == '_':
		default:
			return false
		}
	}
	return !strings.Contains(name, "..")
}

// elevateFor, işlemi yükselterek çalıştırır; zaten yükseltilmişse doğrudan
// yürütür.
func elevateFor(args []string, run func() error) error {
	if elevate.IsElevated() {
		return run()
	}
	if runtime.GOOS != "windows" {
		return fmt.Errorf("bu işlem yönetici hakkı gerektiriyor; komutu yükseltilmiş bir kabuktan çalıştırın")
	}

	fmt.Println("Bu işlem yönetici hakkı gerektiriyor; Windows onay isteyecek.")
	err := elevate.Relaunch(append([]string{"privileged"}, args...))
	if errors.Is(err, elevate.ErrDeclined) {
		return errors.New("yükseltme reddedildi; işlem yapılmadı")
	}
	return err
}
