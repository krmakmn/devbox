package webserver

import (
	"context"
	"fmt"
	"strings"
	"text/template"
)

// Apache, Apache httpd sürücüsü.
type Apache struct {
	// Binary, httpd çalıştırılabilirinin yolu. Boşsa doğrulama ve yeniden
	// yükleme atlanır.
	Binary string

	// ServiceName, Windows'ta kayıtlı servis adı. Verilirse yeniden yükleme
	// servis üzerinden yapılır.
	ServiceName string
}

func (a *Apache) Name() string { return "apache" }

// apacheTemplate, tek bir vhost bloğu üretir.
//
// Tasarım notları:
//
//   - "Require local": sunucu yalnız loopback'ten gelen isteklere cevap
//     verir. Kenar proxy zaten loopback'ten bağlanıyor; bu, sunucunun
//     yanlışlıkla ağa açılmasını engelliyor.
//   - "AllowOverride All": .htaccess desteği bu projelerin Apache'yi
//     seçmesinin başlıca sebebi; kapatmak Apache'yi anlamsız kılar.
//   - FilesMatch içindeki SetHandler, PHP'yi FastCGI havuzuna yönlendirir.
//     Yalnız var olan dosyalar için: "/yuklemeler/kedi.jpg/x.php" numarasına
//     karşı Directory bloğundaki denetim ile birlikte çalışır.
const apacheTemplate = `# DevBox tarafından üretildi — elle düzenlemeyin.
# Değişiklikler bir sonraki "devbox" çalıştırmasında kaybolur.

{{range .}}
{{- if .PHPBackends}}
<Proxy "balancer://{{.UpstreamName}}">
{{- range .PHPBackends}}
    BalancerMember "fcgi://{{.}}"
{{- end}}
</Proxy>
{{end}}
<VirtualHost {{.Listen}}>
    ServerName {{.Domain}}
{{- range .Aliases}}
    ServerAlias {{.}}
{{- end}}
    DocumentRoot "{{.Root}}"
    DirectoryIndex {{.IndexList}}

    # Kenar TLS'i sonlandırıyor; PHP'nin mutlak URL üretebilmesi için şemayı
    # ondan öğreniyoruz. Başlığı doğrudan geçirmek düz HTTP'de bile "açık"
    # sanılmasına yol açardı, o yüzden yalnız tam eşleşmede ayarlıyoruz.
    SetEnvIf X-Forwarded-Proto "^https$" HTTPS=on

    <Directory "{{.Root}}">
        Options -Indexes +FollowSymLinks
        AllowOverride All
        # Yalnız loopback: kenar proxy buradan bağlanıyor, sunucunun
        # yanlışlıkla ağa açılmasını istemiyoruz.
        Require local
    </Directory>

    # Nokta ile başlayan dosyalar sır sızdırır (.env, .git, .htaccess).
    <DirectoryMatch "/\.">
        Require all denied
    </DirectoryMatch>
{{if .PHPBackends}}
    # PHP yalnız diskte gerçekten var olan dosyalar için çalışır. Var
    # olmayan bir .php yolunu PHP'ye göndermek, cgi.fix_pathinfo ile
    # birlikte "/yuklemeler/kedi.jpg/x.php" numarasını uzaktan kod
    # çalıştırmaya dönüştürebiliyor.
    <FilesMatch "\.php$">
        <If "-f %{REQUEST_FILENAME}">
            SetHandler "proxy:balancer://{{.UpstreamName}}/"
        </If>
        <Else>
            Require all denied
        </Else>
    </FilesMatch>
{{end}}
{{- if .LogPath "error"}}
    ErrorLog "{{.LogPath "error"}}"
    CustomLog "{{.LogPath "access"}}" combined
{{- end}}
</VirtualHost>
{{end}}`

var apacheTmpl = template.Must(template.New("apache").Parse(apacheTemplate))

func (a *Apache) Render(sites []Site) (string, error) {
	prepared, err := prepare(sites)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	if err := apacheTmpl.Execute(&sb, prepared); err != nil {
		return "", fmt.Errorf("webserver: apache şablonu işlenemedi: %w", err)
	}
	return sb.String(), nil
}

func (a *Apache) Write(path string, sites []Site) error {
	content, err := a.Render(sites)
	if err != nil {
		return err
	}
	return writeConfig(path, content)
}

// Validate, httpd'nin kendi söz dizimi denetimini çalıştırır.
func (a *Apache) Validate(ctx context.Context, configPath string) error {
	return runCommand(ctx, a.Binary, "-t", "-f", configPath)
}

// Reload, Apache'ye yapılandırmayı yeniden okutur.
//
// Windows'ta "httpd -k restart" servis üzerinden çalışır; zarif yeniden
// başlatma (graceful) Windows derlemelerinde desteklenmiyor.
func (a *Apache) Reload(ctx context.Context) error {
	if a.ServiceName != "" {
		return runCommand(ctx, a.Binary, "-k", "restart", "-n", a.ServiceName)
	}
	return runCommand(ctx, a.Binary, "-k", "restart")
}
