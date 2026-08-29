package projects

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/krmakmn/devbox/internal/supervisor"
)

// ServicePrefix, projelerin denetçideki servis adlarının ön eki.
//
// Ön ek olmasaydı "web" adlı bir proje ile kullanıcının -service ile
// eklediği "web" servisi çakışırdı.
const ServicePrefix = "proje-"

// ServiceName, projenin denetçideki servis adı.
func ServiceName(project string) string { return ServicePrefix + project }

// Runner, projeleri "devbox up" alt süreçleri olarak çalıştırır.
//
// # Neden ayrı süreç, neden kütüphane çağrısı değil
//
// "devbox up"ın yaptığı iş (PHP havuzu, web sunucusu, kenar proxy, DNS,
// posta, servisler) doğrudan çağrılabilirdi. Ayrı süreç üç şey kazandırıyor:
// çöken bir proje çekirdeği düşürmüyor; her projenin günlüğü kendi halka
// tamponunda ayrı duruyor; ve komut satırından çalıştırılan "devbox up" ile
// arayüzden başlatılan proje birebir aynı kodu çalıştırıyor — yani arayüzde
// çalışıp CLI'da çalışmayan (ya da tersi) bir durum oluşmuyor.
//
// Bedeli: başlatma birkaç yüz milisaniye daha uzun ve süreçler arası bir
// sözleşme (çıktıdaki hazır satırı) oluşuyor. İkisi de kabul edilebilir.
type Runner struct {
	// Registry, proje listesi.
	Registry *Registry

	// Supervisor, süreçleri yöneten denetçi.
	Supervisor *supervisor.Supervisor

	// Executable, "devbox" çalıştırılabilirinin yolu.
	Executable string

	// ExtraArgs, her "devbox up" çağrısına eklenecek argümanlar
	// (testlerde port değiştirmek için).
	ExtraArgs []string

	Logger *slog.Logger

	mu       sync.Mutex
	services map[string]*supervisor.Service
}

// ReadyLine, "devbox up"ın hazır olduğunda yazdığı satırın parçası.
//
// Süreçler arası sözleşme: bu metin değişirse arayüz projeyi asla "hazır"
// göremez. up.go'daki yazdırma ile birlikte değişmeli; testi de bunu
// doğruluyor.
const ReadyLine = " hazır: https://"

// Status, bir projenin arayüze dönen durumu.
type Status struct {
	Project

	// Running, projenin ayakta olup olmadığı.
	Running bool `json:"running"`

	// State, denetçinin gördüğü durum ("çalışıyor", "durdu"...).
	State string `json:"state,omitempty"`

	// PID, süreç kimliği.
	PID int `json:"pid,omitempty"`

	// Since, bu duruma ne zaman geçtiği.
	Since time.Time `json:"since,omitempty"`

	// Restarts, kaç kez yeniden başlatıldığı.
	Restarts int `json:"restarts,omitempty"`

	// ServiceName, günlük uç noktalarında kullanılacak ad.
	ServiceName string `json:"serviceName"`
}

// Statuses, tüm projelerin durumunu döner.
func (r *Runner) Statuses() ([]Status, error) {
	list, err := r.Registry.List()
	if err != nil {
		return nil, err
	}
	out := make([]Status, 0, len(list))
	for _, p := range list {
		out = append(out, r.status(p))
	}
	return out, nil
}

// Status, tek bir projenin durumunu döner.
func (r *Runner) Status(name string) (Status, error) {
	p, ok := r.Registry.Get(name)
	if !ok {
		return Status{}, fmt.Errorf("projects: kayıtlı proje bulunamadı: %q", name)
	}
	return r.status(p), nil
}

func (r *Runner) status(p Project) Status {
	st := Status{Project: p, ServiceName: ServiceName(p.Name)}

	r.mu.Lock()
	svc := r.services[p.Name]
	r.mu.Unlock()
	if svc == nil {
		return st
	}

	s := svc.Status()
	st.State = string(s.State)
	st.PID = s.PID
	st.Since = s.Since
	st.Restarts = s.Restarts
	st.Running = s.PID != 0
	return st
}

// Start, projeyi ayağa kaldırır.
func (r *Runner) Start(ctx context.Context, name string) (Status, error) {
	p, ok := r.Registry.Get(name)
	if !ok {
		return Status{}, fmt.Errorf("projects: kayıtlı proje bulunamadı: %q", name)
	}
	if p.Missing {
		return Status{}, fmt.Errorf("projects: %s dizini bulunamadı: %s", name, p.Dir)
	}
	if p.Error != "" {
		return Status{}, fmt.Errorf("projects: %s yapılandırması okunamıyor: %s", name, p.Error)
	}
	if r.Executable == "" {
		return Status{}, fmt.Errorf("projects: devbox çalıştırılabilirinin yolu bilinmiyor")
	}

	svc, err := r.service(p)
	if err != nil {
		return Status{}, err
	}
	if err := svc.Start(ctx); err != nil {
		return Status{}, fmt.Errorf("projects: %s başlatılamadı: %w", name, err)
	}
	return r.status(p), nil
}

// Stop, projeyi durdurur.
func (r *Runner) Stop(name string) (Status, error) {
	p, ok := r.Registry.Get(name)
	if !ok {
		return Status{}, fmt.Errorf("projects: kayıtlı proje bulunamadı: %q", name)
	}
	r.mu.Lock()
	svc := r.services[name]
	r.mu.Unlock()
	if svc != nil {
		svc.Stop()
	}
	return r.status(p), nil
}

// Logs, projenin günlüğünü döner.
func (r *Runner) Logs(name string) (*supervisor.LogBuffer, bool) {
	r.mu.Lock()
	svc := r.services[name]
	r.mu.Unlock()
	if svc == nil {
		return nil, false
	}
	return svc.Logs(), true
}

// service, projenin denetçi servisini bulur ya da oluşturur.
func (r *Runner) service(p Project) (*supervisor.Service, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if svc, ok := r.services[p.Name]; ok {
		return svc, nil
	}

	args := append([]string{"up", "-dir", p.Dir}, r.ExtraArgs...)
	svc, err := r.Supervisor.Add(supervisor.Config{
		Name:    ServiceName(p.Name),
		Exec:    r.Executable,
		Args:    args,
		WorkDir: p.Dir,
		// Hazır olma ölçütü "devbox up"ın kendi çıktısı: TCP denetimi
		// yanıltıcı olurdu, çünkü kenar proxy'nin portu daha proje
		// hazır olmadan açılıyor.
		Ready:        supervisor.LogReady{Substring: ReadyLine},
		StartTimeout: 90 * time.Second,
		StopTimeout:  20 * time.Second,
		// Çöken bir proje ayağa kaldırılmayı denemeli: sebep çoğu zaman
		// geçici (port henüz bırakılmamış, veritabanı açılıyor).
		Restart:       supervisor.RestartAlways,
		HealthyUptime: 20 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	if r.services == nil {
		r.services = make(map[string]*supervisor.Service)
	}
	r.services[p.Name] = svc
	return svc, nil
}
