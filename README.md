# DevBox

Windows için yerel geliştirme ortamı — Laragon'un kolaylığı, DDEV'in yeniden
üretilebilirliği, Herd'ün cilası.

> **Durum: Faz 0 — mimari doğrulama.** PHP FastCGI süreç havuzu, yerel
> sertifika otoritesi ve `*.test` çözücüsü çalışıyor. Kenar proxy henüz yok.
> Plan: **[docs/yol-haritasi.md](docs/yol-haritasi.md)**

## Şu an ne çalışıyor

Bir dizini php-cgi süreç havuzu üzerinden HTTP'de sunabiliyorsunuz:

```bash
devbox serve --root ./public --php C:\php\php-cgi.exe --addr 127.0.0.1:8080
```

Bunun arkasında duran parçalar:

| Paket | İş |
|---|---|
| `internal/fastcgi` | FastCGI 1.0 istemcisi — kayıt çerçeveleme, akışlı gövde, STDERR yakalama |
| `internal/phppool` | php-cgi süreç havuzu — dağıtım, sağlık denetimi, yenileme, üstel geri çekilmeyle yeniden başlatma |
| `internal/web` | HTTP → FastCGI köprüsü, CGI değişkenleri ve güvenlik denetimleri |
| `internal/certs` | Yerel kök CA, joker SAN'lı site sertifikaları, sessiz yenileme |
| `internal/trust` | Kökü Windows güven deposuna ve Firefox'un NSS veritabanına kurma |
| `internal/dns` | `*.test` için yetkili, özyinelemesiz çözücü (UDP + TCP) |
| `internal/nrpt` | Windows Ad Çözümleme İlkesi Tablosu'na kural ekleme |
| `internal/hostsfile` | NRPT engellenirse geri düşüş: hosts dosyasında yönetilen blok |
| `internal/proc` | Süreçlerin arkada kalmamasını sağlayan iş nesnesi / süreç grubu |

Neden PHP-FPM değil: Windows'ta yok. php-cgi.exe var ama tek istek görüp
kapanıyor; kalıcı FastCGI kipinde çalıştırıp süreç yönetimini üstlenmek
gerekiyor. Laragon'un en zayıf yeri de burası.

Alan adı tarafında: `hosts` dosyası joker desteklemediği için `magaza.test`
yazmak `admin.magaza.test`'i çözmez. Bunun yerine `*.test`'e cevap veren
yetkili bir çözücü çalıştırıp Windows'un **NRPT**'sinde yalnız `.test`'i ona
yönlendiriyoruz; makinenin geri kalan DNS'i (VPN, kurumsal ağ, split-DNS)
hiç etkilenmiyor. Çözücü kasten özyinelemesiz ve yalnız kendi son eklerine
cevap veriyor — açık çözücü olarak kötüye kullanılamaz.

Sertifika tarafında iki ayrım noktası:

- **Firefox de kapsanıyor.** Chrome ve Edge Windows'un güven deposunu okur,
  Firefox okumaz — kendi NSS veritabanını taşır. Laragon'un atladığı ve
  "sertifika kurdum ama Firefox'ta hâlâ uyarı veriyor" şikâyetlerinin tek
  sebebi bu.
- **Joker SAN.** `magaza.test` için kesilen sertifika `*.magaza.test`'i de
  kapsar; `admin.magaza.test` sıfır yapılandırmayla çalışır.

Halihazırda kapatılmış üç bilinen tuzak:

- **`cgi.fix_pathinfo` ile uzaktan kod çalıştırma** — diskte gerçekten var olan
  düzenli bir `.php` dosyası dışında hiçbir şey `SCRIPT_FILENAME` olmuyor,
  yani `/yuklemeler/kedi.jpg/x.php` numarası çalışmıyor.
- **httpoxy (CVE-2016-5385)** — gelen `Proxy` başlığı `HTTP_PROXY`'ye çevrilmiyor.
- **Sır sızıntısı** — `.env`, `.git`, `.htaccess` gibi nokta ile başlayan yollar
  403; ACME doğrulaması için `.well-known` ayrık tutuluyor.

## Ne yapacak

