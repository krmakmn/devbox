# DevBox

Windows için yerel geliştirme ortamı — Laragon'un kolaylığı, DDEV'in yeniden
üretilebilirliği, Herd'ün cilası.

> **Durum: Faz 2 sürüyor.** Faz 0'ın dört prototipi (PHP havuzu, yerel CA,
> `*.test` çözücüsü, kenar proxy) ve Faz 1'in dört maddesi (runtime kayıt
> defteri, süreç denetçisi, çekirdek servisin API'si, ayrıcalıklı işlemler)
> hazır. Faz 2'de Apache ve Nginx sürücüleri yazıldı.
>
> Not: NRPT, Firefox NSS ve Windows onay penceresi yolları CI'nin
> kapsayamadığı yerler — gerçek bir Windows makinesinde elle denenmedi.
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
| `internal/runtime` | Runtime kayıt defteri: imzalı manifest, sürdürülebilir indirme, SHA256, sürümlü atomik kurulum |
| `internal/supervisor` | Genel süreç denetçisi: hazır olma ölçütleri, üstel geri çekilme, canlı günlük |
| `internal/api` | devboxd'nin yerel HTTP arayüzü: jeton, Host denetimi, SSE günlük akışı |
| `internal/elevate` | Talep üzerine UAC yükseltmesi; kalıcı ayrıcalıklı servis yerine |
| `internal/webserver` | Apache ve Nginx için vhost üretimi, söz dizimi ön denetimi, geri alma |
| `internal/ports` | Port tahsisi: Hyper-V rezervasyonları, IIS farkındalığı, açıklayıcı hata |
| `internal/phpini` | Proje başına php.ini katmanlama ve Xdebug anahtarı |
| `internal/edge` | 80/443'ü dinleyip host adına göre dağıtan ters vekil |
| `internal/dns` | `*.test` için yetkili, özyinelemesiz çözücü (UDP + TCP) |
| `internal/nrpt` | Windows Ad Çözümleme İlkesi Tablosu'na kural ekleme |
| `internal/hostsfile` | NRPT engellenirse geri düşüş: hosts dosyasında yönetilen blok |
| `internal/proc` | Süreçlerin arkada kalmamasını sağlayan iş nesnesi / süreç grubu |

Neden PHP-FPM değil: Windows'ta yok. php-cgi.exe var ama tek istek görüp
kapanıyor; kalıcı FastCGI kipinde çalıştırıp süreç yönetimini üstlenmek
gerekiyor. Laragon'un en zayıf yeri de burası.

Ayrıcalık tarafında: ilk tasarım LocalSystem olarak koşan kalıcı bir yardımcı
servis öngörüyordu. Ayrıcalıklı işlem listesi tek tek incelenince gerekçesi
kalmadı — altı işlemden üçü ya ayrıcalık gerektirmiyordu (Windows'ta 1024 altı
portlar ayrıcalıklı değil) ya da servisten yapılamıyordu (kök sertifika onay
penceresi). Kalan üçü yılda birkaç kez çalışan tek seferlik işler. Onlar için
kalıcı bir ayrıcalıklı dinleyici tutmak, projenin en büyük güvenlik yüzeyini
sürekli açık tutmak demekti. Yerine **talep üzerine yükseltme**: DevBox kendini
yalnız o işlem için, tipi ve içeriği doğrulanmış argümanlarla yeniden başlatıyor.
Saldırılacak IPC yüzeyi yok. "Şu komutu çalıştır" tarzı genel bir işlem de yok.

API tarafında: GUI'nin yapabildiği her şey CLI'dan da yapılabilsin diye iş
mantığı API'de değil, altta duran paketlerde; `devbox ps` ve `devbox logs`
zaten o API'nin istemcisi. Sunucu yalnız loopback'i dinliyor ama bu tek başına
yeterli değil — makinedeki herhangi bir tarayıcı sayfası da localhost'a istek
atabilir. Üç katman var: jeton (0600 izinli dosyada, sabit sürede
karşılaştırılıyor), **Host başlığı denetimi** (DNS yeniden bağlama saldırısını
keser) ve CORS başlığı vermemek. Günlük akışı WebSocket yerine SSE ile: tek
yönlü olduğu için yetiyor ve standart kütüphaneyle yazılabiliyor, bağımlılık
getirmiyor.

