package projects

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/krmakmn/devbox/internal/edge"
	"github.com/krmakmn/devbox/internal/procstat"
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

	// Edge, paylaşılan kenar. Verilirse projeler 80/443'ü kendileri
	// açmaz: her biri işleyicisini loopback'te sunar, biz de alan
	// adlarını burada bu adrese yönlendiririz.
	//
	// Bu, aynı anda birden çok projenin çalışabilmesinin tek yolu —
	// her proje kendi kenarını açsaydı ikincisi 80'i alamazdı.
	Edge *edge.Edge

	Logger *slog.Logger

	mu       sync.Mutex
	services map[string]*supervisor.Service
	// routes, her proje için kenara kaydettiğimiz alan adları. Durdurma
	// ve yeniden başlatmada eski kayıtları kaldırabilmek için tutuluyor;
	// aksi hâlde kapalı bir projenin alan adı kenarda asılı kalır ve
	// istekler ölü bir adrese gider.
	routes map[string][]string
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

	// RSS ve CPUSeconds, "devbox up" sürecinin kaynak kullanımı.
	//
	// Yalnız o süreç: php-cgi işçileri, veritabanı ve web sunucusu ayrı
	// süreçler ve buraya girmiyor (bkz. internal/procstat). Arayüz de
	// böyle etiketliyor.
	RSS        uint64  `json:"rss,omitempty"`
	CPUSeconds float64 `json:"cpuSeconds,omitempty"`
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

	if st.Running {
		// Ölçüm alınamazsa (süreç az önce öldü, işletim sistemi
		// desteklemiyor) alan boş kalıyor: durum bilgisi bir ölçüm
		// hatası yüzünden kaybolmamalı.
		if usage, err := procstat.Read(s.PID); err == nil {
			st.RSS = usage.RSS
			st.CPUSeconds = usage.CPUSeconds
		}
	}
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
	if err := r.register(name, svc); err != nil {
		// Proje ayakta ama kenara bağlanamadı: alan adı açılmaz.
		// Sessizce geçmek, kullanıcıyı "çalışıyor görünüyor ama
		// açılmıyor" durumunda bırakır.
		svc.Stop()
		return Status{}, err
	}
	return r.status(p), nil
}

// register, projenin bildirdiği adresi paylaşılan kenara işler.
func (r *Runner) register(name string, svc *supervisor.Service) error {
	if r.Edge == nil {
		return nil
	}
	ep, ok := ParseEndpoint(svc.Logs().SinceStart())
	if !ok {
		return fmt.Errorf("projects: %s iç adresini bildirmedi; paylaşılan kenara bağlanamadı", name)
	}

	r.unregister(name)

	target := "http://" + ep.Addr
	var kayitli []string
	for _, host := range ep.Hosts {
		if err := r.Edge.Proxy(host, target); err != nil {
			r.removeHosts(kayitli)
			return fmt.Errorf("projects: %s için %s yönlendirilemedi: %w", name, host, err)
		}
		kayitli = append(kayitli, host)
	}
	// Posta kutusu ve denetleyici ağa açılmamalı. Kısıtı burada
	// uyguluyoruz çünkü proje sürecinin gördüğü uzak adres her zaman
	// 127.0.0.1: araya biz giriyoruz. İçerideki denetim her isteği
	// geçirirdi.
	for _, host := range ep.LocalOnly {
		h, err := edge.ProxyHandler(host, target, r.Logger)
		if err != nil {
			r.removeHosts(kayitli)
			return fmt.Errorf("projects: %s için %s yönlendirilemedi: %w", name, host, err)
		}
		r.Edge.Handle(host, edge.LoopbackOnly(h))
		kayitli = append(kayitli, host)
	}

	r.mu.Lock()
	if r.routes == nil {
		r.routes = make(map[string][]string)
	}
	r.routes[name] = kayitli
	r.mu.Unlock()
	return nil
}

// unregister, projenin kenardaki alan adlarını kaldırır.
func (r *Runner) unregister(name string) {
	r.mu.Lock()
	hosts := r.routes[name]
	delete(r.routes, name)
	r.mu.Unlock()
	r.removeHosts(hosts)
}

func (r *Runner) removeHosts(hosts []string) {
	if r.Edge == nil {
		return
	}
	for _, host := range hosts {
		r.Edge.Remove(host)
	}
}

// Stop, projeyi durdurur.
func (r *Runner) Stop(name string) (Status, error) {
	p, ok := r.Registry.Get(name)
	if !ok {
		return Status{}, fmt.Errorf("projects: kayıtlı proje bulunamadı: %q", name)
	}
	r.unregister(name)
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

	args := []string{"up", "-dir", p.Dir}
	if r.Edge != nil {
		// Port 0: işletim sistemi seçsin. Sabit port vermek, iki projeyi
		// aynı anda çalıştırmayı yeniden imkânsız kılardı.
		args = append(args, "-internal", "127.0.0.1:0")
	}
	args = append(args, r.ExtraArgs...)
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
