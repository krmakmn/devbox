package cron

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func mustParse(t *testing.T, spec string) Schedule {
	t.Helper()
	s, err := Parse(spec)
	if err != nil {
		t.Fatalf("Parse(%q): %v", spec, err)
	}
	return s
}

func at(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04:05", s, time.Local)
	if err != nil {
		panic(err)
	}
	return t
}

func TestNextRunTimes(t *testing.T) {
	cases := []struct {
		spec string
		from string
		want string
	}{
		// Laravel'in zamanlayıcısı: dakikada bir.
		{"* * * * *", "2026-03-01 10:00:30", "2026-03-01 10:01:00"},
		{"*/15 * * * *", "2026-03-01 10:00:00", "2026-03-01 10:15:00"},
		{"*/15 * * * *", "2026-03-01 10:46:00", "2026-03-01 11:00:00"},
		{"0 3 * * *", "2026-03-01 10:00:00", "2026-03-02 03:00:00"},
		{"30 2 1 * *", "2026-03-01 10:00:00", "2026-04-01 02:30:00"},
		{"0 0 * * 0", "2026-03-04 10:00:00", "2026-03-08 00:00:00"}, // pazar
		{"0 0 * * 7", "2026-03-04 10:00:00", "2026-03-08 00:00:00"}, // 7 de pazar
		{"0 0 * * sun", "2026-03-04 10:00:00", "2026-03-08 00:00:00"},
		{"0 12 * feb *", "2026-03-01 10:00:00", "2027-02-01 12:00:00"},
		{"@daily", "2026-03-01 10:00:00", "2026-03-02 00:00:00"},
		{"@hourly", "2026-03-01 10:20:00", "2026-03-01 11:00:00"},
		{"5,35 * * * *", "2026-03-01 10:10:00", "2026-03-01 10:35:00"},
		{"0 9-17 * * 1-5", "2026-03-07 12:00:00", "2026-03-09 09:00:00"}, // cumartesi → pazartesi
		// 29 Şubat: 2026 artık yıl değil, 2028 öyle.
		{"0 0 29 2 *", "2026-03-01 00:00:00", "2028-02-29 00:00:00"},
	}
	for _, c := range cases {
		got := mustParse(t, c.spec).Next(at(c.from))
		if !got.Equal(at(c.want)) {
			t.Errorf("%q, %s sonrası = %s, beklenen %s", c.spec, c.from,
				got.Format("2006-01-02 15:04:05"), c.want)
		}
	}
}

// Vixie cron kuralı: ayın günü ve haftanın günü birlikte kısıtlıysa
// "veya" gibi davranır. Bunu yanlış uygulayan bir zamanlayıcı, ayda bir
// çalışması gereken görevi hiç çalıştırmaz.
func TestDayOfMonthAndWeekdayAreOr(t *testing.T) {
	s := mustParse(t, "0 0 1 * 1") // ayın biri VEYA pazartesi

	// 2026-06-01 pazartesi; 2026-07-01 çarşamba ama ayın biri.
	for _, c := range []struct{ from, want string }{
		{"2026-06-02 00:00:00", "2026-06-08 00:00:00"}, // sonraki pazartesi
		{"2026-06-30 00:00:00", "2026-07-01 00:00:00"}, // ayın biri, çarşamba
	} {
		if got := s.Next(at(c.from)); !got.Equal(at(c.want)) {
			t.Errorf("%s sonrası = %s, beklenen %s", c.from,
				got.Format("2006-01-02 15:04:05"), c.want)
		}
	}

	// Yalnız gün-ay kısıtlıysa "ve" gibi davranmamalı: her ayın biri.
	s = mustParse(t, "0 0 1 * *")
	if got := s.Next(at("2026-06-02 00:00:00")); !got.Equal(at("2026-07-01 00:00:00")) {
		t.Errorf("yalnız ayın günü: %s", got)
	}
}

func TestParseRejectsBadSpecs(t *testing.T) {
	bad := []string{
		"", "* * * *", "* * * * * *", "60 * * * *", "* 24 * * *",
		"* * 0 * *", "* * * 13 *", "* * * * 8", "a * * * *",
		"*/0 * * * *", "5-1 * * * *", "@every 100ms", "@every abc",
		"@bilinmeyen",
	}
	for _, spec := range bad {
		if _, err := Parse(spec); err == nil {
			t.Errorf("geçersiz ifade kabul edildi: %q", spec)
		}
	}
}

func TestEverySchedule(t *testing.T) {
	s := mustParse(t, "@every 30s")
	now := at("2026-03-01 10:00:00")
	if got := s.Next(now); !got.Equal(now.Add(30 * time.Second)) {
		t.Errorf("@every 30s = %s", got)
	}
}

// hemenSchedule, testte beklemeden çalışsın diye.
type hemenSchedule struct{ gecikme time.Duration }

func (h hemenSchedule) Next(after time.Time) time.Time { return after.Add(h.gecikme) }

func TestRunnerRunsJob(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("kabuk komutu Windows'ta farklı")
	}
	dir := t.TempDir()
	iz := filepath.Join(dir, "iz.txt")

	r := &Runner{
		Logger:  slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
		WorkDir: dir,
	}
	if err := r.Add(Job{
		Name:     "iz-bırak",
		Schedule: hemenSchedule{gecikme: 20 * time.Millisecond},
		Exec:     "/bin/sh",
		Args:     []string{"-c", "echo çalıştı >> " + iz},
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(iz); err == nil && len(data) > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("zamanlanmış görev çalışmadı")
}

// Bir dakikadan uzun süren "schedule:run", bir sonraki dakikada ikinci kez
// başlatılmamalı: aynı kuyruk işi iki kez işlenir.
func TestRunnerSkipsOverlappingRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("kabuk komutu Windows'ta farklı")
	}
	dir := t.TempDir()
	sayac := filepath.Join(dir, "sayac.txt")

	r := &Runner{
		Logger:  slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
		WorkDir: dir,
	}
	// Her 20 ms'de bir tetikleniyor ama komut 1 saniye sürüyor.
	if err := r.Add(Job{
		Name:     "yavaş",
		Schedule: hemenSchedule{gecikme: 20 * time.Millisecond},
		Exec:     "/bin/sh",
		Args:     []string{"-c", "echo x >> " + sayac + "; sleep 1"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	r.Close()

	data, err := os.ReadFile(sayac)
	if err != nil {
		t.Fatalf("görev hiç çalışmadı: %v", err)
	}
	if n := len(data) / 2; n != 1 {
		t.Errorf("görev %d kez başladı, üst üste binme engellenmemiş", n)
	}

	var skipped int
	for _, s := range r.Status() {
		skipped = s.Skipped
	}
	if skipped == 0 {
		t.Error("atlanan çalıştırma sayılmamış")
	}
}

func TestRunnerRejectsBadJobs(t *testing.T) {
	r := &Runner{}
	if err := r.Add(Job{Name: "a", Exec: "/bin/true"}); err == nil {
		t.Error("zamanlaması olmayan görev kabul edildi")
	}
	if err := r.Add(Job{Name: "a", Schedule: hemenSchedule{}}); err == nil {
		t.Error("komutu olmayan görev kabul edildi")
	}
}

// Kapatma, çalışan görev süreçlerini de sonlandırmalı.
func TestCloseStopsRunner(t *testing.T) {
	r := &Runner{Logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))}
	if err := r.Add(Job{
		Name: "uzun", Schedule: hemenSchedule{gecikme: time.Hour},
		Exec: "/bin/sleep", Args: []string{"60"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- r.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close döndürmedi")
	}
}
