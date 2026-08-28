package webserver

import (
	"context"
	"fmt"
	"strings"
	"text/template"
)

// Nginx, nginx sürücüsü.
type Nginx struct {
	// Binary, nginx çalıştırılabilirinin yolu.
	Binary string

	// Prefix, nginx'in kök dizini (-p). Windows'ta nginx çalışma dizinine
	// duyarlı; belirtilmezse yapılandırma yollarını yanlış çözer.
	Prefix string
}

func (n *Nginx) Name() string { return "nginx" }

// nginxTemplate, upstream ve server bloklarını üretir.
//
// En kritik satır "try_files $uri =404;". Bu satır olmadan nginx, var
// olmayan bir .php yolunu da PHP'ye gönderir; cgi.fix_pathinfo açıkken
// "/yuklemeler/kedi.jpg/x.php" isteği yüklenmiş bir resmi PHP olarak
// çalıştırabilir. Yaygın nginx yapılandırmalarındaki en bilinen açık budur.
const nginxTemplate = `# DevBox tarafından üretildi — elle düzenlemeyin.
# Değişiklikler bir sonraki "devbox" çalıştırmasında kaybolur.

# Kenar TLS'i sonlandırıp şemayı X-Forwarded-Proto ile bildiriyor. PHP,
# HTTPS değişkenini "boş ve 'off' değilse açık" diye yorumlar; başlığı
# doğrudan geçirmek düz HTTP'de bile "https" sanılmasına yol açar.
map $http_x_forwarded_proto $devbox_https {
    default off;
    https   on;
}

{{range .}}
{{- if .PHPBackends}}
upstream {{.UpstreamName}} {
{{- range .PHPBackends}}
    server {{.}};
{{- end}}
}
{{end}}
server {
    listen {{.Listen}};
    server_name {{.ServerNames}};
    root "{{.Root}}";
    index {{.IndexList}};

    charset utf-8;
    client_max_body_size 128M;

    location / {
{{- if .PHPBackends}}
        try_files $uri $uri/ /{{.FrontController}}?$query_string;
{{- else}}
        # PHP yok: bulunamayan yol doğrudan 404. Var olmayan bir ön
        # denetleyiciye yönlendirmek anlamsız bir iç yönlendirme üretirdi.
        try_files $uri $uri/ =404;
{{- end}}
    }

    # Nokta ile başlayan yollar sır sızdırır (.env, .git). ACME doğrulaması
    # için .well-known ayrık tutuluyor.
    location ~ /\.(?!well-known).* {
        deny all;
    }
{{if .PHPBackends}}
    location ~ \.php$ {
        # Bu satır güvenlik açısından zorunlu: var olmayan bir .php yolunu
        # PHP'ye göndermek, cgi.fix_pathinfo ile birlikte uzaktan kod
        # çalıştırmaya dönüşebiliyor.
        try_files $uri =404;

        fastcgi_pass {{.UpstreamName}};
        fastcgi_index {{.FrontController}};
        fastcgi_split_path_info ^(.+\.php)(/.+)$;
        include fastcgi_params;
        fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;
        fastcgi_param PATH_INFO $fastcgi_path_info;
        fastcgi_param HTTPS $devbox_https;
        fastcgi_read_timeout 300;
    }
{{end}}
{{- if .LogPath "error"}}
    access_log "{{.LogPath "access"}}";
    error_log "{{.LogPath "error"}}";
{{- end}}
}
{{end}}`

var nginxTmpl = template.Must(template.New("nginx").Parse(nginxTemplate))

func (n *Nginx) Render(sites []Site) (string, error) {
	prepared, err := prepare(sites)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	if err := nginxTmpl.Execute(&sb, prepared); err != nil {
		return "", fmt.Errorf("webserver: nginx şablonu işlenemedi: %w", err)
	}
	return sb.String(), nil
}

func (n *Nginx) Write(path string, sites []Site) error {
	content, err := n.Render(sites)
	if err != nil {
		return err
	}
	return writeConfig(path, content)
}

func (n *Nginx) Validate(ctx context.Context, configPath string) error {
	args := []string{"-t", "-c", configPath}
	if n.Prefix != "" {
		args = append(args, "-p", n.Prefix)
	}
	return runCommand(ctx, n.Binary, args...)
}

func (n *Nginx) Reload(ctx context.Context) error {
	args := []string{"-s", "reload"}
	if n.Prefix != "" {
		args = append(args, "-p", n.Prefix)
	}
	return runCommand(ctx, n.Binary, args...)
}

// prepare, siteleri doğrular ve varsayılanları uygular.
func prepare(sites []Site) ([]Site, error) {
	out := make([]Site, 0, len(sites))
	seen := map[string]bool{}

	for _, s := range sites {
		s.applyDefaults()
		if err := s.Validate(); err != nil {
			return nil, err
		}
		if seen[s.Name] {
			return nil, fmt.Errorf("webserver: %q adında birden fazla site var", s.Name)
		}
		seen[s.Name] = true
		out = append(out, s)
	}
	return out, nil
}
