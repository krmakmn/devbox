# Windows Yerel Geliştirme Ortamı — Yol Haritası

> Kod adı: **DevBox**. Laragon'un yerini alacak, ondan belirgin biçimde daha
> yetenekli bir Windows yerel geliştirme yığını. Bu belge bir ürün ve mühendislik
> yol haritasıdır: mimari kararlar, gerekçeleri, faz planı, riskler.

> **Faz 0 tamamlandı.** Dört prototip de yazıldı ve testleriyle birlikte
> depoda. Prototipler iki mimari kararı değiştirdi; ilgili bölümler
> güncellendi ve değişiklikler **"Prototip bulguları"** başlığında toplandı.

---

## 0. Prototip bulguları (Faz 0 sonucu)

| Varsayım | Gerçek | Sonuç |
|---|---|---|
| Çözücü 53535 gibi yüksek bir portta çalışır, NRPT kuralı portu taşır | **NRPT kuralı yalnız sunucu IP'si alır, port taşıyamaz** | Çözücü 53'te dinlemek zorunda. Windows'ta 1024 altı portlar ayrıcalıklı olmadığı için yönetici hakkı gerekmiyor. Çakışmayı önlemek için 127.0.0.1 yerine **127.0.0.53** kullanılıyor |
| Kenar proxy için Caddy kütüphane olarak gömülür | **Kenarın ihtiyacı olan her şey standart kütüphanede var** | `httputil.ReverseProxy` ile host tabanlı yönlendirme, TLS sonlandırma ve WebSocket yükseltmesi çalışıyor. Caddy'nin asıl değeri `acme_server`; bağımlılık **Faz 7'ye ertelendi** ve depo bağımlılıksız kaldı |
| php-cgi havuzu en riskli parça | Doğrulandı, çalışıyor | Kendi FastCGI istemcimiz ve süreç havuzumuz yazıldı; en ince nokta işçi tahsisindeki yarış oldu (bkz. 4.4) |
| API için WebSocket | **Günlük akışı tek yönlü** | SSE yetiyor ve standart kütüphaneyle yazılıyor; WebSocket bağımlılığı ertelendi. Çift yönlü kanal gerektiğinde (etkileşimli konsol) konu yeniden açılır |
| Kök sertifikayı güven deposuna kurmak yeter | Firefox kendi NSS veritabanını taşıyor | Kurulum dört hedefe birden yapılıyor; Firefox atlanırsa "kurdum ama hâlâ uyarı veriyor" yaşanıyor |
| Kök sertifika kurulumu sessizce yapılabilir | **Windows onay penceresi gösteriyor ve yanıt bekliyor** | Masaüstü oturumu olmayan bir ortamda çağrı süresiz bloke oluyor (CI'da 10 dakikalık test zaman aşımıyla keşfedildi). Kurulum artık bağlamla sınırlı; ayrıcalıklı yardımcı bu işi **servis olarak yapamaz**, kullanıcının oturumunda çalışmalı |
| HTML postayı `srcdoc`'lu sandbox iframe'de göstermek yeter | **`srcdoc` belgesi üst sayfanın CSP'sini devralıyor** | Üst sayfanın ilkesinde satır içi betik açık olduğu için posta HTML'ine de betik izni geçiyordu; koruma yalnız `sandbox` özniteliğine kalmıştı. Gövde artık kendi katı ilkesini taşıyan ayrı bir uç noktadan sunuluyor. Gerçek Chromium'da bulundu, testler kaçırmıştı |
| Denetlenen sürece durdurma sinyali göndermek yeter | **Sarmalayıcı komutlarda torun süreç ayakta kalıyor** | `sh -c`, `npm run dev` gibi komutlarda sinyal yalnız sarmalayıcıya gidiyordu; torun boruları açık tuttuğu için `cmd.Wait()` dönmüyor ve kapanış iki kez `StopTimeout` sürüyordu (Ctrl+C sonrası 20 saniye). Sinyal artık süreç ağacına ve SIGINT yerine SIGTERM ile gidiyor — POSIX, arka plana atılmış komutların SIGINT'i yok saymasını şart koşuyor. Kapanış 1,5 saniye |
| Testin bağlanıp kapattığı port boş kalır | **Başka bir süreç portu hemen kapabiliyor** | TCPReady testi, sahte servis dinlemeye başlamadan bağlanıp "hazır olmayı beklemiyor" diye yanıltıcı bir hata veriyordu. Makinede paralel çalışan başka süreçler varken ortaya çıktı. freeAddr artık adresin gerçekten bağlantı reddettiğini doğruluyor |
| Çerçeve kurucusuna hedef dizinin mutlak yolu verilir | **Kurucular yolu çalışma dizinine ekliyor** | create-vite'a mutlak yol verilince proje `/tmp/yeni/tmp/yeni/arayuz`'e kuruldu. Komut artık hedefin üst dizininde çalışıp yalnız adı alıyor. Gerçek kurucuyla ilk denemede çıktı |
| Kalıcı ayrıcalıklı yardımcı servis gerekli | **Ayrıcalıklı işlem listesi eridi** | Altı işlemden üçü ayrıcalık gerektirmiyordu ya da servisten yapılamıyordu; kalan üçü yılda birkaç kez çalışan tek seferlik işler. Kalıcı bir ayrıcalıklı dinleyici, yılda birkaç dakika için projenin en büyük güvenlik yüzeyini sürekli açık tutmak demekti. **Talep üzerine yükseltmeye geçildi** (bkz. 4.2) |

---

## 1. Hedef

Tek bir kurulumla, yönetici hakkı gerektirmeyen gündelik kullanımda, bir Windows
makinesinde şunları veren bir ortam:

- Aynı anda çalışan **birden fazla web sunucusu** (Apache + Nginx + uygulama
  süreçleri), proje başına seçilebilir.
- Yan yana duran **çoklu runtime sürümleri** (PHP 7.4–8.4, Node, Python, Go).
- **Çoklu veritabanı örneği**: MySQL, MariaDB, PostgreSQL — farklı sürümleri aynı
  anda, proje başına ayrı veri dizini ve portla.
- **Yerel alan adı**: `proje.test` yazınca çalışsın, `hosts` dosyasına elle satır
  eklemeden, joker (`*.test`) desteğiyle.
- **Otomatik TLS**: ilk açılışta üretilen yerel CA, tarayıcıların ve Firefox'un
  güven deposuna kurulu; her siteye otomatik sertifika, otomatik yenileme,
  ayrıca **yerel ACME sunucusu** (konteynerdeki Caddy/Traefik/certbot da alsın).
- **Yerel posta**: giden tüm SMTP trafiğini yakalayan, web arayüzü ve API'si olan
  bir posta kutusu.
- Her şeyin **CLI + REST API + GUI** ile eşit biçimde yönetilebilmesi.

Kısaca: Laragon'un kolaylığı, DDEV'in yeniden üretilebilirliği, Herd'ün cilası.

---

## 2. Laragon'da somut olarak ne eksik

Yol haritasının her maddesi bu listeden birine cevap verir.

| Kısıt | Sonucu |
|---|---|
| Aynı anda tek web sunucusu (Apache **veya** Nginx) | Nginx'te çalışan projeyle Apache `.htaccess`'e bağımlı projeyi birlikte açamazsınız |
| `hosts` dosyası yeniden yazılarak alan adı | Joker alt alan adı yok; `hosts` şişer; her değişiklik yükseltilmiş hak ister |
| Tek, uzun ömürlü ve elle üretilen kök sertifika | Firefox güvenmez, Chrome uyarır, yenileme yok, ACME yok |
| Runtime'lar elle indirilip klasöre atılır | Sürüm doğrulama, bütünlük (SHA256), geri alma, temizlik yok |
| Servisler tek bir GUI süreci altında | GUI kapanınca artık süreçler kalır; sağlık denetimi/otomatik yeniden başlatma yok |
| Yapılandırma makineye gömülü | Proje ayarları depoya girmez, ekip arkadaşınızda aynı ortam oluşmaz |
| PostgreSQL yok, çoklu DB örneği yok | Bir projede PG 16, diğerinde MySQL 8 senaryosu kurulamaz |
| Betiklenemez | CI'da veya `winget` sonrası otomatik kurulum yapılamaz |

---

## 3. Rakip konumlandırma

- **Laragon** — hızlı ve hafif, ama yukarıdaki kısıtlar. Kapalı kaynak çekirdek.
- **XAMPP / WampServer** — eski mimari, sürüm yönetimi yok.
- **Laravel Herd (Windows)** — en yakın rakip, cilalı; ama Laravel/PHP odaklı,
  kapalı kaynak, ileri özellikler ücretli. **Farkımız:** çerçeve bağımsızlık,
  çoklu veritabanı ve çoklu web sunucusu, açık mimari, depoya giren proje tanımı.
- **DDEV / Lando** — yeniden üretilebilir ama Docker zorunlu, Windows'ta dosya
  sistemi ve soğuk başlatma maliyeti yüksek. **Farkımız:** yerel (native) hız,
  konteyner *opsiyonel* sürücü.
- **Scoop / winget** — paket kurar, orkestrasyon yapmaz. Runtime kayıt defteri
  tasarımında ilham kaynağı.

Konum cümlesi: *"Yerel hızda çalışan, ama proje tanımı depoya giren ve
betiklenebilen Windows geliştirme yığını."*

---

## 4. Mimari

```
┌───────────────┐   ┌───────────────┐   ┌───────────────────┐
│  DevBox GUI   │   │  devbox CLI   │   │ VS Code eklentisi │
│  (Tauri 2)    │   │               │   │                   │
└───────┬───────┘   └───────┬───────┘   └─────────┬─────────┘
        └───────────────────┴─────────────────────┘
                            │  REST + WebSocket (127.0.0.1, token'lı)
                 ┌──────────▼───────────┐
                 │     devboxd (Go)     │  kullanıcı hakkıyla çalışır
                 │  ──────────────────  │
                 │ süreç denetçisi      │  Job Object, sağlık, log
                 │ yapılandırma üretici │  text/template → vhost/ini/conf
                 │ runtime kayıt defteri│  indir, SHA256 doğrula, sürümle
                 │ sertifika yöneticisi │  iç CA + ACME sunucusu
                 │ proje modeli         │  devbox.yaml
                 └───┬──────────────┬───┘
                     │              │ yalnız işlem sırasında, kısa ömürlü
                     │   ┌──────────▼───────────┐
                     │   │ devbox privileged    │ talep üzerine UAC
                     │   │ (tipli işlemler,     │  • NRPT / hosts yaz
                     │   │  girdi izin listeli) │  • güvenlik duvarı kuralı
                     │   └──────────────────────┘  • servis kaydı
   ┌─────────────────┼─────────────────────────────────────────────┐
   │      Kenar (edge): stdlib ters vekil — 80/443, TLS sonlandırma │
   └───┬─────────┬─────────┬──────────┬──────────┬─────────────────┘
       │         │         │          │          │
   Apache    Nginx     php-cgi     Node/Go    Konteyner
   :8080     :8081      havuzu     süreçleri  (WSL2/Docker sürücüsü)
       │
   ┌───┴────────────────────────────────────────────────────────┐
   │ MySQL 8 · MariaDB 11 · PostgreSQL 16/17 · Redis · Mailpit  │
   │ MinIO · Meilisearch — her biri ayrı örnek, ayrı veri dizini │
   └────────────────────────────────────────────────────────────┘
```

### 4.1 Teknoloji seçimleri ve gerekçe

| Katman | Seçim | Neden |
|---|---|---|
| Çekirdek servis | **Go 1.23+** | Tek statik `.exe`, `x/sys/windows/svc` ile yerel Windows servisi, Job Object/İş nesnesi API'sine kolay erişim, `crypto/x509` ile CA işleri kütüphanesiz |
| Kenar proxy | **Standart kütüphane** (`httputil.ReverseProxy`) | Prototip D'nin bulgusu: kenarın ihtiyacı olan her şey stdlib'de var — host tabanlı yönlendirme, TLS sonlandırma, WebSocket yükseltmesi. Caddy'nin asıl değeri `acme_server` modülü; o bağımlılık gerçekten gerekli olduğunda (Faz 7) eklenecek |
| GUI | **Tauri 2 + Svelte/React** | ~10 MB kurulum, WebView2 zaten Windows'ta var; Electron'un 150 MB'ı ve RAM maliyeti yok |
| CLI | Aynı Go ikilisi, `devbox` alt komutları | GUI ile **tek API** üzerinden konuşur; GUI'nin yapabildiği her şey betiklenebilir |
| Günlük akışı | **SSE** (Sunucu Gönderimli Olaylar) | Tek yönlü olduğu için WebSocket'e gerek yok; stdlib yetiyor (bkz. bölüm 0) |
| Yapılandırma üretimi | `text/template` + atomik dosya yazımı | Kısmi yazılmış conf ile sunucu çökmesin |
| Şema | JSON Schema'lı `devbox.yaml` | Editör tamamlaması ve doğrulama |

**Karar:** GUI asla iş mantığı içermez. Her özellik önce API'de doğar, CLI ve GUI
onu tüketir. Bu, Laragon'dan en büyük yapısal ayrışma.

### 4.2 Yetki ayrımı — talep üzerine yükseltme

Laragon'un tamamı yönetici olarak çalışır. İlk tasarım bunun yerine
LocalSystem olarak koşan kalıcı bir yardımcı servis ve adlandırılmış boru
üzerinden IPC öngörüyordu. **Faz 1'de ayrıcalıklı işlem listesi tek tek
gözden geçirilince o tasarımın gerekçesi kalmadı:**

| Öngörülen ayrıcalıklı işlem | Gerçek |
|---|---|
| 80/443 bağlama | Windows'ta 1024 altı portlar ayrıcalıklı değil — ayrıcalıklı işlem değilmiş |
| Kök sertifikayı güven deposuna kurma | Windows onay penceresi gösteriyor; servis masaüstüne pencere gösteremez. Servisten **yapılamaz** |
| Hyper-V dışlanan port aralığı sorgusu | Sorgu ayrıcalık istemiyor |
| NRPT / hosts yazma | Yönetici gerekiyor — ama yılda birkaç kez |
| Güvenlik duvarı kuralı | Yönetici gerekiyor — ama kurulumda bir kez |
| Servis kaydı | Yönetici gerekiyor — ama kurulumda bir kez |

Geriye kalan üç işlem de seyrek ve tek seferlik. Onlar için kalıcı bir
ayrıcalıklı dinleyici çalıştırmak, projenin en büyük güvenlik yüzeyini —
yerel ayrıcalık yükseltme (LPE) — yılda birkaç dakika kullanılacak bir
yetenek uğruna sürekli açık tutmak demekti.

**Yeni tasarım:** DevBox kendini yalnız o işlem için, tipi ve içeriği
doğrulanmış argümanlarla yeniden başlatıyor (`devbox privileged <işlem>`).
Sürekli dinleyen bir ayrıcalıklı süreç yok, dolayısıyla saldırılacak IPC
yüzeyi de yok. Bedeli işlem başına bir UAC penceresi; seyrek işlemler için
makul.

Korunan ilkeler:

- `devboxd` normal kullanıcı hakkıyla çalışır.
- Ayrıcalıklı yürütücüde **"şu komutu çalıştır" tarzı genel bir işlem yok**;
  her işlem tipli ve girdisi izin listeli. Böyle bir uç nokta, yükseltilmiş
  bir süreçte keyfi kod çalıştırma demektir.
- Girdiler yükseltilmiş tarafta **yeniden** doğrulanır: çağıran kim olursa
  olsun. Windows'ta süreçlere argüman dizisi değil tek bir dize geçtiği için
  komut satırı üretimi de kaçış kurallarına göre yapılır ve testlidir —
  tırnak kaçıran bir değer, yükseltilmiş süreçte komut enjeksiyonu olurdu.

### 4.3 Kenar proxy modeli — aynı anda birden fazla web sunucusu

Laragon'un "Apache mı Nginx mi" seçimi mimari bir zorunluluk değil, sadece 80.
portu tek sürecin dinlemesinden kaynaklanıyor. Çözüm:

- 80/443'ü **yalnızca kenar** dinler, TLS'i orada sonlandırırız.
- Apache `127.0.0.1:8080`, Nginx `127.0.0.1:8081`, Node uygulaması `:3000`,
  konteyner `:32768` — hepsi düz HTTP, yalnız loopback'te.
- Yönlendirme host adına göre: `blog.test → Apache`, `api.test → Nginx`,
  `app.test → Node`. Proje `devbox.yaml`'ında `server: apache|nginx|caddy|proxy`.
- Bonus: kenar, istek/yanıtları **HTTP denetleyicisine** aynalar (bkz. Faz 7) ve
  tüm projelere ortak HSTS/CORS/gzip politikası uygular.

### 4.4 PHP çalıştırma modeli (Windows'ta PHP-FPM yok)

Bu, projenin en çok mühendislik isteyen teknik parçası; Laragon burada zayıf.

- Windows'ta `php-fpm` yok; elde `php-cgi.exe` (tek istek, sonra ölür) var.
- Çözüm: **kendi FastCGI süreç havuzu yöneticimiz**. `devboxd` N adet
  `php-cgi.exe -b 127.0.0.1:PORT` başlatır (`PHP_FCGI_MAX_REQUESTS` ile
  dönüşümlü yenileme), sağlığını dinler, ölürse yerine yenisini koyar,
  isteği en boş işçiye dağıtır.
- Havuz **proje başına**: farklı PHP sürümü, farklı `php.ini`, farklı bellek
  limiti, Xdebug açık/kapalı — hepsi izole.
- Apache tarafında `mod_proxy_fcgi` ile aynı havuza bağlanılır; `mod_php`
  kullanılmaz (iş parçacığı güvenliği ve sürüm kilitlenmesi yüzünden).
- Xdebug anahtarı: ayrı bir `php.ini` katmanı + havuzun sıcak yeniden başlatılması
  (bağlantılar boşalınca), böylece "Xdebug'ı aç" 1 saniye sürer.

### 4.5 Yerel alan adı — `hosts` değil, NRPT

Doğru TLD: **`.test`** (RFC 6761 ile ayrılmış). `.dev` kullanılmaz (Chrome'da
HSTS ön yüklemeli, zorunlu HTTPS), `.local` kullanılmaz (mDNS'e ait).

İki katmanlı çözüm:

1. `devboxd` içinde küçük bir **DNS sunucusu** (`127.0.0.53:53`), `*.test`'i
   `127.0.0.1`'e cevaplar; bilinmeyen her şeyi yukarı akışa iletmez, reddeder.

   Port neden 53: **NRPT kuralı yalnız bir sunucu IP'si alır, port taşıyamaz.**
   Yüksek bir portta dinleyip NRPT ile oraya yönlendirmek mümkün değil.
   Windows'ta 1024 altındaki portlar ayrıcalıklı olmadığı için bu yönetici
   hakkı gerektirmiyor (Unix'in aksine). IP olarak 127.0.0.1 yerine
   **127.0.0.53** seçildi: 127/8'in tamamı loopback'e bağlı olduğundan ayrı
   bir adres kullanmak, 127.0.0.1:53'ü tutan Docker Desktop / ICS / kurumsal
   DNS ajanlarıyla çakışmayı baştan önlüyor.
2. Helper, **NRPT** kuralı yazar:
   `Add-DnsClientNrptRule -Namespace ".test" -NameServers "127.0.0.1"`
   Böylece yalnızca `.test` sorguları yerel çözücüye gider; makinenin geri kalan
   DNS'i (VPN, kurumsal ağ) hiç etkilenmez. Bu, `hosts` yaklaşımının joker ve
   temizlik sorunlarını bir çırpıda çözer.
3. Geri düşüş: NRPT yazılamazsa (politika kısıtı) `hosts` dosyasına DevBox'un
   yönettiği işaretli blok yazılır — blok bütün olarak üretilir, elle düzenlenmez.

Sonuç: `siparis.proje.test`, `admin.proje.test` gibi joker alt alan adları
sıfır yapılandırmayla çalışır — Laragon'da mümkün değil.

### 4.6 TLS — iç CA + otomatik sertifika + yerel ACME

1. İlk açılışta **ECDSA P-256 kök CA** üretilir (10 yıl), özel anahtar
   kullanıcının DPAPI ile korunan dizininde durur.
2. Kök, üç yere kurulur — **Windows bu sırada onay penceresi gösterir**:
   - Windows güven deposu (`CurrentUser\Root`, gerekirse `LocalMachine\Root`)
   - **Firefox NSS veritabanı** (`certutil` ile her profile) — Laragon'un
     atladığı ve "Firefox'ta çalışmıyor" şikâyetlerinin tek sebebi
   - İsteğe bağlı: Java `cacerts`, WSL2 `/usr/local/share/ca-certificates`

   Onay penceresi bir engel değil, kasıtlı bir koruma: kök sertifika eklemek
   makinedeki tüm TLS trafiğini etkiler ve kullanıcının haberi olmadan
   yapılmamalıdır. Mimari sonucu şu: **bu iş ayrıcalıklı yardımcı servise
   verilemez.** Servis, masaüstü oturumuna pencere gösteremez; çağrı
   süresiz bloke olur. Kök kurulumu kullanıcının kendi oturumunda, açık bir
   komutla (`devbox trust install`) yapılmalı — ve bu, ayrıcalıklı işlem
   listesindeki tek "kullanıcı onayı zorunlu" madde.
3. Site sertifikaları: SAN'lı, kısa ömürlü (90 gün), `devboxd` arka planda
   30 gün kala **sessizce yeniler**. Yerel olarak güvenilen kök, CT ve 398 gün
   sınırından muaftır; yine de kısa ömür ilkesini koruyoruz.
4. **Yerel ACME sunucusu** (`https://acme.devbox.test/acme/directory`): Caddy'nin
   `acme_server` modülü. Caddy bağımlılığı **yalnız bunun için** eklenecek;
   kenar proxy'nin ona ihtiyacı olmadığı Faz 0'da görüldü. Böylece konteynerdeki Traefik, `certbot`, `lego` veya
   ekibin kendi aracı sertifikayı standart yolla alır. Bu özellik hiçbir rakipte
   hazır gelmiyor; asıl farklılaştırıcı burası.
5. `devbox cert trust --wsl` / `--java` / `--firefox` gibi açık komutlar; ne
   yapıldığı görünür olsun.

### 4.7 Veritabanı örnekleri

Tek bir "MySQL servisi" yerine **örnek (instance)** kavramı:

```
devbox db create pg17-main --engine postgres --version 17 --port 5433
devbox db create my8-shop  --engine mysql    --version 8.4 --port 3307
devbox db snapshot my8-shop --tag "migration-oncesi"
devbox db restore  my8-shop --tag "migration-oncesi"
```

- Her örnek: kendi veri dizini, kendi portu (otomatik tahsis), kendi conf'u.
- İlk kurulumda `initdb` / `mysqld --initialize-insecure` otomatik.
- Sürüm yükseltme yolu: `pg_upgrade` ve `mysql_upgrade` sarmalayıcıları,
  öncesinde otomatik anlık görüntü.
- Yönetim arayüzleri DevBox'un kendi web arayüzünden açılır: Adminer (tek dosya),
  pgAdmin ve phpMyAdmin isteğe bağlı bileşen olarak.
- Anlık görüntü/geri alma, Laragon'da hiç yok; günlük hayatta en çok işe yarayan
  özelliklerden biri olacak.

### 4.8 Proje tanım dosyası — depoya giren ortam

Projenin kökündeki `devbox.yaml`, `docker-compose.yml`'ın yerel karşılığı:

```yaml
name: magaza
domain: magaza.test          # magaza.test + *.magaza.test
server: nginx                # apache | nginx | caddy | proxy
root: public
php:
  version: "8.3"
  ini:
    memory_limit: 512M
    upload_max_filesize: 64M
  extensions: [redis, intl, gd]
  xdebug: off
services:
  - postgres@17
  - redis@7
  - mailpit
  - minio
env:
  DB_HOST: 127.0.0.1
  MAIL_HOST: 127.0.0.1
processes:                   # Procfile benzeri
  queue: php artisan queue:work
  vite:  npm run dev
cron:
  - schedule: "* * * * *"
    run: php artisan schedule:run
```

`devbox up` bu dosyayı okur, eksik runtime'ları indirir, alan adını ve
sertifikayı ayarlar, servisleri ayağa kaldırır. Ekip arkadaşı depoyu klonlayıp
`devbox up` der; ortam birebir aynı olur. **Laragon'un yapamadığı asıl şey bu.**

Çerçeve algılama: `composer.json` / `package.json` / `manage.py` okunarak
Laravel, Symfony, WordPress, Next.js, Django için `devbox.yaml` otomatik önerilir.

---

## 5. Windows'a özgü tuzaklar (baştan planlanmalı)

Bunlar sonradan keşfedilirse takvimi haftalarca kaydırır:

1. **Hyper-V dışlanan port aralıkları** — WSL2/Docker açıkken Windows
   `netsh int ipv4 show excludedportrange protocol=tcp` ile geniş aralıkları
   rezerve eder; 3306 veya 8080 birdenbire bağlanamaz hâle gelir. Port tahsis
   edicisi bu listeyi **her açılışta okumalı**.
2. **IIS / W3SVC** ve "World Wide Web Publishing Service" 80'i tutuyor olabilir;
   kurulum sihirbazı tespit edip devre dışı bırakmayı önermeli.
3. **Artık süreçler** — GUI/daemon çökerse `mysqld.exe`, `php-cgi.exe` ayakta
   kalır. Çözüm: tüm alt süreçler `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE` bayraklı
   bir **İş Nesnesi** içinde doğar.
4. **Windows Defender** — `node_modules`, veri dizinleri ve PHP dosya taraması
   yığını 3–5 kat yavaşlatır. Kurulumda dışlama önerisi (kullanıcı onaylı,
   sessizce değil) ve etkisini ölçen bir kıyas komutu.
5. **Sembolik bağlar** yönetici olmadan yalnız Geliştirici Modu açıkken kurulur;
   runtime sürüm değişimi için symlink yerine **shim `.exe`** kullanacağız
   (Scoop yaklaşımı) — `bin\php.exe` çağrılan dizine bakıp aktif sürümü seçer.
6. **PATH'i kirletmeme** — global PATH'e onlarca dizin eklemek yerine tek bir
   `%LOCALAPPDATA%\DevBox\bin` shim dizini.
7. **Uzun yol** desteği (`\\?\` ve `LongPathsEnabled`) — `node_modules` derinliği.
8. **CRLF/BOM** — üretilen conf dosyaları LF ve BOM'suz olmalı; Apache BOM'lu
   conf'ta anlaşılmaz hata verir.
9. **Dosya kilitleri** — çalışan sürecin `.exe`'si silinemez; güncelleyici
   "önce durdur, sonra değiştir, sonra başlat" sırasına uymalı, ayrıca
   `MoveFileEx(..., DELAY_UNTIL_REBOOT)` geri düşüşü.
10. **WSL2 birlikte çalışma** — dosyalar `\\wsl$` üzerinden erişilirse çok yavaş;
    WSL sürücüsü kullanılıyorsa proje dosyaları Linux dosya sisteminde durmalı.
    Yeni WSL'in `networkingMode=mirrored` ayarı localhost sorunlarını çözüyor;
    sürüm tespiti yapılmalı.
11. **SmartScreen** — imzasız kurulum "bilinmeyen yayıncı" uyarısı verir; itibar
    birikimi haftalar sürer. Kod imzalama sertifikası **1. günde** alınmalı.
12. **Antivirüs yanlış pozitifi** — süreç enjeksiyonu benzeri davranışlar
    (Job Object, port dinleme) bazı AV'leri tetikler; VirusTotal ve satıcı
    beyaz liste başvurusu sürece dahil.

---

## 6. Yol haritası

Tahminler 2 kişilik bir ekip içindir. **MVP Faz 3 sonunda** (≈ 4. ay).

### Faz 0 — Keşif ve mimari doğrulama · 3 hafta — **tamamlandı**
- Riskli parçalar için prototipler: FastCGI havuzu, iç CA + güven deposu,
  `*.test` çözücüsü + NRPT, kenar proxy.
- Prototipler tek kullanımlık olmadı: dördü de testleriyle birlikte `internal/`
  altında duruyor ve Faz 1–3'ün temeli olarak kullanılacak.
- Runtime kayıt defteri şeması ve `devbox.yaml` JSON Schema taslağı.
- **Kabul:** dört prototip de Windows 10 ve 11'de çalışıyor; NRPT'nin kurumsal
  politika altında yazılabilirliği doğrulandı.

### Faz 1 — Çekirdek · 6 hafta — **tamamlandı**
- `devboxd`: süreç denetçisi (İş Nesnesi, hazır olma ölçütleri, geri çekilmeli
  yeniden başlatma, halka tamponlu günlük), REST API + SSE günlük akışı.
- Ayrıcalıklı işlemler: kalıcı yardımcı servis yerine talep üzerine yükseltme
  (`devbox privileged`), tipli işlemler ve girdi izin listesi (bkz. 4.2).
- Runtime kayıt defteri: imzalı manifest, devam ettirilebilir indirme, SHA256
  doğrulama, sürümlü atomik kurulum, önbellek ve artık temizliği.
- `devbox` CLI: daemon, ps, logs, runtime, trust, dns, edge, serve.

**Kalan borç:** manifest yayın altyapısı (imzalama anahtarı ve gömülü açık
anahtar) ve shim üretimi henüz yok; ikincisi sürüm değiştirmenin PATH'i
kirletmeden yapılmasıyla ilgili ve Faz 2'de web sunucusu sürücüleriyle
birlikte ele alınacak.
- **Kabul:** `devbox runtime install php@8.3` bir dakikada biter, `devbox ps`
  süreçleri gösterir, daemon öldürülünce alt süreçler de ölür.

### Faz 2 — Web katmanı · 5 hafta — **tamamlandı**
- Gömülü Caddy kenarı, 80/443, host adına göre yönlendirme.
- Apache ve Nginx sürücüleri: şablondan vhost üretimi, atomik yazım, zarif
  yeniden yükleme, conf söz dizimi ön denetimi (`httpd -t`, `nginx -t`).
- PHP FastCGI havuz yöneticisi, proje başına `php.ini` katmanı, Xdebug anahtarı.
- Port tahsis edicisi (Hyper-V dışlama listesi ve IIS farkındalığı).
- **Kabul:** Apache'de bir WordPress ile Nginx'te bir Laravel **aynı anda**,
  farklı PHP sürümleriyle çalışıyor.

### Faz 3 — Alan adı, TLS ve MVP · 5 hafta — **kod tarafı tamamlandı**
- Yerel DNS sunucusu + NRPT kuralı + `hosts` geri düşüşü.
- İç CA, güven deposu kurulumu (Windows + Firefox NSS + WSL + Java).
- Otomatik sertifika üretimi ve sessiz yenileme.
- `devbox.yaml` okuyucu, çerçeve algılama, `devbox up` / `devbox down`.
- **Kabul (MVP):** temiz bir Windows sanal makinesinde kurulum → `devbox up` →
  `https://magaza.test` Chrome, Edge ve Firefox'ta **uyarısız** açılıyor.
  → **v0.5 kapalı beta**

  **Bu kriter henüz karşılanmadı.** Kod hazır ama gerçek bir Windows
  makinesinde hiç çalıştırılmadı. CI'nın kapsayamadığı yollar: NRPT kuralı
  (yönetici hakkı), Firefox NSS kurulumu (NSS araçları), UAC yükseltmesi
  (masaüstü oturumu), kök sertifika onay penceresi (masaüstü oturumu) ve
  Apache/Nginx'in Windows derlemeleri.

  Bu oturumun deneyimi kriteri ciddiye almayı destekliyor: en değerli üç
  hatayı (Windows CRLF, root olmayan kullanıcı, platforma bağlı
  `filepath.IsAbs`) testler değil gerçek ortamlar buldu.

### Faz 4 — Veritabanları · 5 hafta
- MySQL 8.x, MariaDB 11.x, PostgreSQL 14–17 sürücüleri; çoklu örnek, otomatik
  `initdb`, port tahsisi.
- Anlık görüntü / geri yükleme, zamanlanmış yedek, sürüm yükseltme sarmalayıcıları.
- Adminer gömülü; pgAdmin/phpMyAdmin isteğe bağlı bileşen.
- **Kabul:** PG 16 ve PG 17 örnekleri aynı anda ayakta; anlık görüntü alıp geri
  yükleme veri kaybı olmadan çalışıyor.

### Faz 5 — Posta ve yan servisler · 3 hafta
- ~~**Mailpit**~~ → **kendi yakalayıcımız** (`internal/mail`): SMTP yakalayıcı,
  MIME/ek çözümleme, posta kutusu arayüzü, JSON API, SSE canlı akış, arama.
  `mail.<alan-adı>` altında, `devbox up` ile birlikte açılıyor. ✅
- Kuyruk işçileri ve cron: `devbox.yaml`'daki `processes` ve `cron` blokları. ✅
- İsteğe bağlı gerçek röle (bir projede gerçekten posta göndermek gerekirse,
  açıkça izin verilen alan adlarına). ✅ Beyaz liste zorunlu, alt alan adları
  kapsanmıyor, parola yalnız ortam değişkeninden (`passwordEnv`).
- Redis (Memurai ya da Valkey), MinIO, Meilisearch bileşenleri. ✅ `services`
  bloğuyla projeyle birlikte açılıyor; bağlantı bilgileri süreçlere ortam
  değişkeni olarak geçiyor. RabbitMQ ertelendi: Erlang çalışma zamanı
  gerektiriyor ve runtime kayıt defteri olmadan kurulumu yönetilemiyor.
  Sürüm sabitleme de (`redis@7`) aynı altyapıyı bekliyor.
- **Kabul:** Laravel'den atılan posta 200 ms içinde arayüzde; `devbox mail api`
  ile son postanın gövdesi testten okunabiliyor.
  → **Kabul karşılandı** (`TestAPIStreamDeliversQuickly` 200 ms sınırını
  ölçüyor; `devbox mail latest` ve `/api/latest` gövdeyi döndürüyor). Postayı
  Laravel değil, stdlib `net/smtp` ve Python `smtplib` gönderdi — bağımsız
  yazılmış iki istemciyle çapraz doğrulama, tek bir çerçeveye bakmaktan daha
  çok şey söylüyor.

  **Neden Mailpit değil:** manifest yayın altyapısı yok, yani ikiliyi
  doğrulayarak indiremiyoruz (bkz. yukarıdaki fail-closed notu). Buna karşılık
  yakalamak — asla röle etmemek — için gereken SMTP altkümesi küçük ve sınırları
  belli. Mailpit ileride runtime kayıt defteri üzerinden bir seçenek olarak
  kalıyor.

  **Gerçek tarayıcıda çıkan bulgu:** HTML gövde ilk sürümde `srcdoc`'lu bir
  iframe'e konuyordu. Chromium'da denenince `srcdoc` belgesinin **üst sayfanın
  güvenlik ilkesini devraldığı** görüldü; bizim ilkemizde satır içi betik açık
  olduğu için koruma tümüyle `sandbox` özniteliğine kalmıştı. Gövde artık kendi
  ilkesini taşıyan ayrı bir uç noktadan geliyor (`default-src 'none'`, başlıkta
  `sandbox`) — adres çubuğuna yapıştırılsa bile betik çalışmıyor, takip
  pikselleri de `ERR_BLOCKED_BY_CSP` ile düşüyor.

### Faz 6 — GUI ve geliştirici deneyimi · 6 hafta
- ~~Tauri masaüstü uygulaması~~ → **çekirdek sürecin sunduğu denetim paneli**:
  proje listesi, tek tık başlat/durdur, canlı log görüntüleyici (filtre + arama
  + vurgulama), sağlık paneli. ✅ `devbox ui`
  - **Neden Tauri değil:** Tauri bir Rust derleme zinciri ve platform başına bir
    web görünümü kütüphanesi istiyor; Windows'ta derlenip çalıştırılmadan
    doğruluğu gösterilemez. Panel aynı API'yi kullanan tek dosyalık bir sayfa
    olduğu için gerçek bir tarayıcıda sınanabiliyor. Tauri/WebView2 kabuğu
    sonradan bu adrese bakan ince bir sarmalayıcı olarak eklenebilir.
  - Sürüm değiştirici runtime kayıt defterine bağlı; o altyapı (imzalı manifest)
    henüz yok. ⏳
- Kaynak kullanımı göstergeleri ✅ (panelde ve `devbox ps`'te bellek/işlemci;
  ölçüm ana sürecin kendisi, ağaç toplamı değil — bkz. `internal/procstat`).
  Sistem tepsisi ve oturum açılışında başlatma ⏳: ikisi de Windows'a özgü ve bu
  ortamda çalıştırılıp doğrulanamıyor (tepsi bir GUI araç zinciri, otomatik
  başlatma bir kayıt defteri anahtarı istiyor). Gerçek Windows denemesiyle
  birlikte yapılmalı.
- Proje şablonları (`devbox new laravel magaza`). ✅ Çerçevenin kendi kurucusu
  çağrılıyor (composer create-project, npm create, create-next-app, wp core
  download); düz PHP ve statik site için iki dosyalık kendi şablonumuz. Kurulan
  iskelet sonra algılamaya veriliyor. İçe/dışa aktarma ⏳
- VS Code eklentisi: durum çubuğu, hızlı komutlar, Xdebug tek tıkla.
- **Kabul:** Beta kullanıcılarının %80'i GUI ile kurulumdan ilk `https://` sayfaya
  10 dakikanın altında ulaşıyor (ölçülür).

### Faz 7 — İleri yetenekler · 6 hafta
- **Yerel ACME sunucusu** (`acme_server`) + `devbox cert` komutları.
- **Konteyner sürücüsü:** `devbox.yaml`'daki bir servis `driver: docker` ile
  konteynerde koşabilir; kenar proxy onu da yönlendirir. Yerel/konteyner karışık.
- **HTTP denetleyicisi:** kenardan aynalanan istek/yanıt akışı, gövde ve başlık
  incelemesi, tekrar gönderme (yerel Charles/Proxyman).
- **Tünelleme:** Cloudflare Tunnel / ngrok entegrasyonu ile `devbox share magaza`.
- Ekip paylaşımı: `devbox.yaml` + kilit dosyası ile birebir sürüm eşleme.
- **Kabul:** WSL2'deki bir konteyner, DevBox'un yerel ACME'sinden sertifika alıp
  `https://api.magaza.test` olarak yayınlanıyor.

### Faz 8 — Sağlamlaştırma ve 1.0 · 5 hafta
- Bağımsız **güvenlik denetimi** (odak: helper IPC ve LPE yüzeyi).
- Kod imzalama, MSI/`winget` paketi, delta güncelleyici, çökme raporlama
  (kullanıcı onaylı).
- Belgeler, Laragon/XAMPP'tan **göç aracı** (var olan siteleri ve DB'leri içe
  aktarır) — benimsemenin en kritik parçası.
- Performans kıyasları: soğuk başlatma, ilk istek gecikmesi, RAM.
- **Kabul:** denetimde yüksek/kritik bulgu yok; temiz kurulumdan çalışan siteye
  5 dakika; → **v1.0**

**Toplam: ≈ 44 hafta.** MVP 19. haftada.

---

## 7. Test ve CI stratejisi

- **Birim:** yapılandırma şablonları (üretilen conf'un altın dosyalarla
  karşılaştırılması), port tahsisi, sürüm çözümleme, manifest doğrulama.
- **Bütünleşme:** GitHub Actions `windows-latest` üzerinde gerçek servisleri
  ayağa kaldırıp HTTP/SQL isteği atan matris testleri
  (PHP 7.4/8.1/8.3/8.4 × Apache/Nginx × MySQL/MariaDB/PostgreSQL).
- **Kurulum testi:** her sürüm için temiz Windows Sandbox / VM anlık görüntüsünde
  uçtan uca kurulum; "yükseltme" senaryosu için önceki sürümden geçiş.
- **Sertifika testi:** üretilen sertifikanın gerçek Chrome ve Firefox ile
  headless doğrulanması (uyarı yok kontrolü) — regresyonun en sinsi olduğu yer.
- **Kaos:** daemon `taskkill /f` ile öldürülür; artık süreç kalmamalı, yeniden
  başlatmada durum tutarlı olmalı.

---

## 8. Lisans ve hukuki notlar

Bileşenleri **kurulum sırasında indirmek**, kuruluma gömmekten hukuken çok daha
rahat; özellikle MySQL için (Oracle, GPL) bu tercih edilmeli.

| Bileşen | Lisans | Not |
|---|---|---|
| Apache httpd | Apache-2.0 | Serbest; Windows derlemeleri için Apache Lounge |
| Nginx | 2-clause BSD | Serbest |
| Caddy | Apache-2.0 | Yalnız ACME sunucusu için, Faz 7'de |
| PHP | PHP License | Serbest, adlandırma kısıtına dikkat |
| MySQL | GPL-2.0 (istisnalı) | **İndirerek kur**, paketleme yapma |
| MariaDB | GPL-2.0 | Aynı yaklaşım |
| PostgreSQL | PostgreSQL Lisansı | En rahatı, gömülebilir |
| Mailpit | MIT | Gömülebilir |
| Adminer | Apache-2.0 / GPL-2.0 | Serbest |

DevBox'un kendisi için öneri: çekirdek **Apache-2.0** (patent maddesi güven verir),
ticarileşme istenirse ekip/uzak özellikleri ayrı katmanda.

---

## 9. Risk kaydı

| Risk | Etki | Önlem |
|---|---|---|
| NRPT kurumsal politikayla engellenir | Alan adları çalışmaz | `hosts` geri düşüşü Faz 3'te birlikte yazılır, sonradan değil |
| FastCGI havuzu kararsız olur | PHP projeleri güvenilmez | Faz 0'da prototip; yük altında 24 saat dayanma testi |
| Helper'da ayrıcalık yükseltme açığı | Kritik güvenlik | Genel amaçlı uç nokta yok; izin listesi; bağımsız denetim |
| SmartScreen/AV yanlış pozitifi | Kurulum terk edilir | Kod imzalama 1. günde; VirusTotal takibi; satıcı başvuruları |
| Laravel Herd hızla eşitler | Farklılaşma erir | Farkı çoklu-DB, çoklu-sunucu, ACME ve açık mimaride tut |
| Runtime derlemelerinin kaynağı kesilir | Kurulum bozulur | Manifest'te ayna URL'leri; yerel önbellek; SHA256 sabitleme |
| Kapsam şişmesi | 1.0 gecikir | Faz 3 MVP'si sabit; Faz 7 maddeleri gerekirse 1.1'e |

---

## 10. İlk iki hafta — somut başlangıç

1. Depoyu kur: `cmd/devboxd`, `cmd/devbox`, `internal/supervisor`,
   `internal/runtime`, `internal/certs`, `internal/dns`, `internal/webserver`.
2. **Prototip A:** 4 işçilik `php-cgi.exe` FastCGI havuzu + bir istek dağıtıcı;
   1000 eşzamanlı istek altında sızıntı ve çökme ölçümü.
3. **Prototip B:** `Add-DnsClientNrptRule` ile `.test` yönlendirmesi; VPN açıkken
   ve kurumsal makinede davranış.
4. **Prototip C:** Go ile iç CA üret, `CurrentUser\Root`'a ve Firefox NSS'e kur,
   üretilen sertifikayla Chrome + Firefox'ta uyarısız `https://` doğrula.
5. **Prototip D:** kenar proxy — 443'ü dinle, host adına göre
   `127.0.0.1:8080` ve `:8081`'e dağıt. *(Caddy gömmeyi denemeden önce
   stdlib'in yettiği görüldü; bkz. bölüm 0.)*
6. Kod imzalama sertifikası başvurusunu başlat (teslim süresi haftaları bulur).

---

## 11. Karar bekleyen konular

1. **Ad ve TLD:** ürün adı ve varsayılan TLD (`.test` öneriliyor; `.devbox` gibi
   özel bir TLD sadece NRPT ile mümkün, ama standart dışı).
2. **Lisans modeli:** tamamen açık kaynak mı, çekirdek açık + ekip özellikleri
   ticari mi?
3. **Konteyner sürücüsü Faz 7'de mi, yoksa hiç mi?** Yerel hız ana vaadimizse
   opsiyonel kalmalı.
4. **Hedeflenen en eski Windows:** yalnız Windows 11 mi, Windows 10 22H2 de mi?
   (WSL `mirrored` ağ ve WebView2 varsayımları buna bağlı.)
5. **GUI çerçevesi:** Tauri 2 öneriliyor; ekipte Rust deneyimi yoksa .NET 8 +
   WinUI 3 alternatifi değerlendirilmeli.