Runtime tarafında: Laragon'da bileşenler elle indirilip bir klasöre atılır —
bütünlük doğrulaması, sürüm yönetimi, geri alma ve temizlik yoktur. Buradaki
sözleşme bunun tersi: her indirme SHA256 ile doğrulanır ve eşleşmeyen dosya
diske kalıcı yazılmaz; kurulum atomiktir (arşiv geçici dizine açılır, ancak
eksiksizse yerine taşınır); sürümler yan yana durur; manifest ed25519 ile
imzalanır. Arşivden çıkan `../` girdileri sessizce düzeltilmez, reddedilir —
meşru bir dağıtımda böyle bir girdi olmaz.

> Manifest yayın altyapısı henüz yok: imzalayacak anahtar üretilmediği için
> **uzaktan manifest reddediliyor**, yerel dosyayla çalışılıyor. Fail-closed
> davranış bilinçli; doğrulanmamış bir liste istenmeyen bir ikilinin
> kurulmasına yol açar.

Web sunucusu tarafında: DevBox PHP'yi kendi de sunabiliyor, ama gerçek projeler
`.htaccess`'e, nginx yeniden yazma kurallarına ve özel `location` bloklarına
bağımlı — onları taklit etmek yerine gerçeğini çalıştırıyoruz. Üretilen
yapılandırma önce diske atomik yazılıyor, sonra sunucunun **kendi söz dizimi
denetiminden** geçiriliyor (`httpd -t`, `nginx -t`), ancak ondan sonra yeniden
yükleniyor; denetim başarısız olursa eski dosya geri konuyor, çünkü bozuk bir
yapılandırmayla yeniden yükleme çalışan siteleri de düşürür. Testler üretilen
yapılandırmayı gerçekten kurulu nginx ve Apache'ye doğrulattırıyor.

PHP ayarları tarafında: her projenin kendi `php.ini`'si üretiliyor — birinde
Xdebug açık ve 1 GB bellekle, diğerinde varsayılanlarla çalışabilmek için.
Katmanlama basit: runtime'ın kendi `php.ini-development`'ı olduğu gibi alınıp
altına DevBox bloğu ekleniyor; PHP aynı yönergenin sonuncusunu uyguladığı için
bu doğru önceliği veriyor ve temel dosyayı ayrıştırmak gerekmiyor.
Varsayılanlardan biri güvenlik gereği: **`cgi.fix_pathinfo=0`** — bu ayar
açıkken php-cgi yüklenmiş bir resmi PHP olarak çalıştırabiliyor. Web sunucusu
yapılandırmaları bunu ayrıca engelliyor; burada kapatmak üçüncü savunma hattı.

Port tarafında: Windows'ta bir port "boş görünüp" bağlanamayabiliyor. En sinsi
sebep **Hyper-V rezervasyonları** — WSL2 ya da Docker Desktop açıkken Windows
geniş aralıkları sessizce rezerve eder; `netstat` boş gösterir ama `bind`
"erişim engellendi" der. Tahsis edici bu listeyi okuyup aralığı önceden eliyor,
sonra portu gerçekten bağlamayı deneyerek doğruluyor, bulamazsa da sebebi ve
ne yapılacağını yazıyor (80/443 için IIS/W3SVC'yi anıyor).

Kenar tarafında: Laragon'daki "Apache **veya** Nginx" seçimi mimari bir
zorunluluk değil, sadece 80. portu tek sürecin dinleyebilmesinden kaynaklanıyor.
Kenarı ayırınca kısıt kalkıyor — `.htaccess`'e bağımlı bir WordPress ile Nginx
isteyen bir Laravel aynı anda çalışabiliyor. Kenar için Caddy gömmeye gerek
olmadığı görüldü: host tabanlı yönlendirme, TLS sonlandırma ve WebSocket
yükseltmesi standart kütüphaneyle çalışıyor, depo bağımlılıksız kalıyor.

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
| Ayrıcalıklı işlemler | Talep üzerine UAC yükseltmesi, tipli ve izin listeli (kalıcı servis yok) |
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
