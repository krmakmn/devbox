package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/krmakmn/devbox/internal/database"
	"github.com/krmakmn/devbox/internal/paths"
	"github.com/krmakmn/devbox/internal/ports"
	"github.com/krmakmn/devbox/internal/supervisor"
)

func runDB(args []string) error {
	fs := flag.NewFlagSet("db", flag.ContinueOnError)
	var (
		engineName = fs.String("engine", "", "motor: postgres, mysql ya da mariadb")
		version    = fs.String("version", "", "sürüm etiketi (üstveride tutulur)")
		port       = fs.Int("port", 0, "port (0 = motorun varsayılanından başlayarak boş port)")
		binDir     = fs.String("bin", "", "motorun kurulum dizini (boşsa PATH)")
		tag        = fs.String("tag", "", "anlık görüntü etiketi")
		file       = fs.String("file", "", "dışa/içe aktarma dosyası")
	)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Kullanım: devbox db <alt komut> [seçenekler]

  create <ad> -engine postgres   yeni örnek kur
  list                           örnekleri listele
  start <ad> / stop <ad>         sunucuyu başlat / durdur
  remove <ad>                    örneği ve verisini sil (anlık görüntüler kalır)

  snapshot <ad> -tag <etiket>    veri dizininin birebir kopyasını al
  restore  <ad> -tag <etiket>    anlık görüntüye dön
  snapshots <ad>                 anlık görüntüleri listele

  export <ad> -file <yol>        SQL dökümü al (sürümler arası taşınabilir)
  import <ad> -file <yol>        SQL dosyasını uygula

Anlık görüntü ile dışa aktarma farklı işler: anlık görüntü birebir ve hızlı
ama aynı motor sürümüne bağlı; dışa aktarma taşınabilir ama birebir değil.

`)
		fs.PrintDefaults()
	}

	sub, rest := splitSubcommand(args, "list")
	name, flagArgs := splitNameAndFlags(rest)
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	// Ad bayrakların ardından da yazılmış olabilir.
	if name == "" && fs.NArg() > 0 {
		name = fs.Arg(0)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	sup, err := supervisor.New(logger)
	if err != nil {
		return err
	}
	defer sup.Close()

	m := database.NewManager(
		filepath.Join(paths.DataDir(), "db"),
		sup, ports.New("127.0.0.1"), logger,
	)
	ctx := context.Background()

	switch sub {
	case "list":
		return dbList(m)

	case "create":
		if name == "" || *engineName == "" {
			return errors.New("kullanım: devbox db create <ad> -engine postgres")
		}
		engine, err := database.ParseEngine(*engineName)
		if err != nil {
			return err
		}
		fmt.Printf("%s örneği kuruluyor…\n", name)
		inst, err := m.Create(ctx, database.Spec{
			Name: name, Engine: engine, Version: *version, Port: *port, BinDir: *binDir,
		})
		if err != nil {
			return err
		}
		fmt.Printf("kuruldu: %s (%s) → %s\n", inst.Name, inst.Engine, inst.Addr())
		fmt.Printf("veri dizini: %s\n", inst.DataDir)
		fmt.Printf("başlatmak için: devbox db start %s\n", name)
		return nil

	case "start":
		if name == "" {
			return errors.New("örnek adı gerekli")
		}
		startCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		inst, err := m.Start(startCtx, name)
		if err != nil {
			return err
		}
		fmt.Printf("%s çalışıyor: %s (kullanıcı: %s)\n", inst.Name, inst.Addr(), inst.Superuser)
		fmt.Println("not: devbox db, servisi kendi süreci altında çalıştırır; bu komut çıkınca durur.")
		fmt.Println("kalıcı çalışması için: devbox daemon")
		return nil

	case "stop":
		if name == "" {
			return errors.New("örnek adı gerekli")
		}
		return m.Stop(name)

	case "remove":
		if name == "" {
			return errors.New("örnek adı gerekli")
		}
		if err := m.Remove(name); err != nil {
			return err
		}
		fmt.Printf("%s silindi (anlık görüntüleri korundu)\n", name)
		return nil

	case "snapshot":
		if name == "" || *tag == "" {
			return errors.New("kullanım: devbox db snapshot <ad> -tag <etiket>")
		}
		snapCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
		defer cancel()
		path, err := m.Snapshot(snapCtx, name, *tag)
		if err != nil {
			return err
		}
		fmt.Printf("anlık görüntü alındı: %s\n", path)
		return nil

	case "restore":
		if name == "" || *tag == "" {
			return errors.New("kullanım: devbox db restore <ad> -tag <etiket>")
		}
		restoreCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
		defer cancel()
		if err := m.Restore(restoreCtx, name, *tag); err != nil {
			return err
		}
		fmt.Printf("%s, %q anlık görüntüsüne döndürüldü\n", name, *tag)
		return nil

	case "snapshots":
		if name == "" {
			return errors.New("örnek adı gerekli")
		}
		snaps, err := m.Snapshots(name)
		if err != nil {
			return err
		}
		if len(snaps) == 0 {
			fmt.Printf("%s için anlık görüntü yok\n", name)
			return nil
		}
		for _, s := range snaps {
			fmt.Println(s)
		}
		return nil

	case "export":
		if name == "" || *file == "" {
			return errors.New("kullanım: devbox db export <ad> -file <yol>")
		}
		expCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
		defer cancel()
		if err := m.Export(expCtx, name, *file); err != nil {
			return err
		}
		fmt.Printf("dışa aktarıldı: %s\n", *file)
		return nil

	case "import":
		if name == "" || *file == "" {
			return errors.New("kullanım: devbox db import <ad> -file <yol>")
		}
		impCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
		defer cancel()
		if err := m.Import(impCtx, name, *file); err != nil {
			return err
		}
		fmt.Printf("içe aktarıldı: %s\n", *file)
		return nil

	default:
		fs.Usage()
		return fmt.Errorf("bilinmeyen alt komut %q", sub)
	}
}

func dbList(m *database.Manager) error {
	instances, err := m.List()
	if err != nil {
		return err
	}
	if len(instances) == 0 {
		fmt.Println("kurulu veritabanı örneği yok (devbox db create <ad> -engine postgres)")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "AD\tMOTOR\tSÜRÜM\tADRES\tKULLANICI\tVERİ DİZİNİ")
	for _, inst := range instances {
		version := inst.Version
		if version == "" {
			version = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			inst.Name, inst.Engine, version, inst.Addr(), inst.Superuser, inst.DataDir)
	}
	return w.Flush()
}
