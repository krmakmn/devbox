package api

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"

	"github.com/krmakmn/devbox/internal/project"
)

// ConfigResponse, bir projenin yapılandırması ve panelin sunacağı
// seçenekler.
type ConfigResponse struct {
	// Raw, devbox.yaml'ın olduğu gibi içeriği. Panel bunu gösteriyor:
	// kullanıcı neyi değiştirdiğini formda değil, dosyada görmeli.
	Raw string `json:"raw"`

	Config *project.Config `json:"config"`

	// Options, formun dolduracağı seçenek listeleri.
	Options ConfigOptions `json:"options"`
}

// ConfigOptions, panelde seçilebilecek değerler.
type ConfigOptions struct {
	// Servers, desteklenen sunucu tipleri.
	Servers []string `json:"servers"`

	// PHPVersions, makinede KURULU PHP sürümleri.
	//
	// Kurulu olmayan bir sürümü seçtirmek, kullanıcıyı projeyi
	// başlatınca çıkacak bir hataya yollamak olurdu. Katalogdan
	// kurulabilecek sürümler ayrı bir iş; burada yalnız elde olan var.
	PHPVersions []string `json:"phpVersions"`
}

// ConfigUpdateRequest, panelden gelen değişiklikler.
//
// Yol→değer biçimi ("php.version": "8.3") bilerek seçildi: tüm
// yapılandırmayı geri göndermek, kullanıcının aynı anda elle yaptığı
// düzenlemeleri sessizce ezerdi.
type ConfigUpdateRequest struct {
	Changes map[string]any `json:"changes"`
}

// ConfigUpdateResponse, kaydetmenin sonucu.
type ConfigUpdateResponse struct {
	Raw string `json:"raw"`

	Config *project.Config `json:"config"`

	// RestartNeeded, değişikliğin etkili olması için projenin yeniden
	// başlatılması gerektiğini söyler. Panel bunu kullanıcıya
	// gösteriyor; sessizce yeniden başlatmak, açık bir sekmeyi
	// habersiz düşürmek olurdu.
	RestartNeeded bool `json:"restartNeeded"`
}

func (s *Server) projectDir(name string) (string, error) {
	if s.projects == nil || s.projects.Registry == nil {
		return "", fmt.Errorf("proje kaydı yok")
	}
	p, ok := s.projects.Registry.Get(name)
	if !ok {
		return "", fmt.Errorf("kayıtlı proje bulunamadı: %q", name)
	}
	if p.Missing {
		return "", fmt.Errorf("%s dizini bulunamadı: %s", name, p.Dir)
	}
	return p.Dir, nil
}

func (s *Server) handleProjectConfig(w http.ResponseWriter, r *http.Request) {
	dir, err := s.projectDir(r.PathValue("name"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	raw, err := os.ReadFile(filepath.Join(dir, project.FileName))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	cfg, err := project.Load(dir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, ConfigResponse{
		Raw:    string(raw),
		Config: cfg,
		Options: ConfigOptions{
			Servers:     project.Servers(),
			PHPVersions: s.installedPHPVersions(),
		},
	})
}

func (s *Server) handleProjectConfigUpdate(w http.ResponseWriter, r *http.Request) {
	dir, err := s.projectDir(r.PathValue("name"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	var req ConfigUpdateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "istek çözülemedi: "+err.Error())
		return
	}
	if len(req.Changes) == 0 {
		writeError(w, http.StatusBadRequest, "değişiklik yok")
		return
	}

	changes, err := yamlValues(req.Changes)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	path := filepath.Join(dir, project.FileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	out, err := project.Update(raw, changes)
	if err != nil {
		// Kullanıcının hatası: geçersiz değer. Dosyaya dokunulmadı.
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Dosya izinleri korunuyor: kullanıcı 0600 yaptıysa 0644'e
	// düşürmek sessiz bir gerileme olurdu.
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	if err := os.WriteFile(path, out, mode); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	cfg, err := project.Load(dir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	st, _ := s.projects.Status(r.PathValue("name"))
	writeJSON(w, http.StatusOK, ConfigUpdateResponse{
		Raw:           string(out),
		Config:        cfg,
		RestartNeeded: st.Running,
	})
}

// installedPHPVersions, kurulu PHP sürümlerini yeniden eskiye sıralar.
func (s *Server) installedPHPVersions() []string {
	if s.runtimes == nil {
		return nil
	}
	list, err := s.runtimes.List()
	if err != nil {
		return nil
	}
	var out []string
	for _, inst := range list {
		if inst.Package.Name == "php" {
			out = append(out, inst.Package.Version)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out
}

// yamlValues, JSON'dan gelen değerleri Update'in kabul ettiği türlere
// çevirir.
//
// JSON her sayıyı float64 yapıyor. "workers": 8 doğrudan geçirilseydi
// dosyaya "8" değil "8.0" yazılırdı ve yapılandırma çözülemezdi.
func yamlValues(in map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(in))
	for k, v := range in {
		conv, err := yamlValue(v)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		out[k] = conv
	}
	return out, nil
}

func yamlValue(v any) (any, error) {
	switch t := v.(type) {
	case nil:
		return nil, nil
	case string, bool:
		return t, nil
	case float64:
		if t != math.Trunc(t) {
			return nil, fmt.Errorf("ondalık sayı kabul edilmiyor: %v", t)
		}
		return int(t), nil
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("liste yalnız metin içerebilir")
			}
			out = append(out, s)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("desteklenmeyen değer türü")
	}
}
