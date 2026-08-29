package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/krmakmn/devbox/internal/mail"
)

func runMail(args []string) error {
	fs := flag.NewFlagSet("mail", flag.ContinueOnError)
	var (
		smtpAddr = fs.String("smtp", mail.DefaultSMTPAddr, "SMTP dinleme adresi")
		httpAddr = fs.String("http", "127.0.0.1:8025", "web arayüzü adresi")
		capacity = fs.Int("capacity", mail.DefaultCapacity, "saklanacak en fazla posta")
		format   = fs.String("format", "text", "çıktı biçimi: text ya da json")
		query    = fs.String("q", "", "arama sorgusu (konu, adres, gövde, ek adı)")
	)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Kullanım: devbox mail <alt komut> [seçenekler]

  serve    posta yakalayıcıyı çalıştır (Ctrl+C ile durur)
  list     yakalanan postaları listele (-q ile ara)
  latest   son postayı göster
  clear    tüm postaları sil

Uygulamanızı bu SMTP adresine yönlendirin; posta dışarı çıkmaz, web
arayüzünde görünür. Kullanıcı adı ve parola sorulursa herhangi bir değer
verebilirsiniz.

`)
		fs.PrintDefaults()
	}

	sub, rest := splitSubcommand(args, "serve")
	if err := fs.Parse(rest); err != nil {
		return err
	}

	switch sub {
	case "serve":
		return mailServe(*smtpAddr, *httpAddr, *capacity)
	case "list":
		return mailList(*httpAddr, *format, *query)
	case "latest":
		return mailLatest(*httpAddr, *format)
	case "clear":
		return mailClear(*httpAddr)
	default:
		fs.Usage()
		return fmt.Errorf("bilinmeyen alt komut %q", sub)
	}
}

func mailServe(smtpAddr, httpAddr string, capacity int) error {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	store := mail.NewStore(capacity)

	smtpSrv := &mail.SMTPServer{Addr: smtpAddr, Store: store, Logger: logger}
	if err := smtpSrv.Start(); err != nil {
		return err
	}
	defer smtpSrv.Close()

	web := &http.Server{
		Addr:              httpAddr,
		Handler:           &mail.Handler{Store: store, SMTPAddr: smtpSrv.ListenAddr()},
		ReadHeaderTimeout: 15 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		if err := web.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	fmt.Printf("\n  Posta yakalayıcı hazır\n\n")
	fmt.Printf("  SMTP    : %s\n", smtpSrv.ListenAddr())
	fmt.Printf("  Arayüz  : http://%s\n\n", httpAddr)
	fmt.Printf("  Uygulamanızın posta ayarları:\n")
	fmt.Printf("    MAIL_HOST=%s\n", strings.Split(smtpSrv.ListenAddr(), ":")[0])
	fmt.Printf("    MAIL_PORT=%s\n", strings.Split(smtpSrv.ListenAddr(), ":")[1])
	fmt.Printf("    MAIL_USERNAME ve MAIL_PASSWORD: herhangi bir değer\n\n")
	fmt.Println("  Ctrl+C ile durdurun.")

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errc:
		return err
	case <-stop:
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	web.Shutdown(ctx)
	return nil
}

func mailList(httpAddr, format, query string) error {
	var summaries []mail.Summary
	path := "/api/messages"
	if query != "" {
		path += "?q=" + url.QueryEscape(query)
	}
	if err := mailAPI(httpAddr, path, &summaries); err != nil {
		return err
	}
	if format == "json" {
		return json.NewEncoder(os.Stdout).Encode(summaries)
	}
	if len(summaries) == 0 {
		if query != "" {
			fmt.Printf("%q aramasına uyan posta yok\n", query)
		} else {
			fmt.Println("yakalanmış posta yok")
		}
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ZAMAN\tKİMDEN\tKİME\tKONU")
	for _, s := range summaries {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			s.Received.Format("15:04:05"), s.From, strings.Join(s.To, ", "), s.Subject)
	}
	return w.Flush()
}

func mailLatest(httpAddr, format string) error {
	var msg mail.Message
	if err := mailAPI(httpAddr, "/api/latest", &msg); err != nil {
		return err
	}
	if format == "json" {
		return json.NewEncoder(os.Stdout).Encode(msg)
	}

	fmt.Printf("Kimden : %s\n", msg.From)
	fmt.Printf("Kime   : %s\n", strings.Join(msg.To, ", "))
	fmt.Printf("Konu   : %s\n", msg.Subject)
	fmt.Printf("Zaman  : %s\n", msg.Received.Format(time.RFC3339))
	if len(msg.Attachments) > 0 {
		names := make([]string, 0, len(msg.Attachments))
		for _, a := range msg.Attachments {
			names = append(names, a.Filename)
		}
		fmt.Printf("Ekler  : %s\n", strings.Join(names, ", "))
	}
	fmt.Println()
	if msg.Text != "" {
		fmt.Println(msg.Text)
	} else if msg.HTML != "" {
		fmt.Println("(yalnız HTML gövde; tamamı için web arayüzüne bakın)")
	}
	return nil
}

func mailClear(httpAddr string) error {
	req, err := http.NewRequest(http.MethodDelete, "http://"+httpAddr+"/api/messages", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return mailUnreachable(err)
	}
	defer resp.Body.Close()
	fmt.Println("tüm postalar silindi")
	return nil
}

func mailAPI(httpAddr, path string, out any) error {
	resp, err := http.Get("http://" + httpAddr + path)
	if err != nil {
		return mailUnreachable(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return errors.New("henüz posta yok")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("posta arayüzü %s döndü", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func mailUnreachable(err error) error {
	return fmt.Errorf("%w\n(posta yakalayıcı çalışmıyor olabilir: devbox mail serve)", err)
}