- **Aynı anda birden çok web sunucusu** — Apache, Nginx ve Caddy yan yana; proje
  başına seçilir. 80/443'ü tek bir kenar proxy dinler, gerisini host adına göre
  dağıtır. (Laragon'da "Apache **veya** Nginx" zorunluluğunun sebebi bu değildi.)
- **Yan yana runtime sürümleri** — PHP 7.4–8.4, Node, Python, Go. İndirme,
  SHA256 doğrulama, sürümlü kurulum ve geri alma ile.
- **Çoklu veritabanı örneği** — MySQL, MariaDB, PostgreSQL; farklı sürümleri aynı
  anda, ayrı veri dizini ve portla. Anlık görüntü alma ve geri yükleme dahil.
- **Yerel alan adı** — `proje.test` ve joker `*.proje.test`, `hosts` dosyasına
  elle satır eklemeden. Windows'un **NRPT** mekanizmasıyla; makinenin geri kalan
  DNS'i (VPN, kurumsal ağ) etkilenmez.
- **Otomatik TLS** — ilk açılışta üretilen yerel CA; Windows güven deposuna,
  **Firefox'un kendi NSS veritabanına**, WSL'e ve Java'ya kurulur. Site
  sertifikaları otomatik üretilir ve sessizce yenilenir.
- **Yerel ACME sunucusu** — konteynerdeki Traefik, `certbot` ya da `lego` da
  sertifikayı standart yolla alabilir. Rakiplerde hazır gelmiyor.
- **Yerel posta** — giden tüm SMTP trafiğini yakalayan Mailpit; web arayüzü,
  arama ve test edilebilir API.
- **Depoya giren ortam** — proje kökündeki `devbox.yaml`. Ekip arkadaşınız
  klonlayıp `devbox up` der, ortam birebir aynı olur.
- **CLI + REST API + GUI eşitliği** — GUI iş mantığı içermez; her özellik önce
  API'de doğar. Yani her şey betiklenebilir.

## Mimari özeti

| Katman | Seçim |
|---|---|
| Çekirdek servis (`devboxd`) | Go — tek statik `.exe`, yerel Windows servisi |
| Ayrıcalıklı yardımcı (`devbox-helper`) | LocalSystem, yalnız 6 izin listeli işlem |
| Kenar proxy | Caddy, kütüphane olarak `devboxd` içine gömülü |
| GUI | Tauri 2 (~10 MB, WebView2) |
| CLI | Aynı Go ikilisi, API üzerinden |

Gerekçeleri ve alternatifleri yol haritasının 4. bölümünde.

## Yol haritası

| Faz | Konu | Süre |
|---|---|---|
| 0 | Keşif ve mimari doğrulama (4 prototip) | 3 hafta |
| 1 | Çekirdek: süreç denetçisi, helper, runtime kayıt defteri | 6 hafta |
| 2 | Web katmanı: Caddy kenarı, Apache/Nginx, PHP FastCGI havuzu | 5 hafta |
| 3 | Alan adı + TLS → **MVP (v0.5)** | 5 hafta |
| 4 | Veritabanları ve anlık görüntüler | 5 hafta |
| 5 | Posta ve yan servisler | 3 hafta |
| 6 | GUI ve geliştirici deneyimi | 6 hafta |
| 7 | ACME sunucusu, konteyner sürücüsü, HTTP denetleyicisi, tünel | 6 hafta |
| 8 | Güvenlik denetimi, imzalama, göç aracı → **v1.0** | 5 hafta |

Ayrıntı, kabul kriterleri ve risk kaydı: [docs/yol-haritasi.md](docs/yol-haritasi.md)

## Geliştirme

Go 1.23+ yeterli; dış bağımlılık yok.

```bash
go test ./... -race     # testler (gerçek süreç başlatan bütünleşme testleri dahil)
go vet ./...
GOOS=windows go build ./...
```

Testler gerçek bir PHP kurulumu istemez: test ikilisi kendini sahte bir php-cgi
olarak yeniden çalıştırır ve FastCGI'yi standart kütüphanenin sunucu tarafı
konuşur. Yani tel üzerindeki protokol ve süreç yönetimi gerçek, taklit edilen
tek şey PHP yorumlayıcısı.

## Lisans

Apache-2.0. Ayrıntı: [LICENSE](LICENSE).

## Karar bekleyenler

Yol haritasının 11. bölümünde beş açık soru var: TLD tercihi, lisans modeli,
konteyner sürücüsünün kapsamı, desteklenecek en eski Windows sürümü ve GUI
çerçevesi.
