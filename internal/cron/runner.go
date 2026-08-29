package cron

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/krmakmn/devbox/internal/proc"
)

// Job, zamanlanmış tek bir görev.
type Job struct {
	// Name, günlüklerde görünen ad.
	Name string

	// Schedule, ne zaman çalışacağı.
	Schedule Schedule

	// Exec ve Args, çalıştırılacak komut.
	Exec string
	Args []string
}

// Runner, görevleri zamanında çalıştırır.
//
// # Üst üste binme
//
// Bir çalıştırma bitmeden sırası gelen ikinci çalıştırma atlanıyor.
// Laravel'in "schedule:run"u dakikada bir çağrılır ve zaman zaman bir
// dakikadan uzun sürer; onu paralel başlatmak, aynı kuyruk işini iki kez
// işlemek gibi gerçek hasarlara yol açar. Sistem cron'u da aynı şeyi
// yapmaz ama orada kullanıcı flock ile korunur; burada varsayılan
// güvenli olan.
type Runner struct {
	// Logger, çalıştırma günlüğü.
	Logger *slog.Logger

	// WorkDir, komutların çalışacağı dizin.
	WorkDir string

	// Env, komutlara verilecek ortam değişkenleri.
	Env []string

	mu      sync.Mutex
	jobs    []*jobState
	group   *proc.Group
	stop    chan struct{}
	done    chan struct{}
	started bool
}

type jobState struct {
	Job
	running bool
	runs    int
	skipped int
	last    time.Time
}

// Add, bir görev ekler. Start'tan önce çağrılmalı.
func (r *Runner) Add(j Job) error {
	if j.Schedule == nil {
		return fmt.Errorf("%s: zamanlama yok", j.Name)
	}
	if j.Exec == "" {
		return fmt.Errorf("%s: komut yok", j.Name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return fmt.Errorf("çalışan zamanlayıcıya görev eklenemez")
	}
	r.jobs = append(r.jobs, &jobState{Job: j})
	return nil
}

// Start, zamanlayıcıyı arka planda çalıştırır.
func (r *Runner) Start(ctx context.Context) error {
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return fmt.Errorf("zamanlayıcı zaten çalışıyor")
	}
	if len(r.jobs) == 0 {
		r.mu.Unlock()
		return nil
	}
	// Görev süreçleri tek bir grupta: DevBox kapanırken yarım kalan bir
	// "artisan schedule:run" arkada kalmasın.
	group, err := proc.NewGroup()
	if err != nil {
		r.mu.Unlock()
		return err
	}
	r.group = group
	r.started = true
	r.stop = make(chan struct{})
	r.done = make(chan struct{})
	stop, done := r.stop, r.done
	r.mu.Unlock()

	go r.loop(ctx, stop, done)
	return nil
}

// loop, sıradaki görevi bekler ve çalıştırır.
func (r *Runner) loop(ctx context.Context, stop, done chan struct{}) {
	defer close(done)

	timer := time.NewTimer(time.Hour)
	defer timer.Stop()

	for {
		now := time.Now()
		next, when := r.nextJob(now)
		if next == nil {
			return // hiçbir görev bir daha çalışmayacak
		}

		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(when.Sub(now))

		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-timer.C:
			r.fire(ctx, next, when)
		}
	}
}

// nextJob, en yakın çalışma zamanı olan görevi bulur.
func (r *Runner) nextJob(now time.Time) (*jobState, time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var best *jobState
	var bestTime time.Time
	for _, j := range r.jobs {
		t := j.Schedule.Next(now)
		if t.IsZero() {
			continue
		}
		if best == nil || t.Before(bestTime) {
			best, bestTime = j, t
		}
	}
	return best, bestTime
}

func (r *Runner) fire(ctx context.Context, j *jobState, at time.Time) {
	r.mu.Lock()
	if j.running {
		j.skipped++
		skipped := j.skipped
		r.mu.Unlock()
		r.logger().Warn("zamanlanmış görev atlandı: öncekisi hâlâ çalışıyor",
			"görev", j.Name, "atlanan", skipped)
		return
	}
	j.running = true
	j.runs++
	j.last = at
	group := r.group
	r.mu.Unlock()

	go func() {
		defer func() {
			r.mu.Lock()
			j.running = false
			r.mu.Unlock()
		}()

		cmd := exec.CommandContext(ctx, j.Exec, j.Args...)
		cmd.Dir = r.WorkDir
		cmd.Env = append(os.Environ(), r.Env...)

		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out

		start := time.Now()
		// Süreç, başlatıldıktan sonra gruba katılıyor: Windows'ta iş
		// nesnesine atama ancak süreç tutamağı varken yapılabiliyor.
		if group != nil {
			group.Prepare(cmd)
		}
		err := cmd.Start()
		if err == nil {
			if group != nil {
				if addErr := group.Add(cmd); addErr != nil {
					r.logger().Warn("zamanlanmış görev süreç grubuna eklenemedi",
						"görev", j.Name, "hata", addErr)
				}
			}
			err = cmd.Wait()
		}
		if err != nil {
			r.logger().Error("zamanlanmış görev başarısız",
				"görev", j.Name, "süre", time.Since(start), "hata", err,
				"çıktı", trim(out.Bytes()))
			return
		}
		r.logger().Info("zamanlanmış görev çalıştı",
			"görev", j.Name, "süre", time.Since(start), "çıktı", trim(out.Bytes()))
	}()
}

// Status, görevlerin o anki durumu.
type Status struct {
	Name    string
	Running bool
	Runs    int
	Skipped int
	Last    time.Time
}

// Status, görev sayaçlarını döner.
func (r *Runner) Status() []Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Status, 0, len(r.jobs))
	for _, j := range r.jobs {
		out = append(out, Status{
			Name: j.Name, Running: j.running, Runs: j.runs,
			Skipped: j.skipped, Last: j.last,
		})
	}
	return out
}

// Close, zamanlayıcıyı durdurur ve çalışan görev süreçlerini kapatır.
func (r *Runner) Close() error {
	r.mu.Lock()
	if !r.started {
		r.mu.Unlock()
		return nil
	}
	r.started = false
	stop, done, group := r.stop, r.done, r.group
	r.group = nil
	r.mu.Unlock()

	close(stop)
	<-done
	if group != nil {
		return group.Close()
	}
	return nil
}

func (r *Runner) logger() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.Default()
}

// trim, günlüğe düşen çıktıyı kısaltır: bir görev megabaytlarca çıktı
// üretebilir, terminali doldurmasın.
func trim(out []byte) string {
	const max = 2000
	s := string(out)
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
