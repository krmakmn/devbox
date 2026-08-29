# DevBox

Windows için yerel geliştirme ortamı — Laragon'un kolaylığı, DDEV'in yeniden
üretilebilirliği, Herd'ün cilası.

> **Durum: Faz 7 sürüyor.** Faz 0'ın dört prototipi (PHP havuzu, yerel CA,
> `*.test` çözücüsü, kenar proxy) ve Faz 1'in dört maddesi (runtime kayıt
> defteri, süreç denetçisi, çekirdek servisin API'si, ayrıcalıklı işlemler)
> hazır. Faz 2 (Apache/Nginx sürücüleri, port tahsisi, php.ini) ve Faz 3'ün
> kod tarafı (`devbox.yaml`, çerçeve algılama, `devbox up`/`down`) tamamlandı.
> Faz 4'te veritabanı örnekleri, Faz 5'te posta yakalayıcı, zamanlanmış
> görevler ve yan servisler çalışıyor. Faz 6'da proje kaydı ve denetim
> paneli geldi: `devbox ui` ile projeler listeleniyor, tek tıkla başlayıp
> duruyor, günlükler canlı akıyor.
>
> **MVP'nin kabul kriteri henüz karşılanmadı:** "temiz bir Windows'ta
> `devbox up` → tarayıcıda uyarısız `https://`". NRPT, Firefox NSS, UAC ve
> Windows'un Apache/Nginx derlemeleri gerçek bir makinede hiç denenmedi;
> CI bunları kapsayamıyor.
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
| `internal/database` | Veritabanı örnekleri: PostgreSQL, MySQL, MariaDB — çoklu örnek, anlık görüntü |
| `internal/project` | `devbox.yaml` okuma ve çerçeve algılama (Laravel, WordPress, Symfony, Next.js, Django) |
| `internal/webserver` | Apache ve Nginx için vhost üretimi, söz dizimi ön denetimi, geri alma |
| `internal/ports` | Port tahsisi: Hyper-V rezervasyonları, IIS farkındalığı, açıklayıcı hata |
| `internal/phpini` | Proje başına php.ini katmanlama ve Xdebug anahtarı |
| `internal/edge` | 80/443'ü dinleyip host adına göre dağıtan ters vekil |
| `internal/dns` | `*.test` için yetkili, özyinelemesiz çözücü (UDP + TCP) |
| `internal/nrpt` | Windows Ad Çözümleme İlkesi Tablosu'na kural ekleme |
| `internal/hostsfile` | NRPT engellenirse geri düşüş: hosts dosyasında yönetilen blok |
| `internal/mail` | SMTP yakalayıcı, MIME çözümleme, posta kutusu arayüzü ve API |
| `internal/tunnel` | Geçici genel adres: cloudflared/ngrok sarmalayıcısı |
| `internal/lockfile` | Çalışan sürümlerin kaydı ve karşılaştırması (`devbox.lock`) |
| `internal/inspect` | HTTP denetleyicisi: kenardan geçen istek/yanıt kaydı ve tekrar gönderme |
| `internal/container` | Konteyner sürücüsü: docker/podman ile servis çalıştırma |
| `internal/acme` | Yerel ACME (RFC 8555) sunucusu: JWS doğrulama, http-01, CSR imzalama |
| `internal/procstat` | Süreçlerin bellek ve işlemci kullanımı (Linux /proc, Windows psapi) |
| `internal/scaffold` | Proje şablonları: çerçevenin kendi kurucusunu çağırır |
| `internal/projects` | Proje kaydı ve projeleri çekirdek süreç üzerinden çalıştırma |
| `internal/services` | Yan servisler: Redis/Valkey, Meilisearch, MinIO — port tahsisi, proje başına veri dizini |
| `internal/cron` | Zamanlanmış görevler: cron ifadesi ayrıştırma ve üst üste binmeyen çalıştırma |
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

Veritabanı tarafında: Laragon'da tek bir MySQL servisi vardır — tüm projeler
onu paylaşır, sürüm değiştirmek herkesi etkiler, bir projeyi bozan geçiş
diğerini de bozar. Buradaki birim **örnek**: her birinin kendi sürümü, kendi
veri dizini ve kendi portu var; PostgreSQL 16 ile 17 aynı anda ayakta durabilir.
Port ataması kalıcı, örnek durmuş olsa bile portu başkasına verilmiyor.

**Anlık görüntü veri dizininin birebir kopyası, SQL dökümü değil.** İlk tasarım
`pg_dumpall` kullanıyordu; gerçek bir kümede denenince kırıldı — üretilen dosya
`DROP ROLE postgres` içeriyor ve bunu çalışan bir kümeye uygulamak "current user
cannot be dropped" ile düşüyor, üstelik veritabanı zaten düşürüldükten sonra.
Yani kullanıcı eskisinden kötü bir durumla kalıyordu. Dizin kopyası bu sorunu
tümden ortadan kaldırıyor ve istenen şeye daha yakın: "geçişten önceki hâle
dön" demek, bit bit o hâle dönmek demek — roller, ayarlar, diziler dahil.
Sürümler arası taşıma için SQL dökümü ayrı bir komut: `devbox db export`.

Hazır olma ölçütü TCP değil günlük satırı: MySQL ve MariaDB portu, bağlantı
kabul etmeye hazır olmadan önce açıyor.

Posta tarafında: Laragon'un yaptığı, `php.ini`'de `sendmail_path`'i bir
yakalayıcıya çevirmekten ibaret; postanın nereye gittiği projeye değil makineye
bağlı. DevBox'ta yakalayıcı **öntanımlı açık** ve `devbox.yaml`'a yazılı — test
verisindeki gerçek bir adrese kazara posta gitmesi, bu araçların önlemesi
gereken en pahalı hatalardan biri. Kutu `https://mail.<alan-adı>` altında
açılıyor; proje sertifikası `*.<alan-adı>` joker adını kapsadığı için ayrı
sertifika, çözücü son eki sahiplendiği için ayrı DNS kaydı gerekmiyor.
Süreçlere `MAIL_HOST`/`MAIL_PORT` ortam değişkenleri veriliyor — `devbox.yaml`'da
açıkça yazılmış bir değer varsa o korunuyor.

Mailpit yerine kendi yakalayıcımızı yazdık. İki gerekçe: manifest yayın
altyapısı henüz olmadığı için ikiliyi doğrulayarak indiremiyoruz; ve yakalamak
(asla röle etmemek) için gereken SMTP altkümesi küçük, sınırları belli ve
yanlış davrandığında sessiz kalmıyor. Mailpit ileride runtime kayıt defteri
üzerinden bir seçenek olarak durabilir.

**Yakalanan posta güvenilmez içeriktir.** Uygulamanın gönderdiği HTML'de
kullanıcıdan gelen veri olur; onu arayüze doğrudan basmak, postayı tetikleyen
kişiye posta kutusunun kökeninde betik çalıştırma imkânı verir. İlk sürüm HTML
gövdeyi `srcdoc`'lu bir iframe'e koyuyordu; Chromium'da denenince görüldü ki
`srcdoc` belgesi **üst sayfanın güvenlik ilkesini devralıyor** — bizim ilkemizde
satır içi betik açık olduğu için koruma tümüyle `sandbox` özniteliğine kalmıştı.
Şimdi gövde kendi ilkesini taşıyan ayrı bir uç noktadan geliyor
(`default-src 'none'`, başlıkta `sandbox`), yani adres çubuğuna yapıştırılsa
bile betik çalışmıyor. Yan faydası: takip pikselleri de engelleniyor — gerçek
bir tarayıcıda `ERR_BLOCKED_BY_CSP` ile düştükleri doğrulandı.

Röle isteğe bağlı ve **beyaz listeli**. Bir projede gerçekten posta göndermek
gerekirse `mail.relay` yazılır; yalnız listelenen alıcılara gider, geri kalan
her şey yalnız yakalanır. Liste boşsa yapılandırma reddediliyor — "hepsine
gönder" kısayolu aracın var oluş sebebini ortadan kaldırırdı. Alt alan adları
kapsanmıyor: `sirket.com` yazan biri `test.sirket.com`a posta gitmesini istemiş
sayılmaz. Parola `devbox.yaml`'a değil ortam değişkenine yazılıyor (`passwordEnv`),
çünkü bu dosya depoya giriyor. Röle edilen posta da yakalanıyor ve sonucu
arayüzde görünüyor — "gitti mi gitmedi mi" sorusu cevapsız kalmıyor.

Kilit dosyası tarafında: `devbox.yaml` niyeti anlatıyor ("PHP 8.3 istiyorum"),
`devbox.lock` gerçekleşeni ("8.3.14 çalıştı"). Aradaki fark bazen bir hatanın
sizde görünüp ekip arkadaşınızda görünmemesi demek. `devbox lock check` farkı
söylüyor: "kilitte 7.4.1, makinede 7.0.15". Kilit bir **rapor**, zorlayıcı değil
— eksik sürümü indirmek imzalı manifest altyapısına bağlı ve o henüz yok. Bugün
yaptığı şey farkı göstermek; bu tek başına, sorunu saatlerce aramaktan iyi.
Konteynerlerde etiket değil **içerik özeti** kilitleniyor: `nginx:alpine`
zamanla farklı bir imaja işaret edebilir.

Tünel tarafında: `devbox share` projeyi geçici bir genel adresle açıyor —
webhook denemeleri için. Kendi tünel sunucumuz yok: tünel internete açık bir
sunucu gerektiriyor ve birinin onu işletmesi, ödemesi, güvenliğini üstlenmesi
gerekiyor; DevBox yerel bir araç. Onun yerine kullanıcının seçtiği sağlayıcının
kendi aracı (cloudflared ya da ngrok) çalıştırılıyor. Komut her seferinde
uyarıyor: tünel açmak, hata ayıklama araçları ve denetleyiciyle birlikte
geliştirme makinenizi internete açmak demek.

Denetleyici tarafında: `https://inspect.<alan-adı>` kenardan geçen her isteği ve
yanıtı gösteriyor — başlıklar, gövdeler, süre — ve bir isteği değiştirmeden
tekrar gönderebiliyor. Bugünkü seçenekler tarayıcının geliştirici araçları
(yalnız tarayıcıdan çıkan isteği görür, sunucudan sunucuya gideni değil) ya da
Charles/Proxyman (ayrı vekil, ayrı kök sertifika, ayrı kurulum). DevBox'ın kenarı
zaten bütün trafiğin geçtiği yer.

Üç karar: kayıt **öntanımlı açık** (bir hata ayıklama aracının "önce beni aç,
sonra hatayı tekrar üret" demesi, en çok ihtiyaç duyulan anda elde olmaması
demek), yalnız **bellekte** (diske yazmak, kullanıcının haberi olmadan parola ve
jeton içeren istekleri kalıcılaştırmak olurdu) ve gövdeler **64 KB'de kesiliyor**
(bir dosya yükleme isteği belleği tek başına doldurabilir; kesilme arayüzde
söyleniyor). Tekrar gönderme, isteği arka uca değil **kendi kenarımıza** geri
gönderiyor: aksi hâlde kenarın eklediği başlıklar atlanır ve "aynı isteği bir
daha yap" sözü tutulmamış olurdu.

Denetleyicinin kendi trafiği kaydedilmiyor — her yoklama yeni bir kayıt üretir,
o kayıt akışa düşer, arayüz yeniden yoklar: kendini besleyen bir döngü.

Konteyner tarafında: `devbox.yaml`'daki bir servis `driver: docker` ile
konteynerde koşabiliyor; kenar vekili ona da alan adı verip TLS'i sonlandırıyor.
Her bileşen yerel ikili olarak kurulamıyor — Windows'ta resmî derlemesi olmayan
ya da kurulumu makineyi kirleten şeyler var.

Konteyner `docker run -d` ile arka planda değil, **denetçinin altında ön planda**
çalışıyor. Böylece çıktısı doğrudan servisin halka tamponuna düşüyor, süreç ömrü
konteyner ömrüyle eşleşiyor ve yeniden başlatma ilkesi, hazırlık ölçütü, durum
bildirimi denetçiden geliyor — ikinci bir yaşam döngüsü yönetimi yazmıyoruz.

Port yalnız `127.0.0.1`'e yayınlanıyor. `-p 8080:80` yazmak konteyneri makinenin
tüm arayüzlerinde açar ve aynı ağdaki herkes geliştirme veritabanınıza
bağlanabilir; Docker bu öntanımlıyla güvenlik duvarı kurallarını da atladığı için
sık yaşanan bir kazadır. Bağlanan dizinler de proje dışına çıkamıyor: `devbox.yaml`
depodan geliyor ve klonlayanın makinesinde istediği dizini konteynere açmaya
yetkisi olmamalı.

ACME tarafında: `devbox acme serve` yerel bir RFC 8555 sunucusu açıyor. Amaç,
DevBox'ın dizinine girmeyen şeylerin de sertifika alabilmesi — WSL2'deki bir
konteynerde koşan Caddy, Traefik ya da certbot. Onların bildiği tek dil ACME.
Pebble ya da Smallstep gibi hazır bir sunucu ikinci bir CA demek olurdu;
kullanıcının iki kök sertifikaya güvenmesi gerekirdi. Bizimki zaten kurulu olan
CA'yı kullanıyor.

Bir tasarım ayrıntısı ilginç: http-01 doğrulaması alan adının 80. portuna
bağlanır, ama o portu zaten DevBox'ın kenar vekili tutuyor — yani konteynerdeki
istemcinin sunduğu meydan okumayı değil, kenarın 404'ünü görürdük. Bu yüzden
`-map alan=adres:port` ile doğrulamanın nereye gideceği söylenebiliyor; istek
alan adına gidiyormuş gibi görünüyor (Host başlığı korunuyor) ama soket
yönlendirilen adrese açılıyor.

Sertifika **yalnız doğrulanmış adlar** için veriliyor; CSR'ın kendi SAN
listesine güvenilmiyor. Ona güvenmek, bir meydan okumayı geçen istemcinin
istediği adı — `banka.example.com` dahil — alması demekti. Bir test bunu
sabitliyor.

Doğrulama tarafında: protokolü kendi testlerimizle sınamak yetmez, çünkü yanlış
anladığım bir yer varsa test de aynı yanlışı yapar. Bu yüzden `tests/acme-client`
altında **ayrı bir Go modülü** var; bağımsız yazılmış `lego` istemcisi akışı
baştan sona tamamlıyor ve CI bunu her itmede koşuyor. Ayrı modül olması, `lego`nun
DevBox'ın bağımlılığı olmaması demek. RFC 7638 parmak izi de RFC'deki bilinen
vektörle sınanıyor.

Arayüz tarafında: yol haritası Tauri masaüstü uygulaması diyordu. Tauri bir Rust
derleme zinciri ve platform başına bir web görünümü kütüphanesi gerektiriyor —
Windows'ta derlenip çalıştırılmadan doğruluğu gösterilemeyecek bir katman. Bu
depodaki kural, yazılan her şeyin çalıştığının gösterilmesi. Bu yüzden arayüz
**çekirdek sürecin sunduğu yerel bir sayfa**: aynı API'yi kullanıyor, tek dosya,
derleme adımı yok, çevrimdışı çalışıyor ve gerçek bir tarayıcıda sınanabiliyor.
Tauri (ya da Windows'ta zaten kurulu olan WebView2) kabuğu sonradan bu adrese
bakan ince bir sarmalayıcı olarak eklenebilir; mimari onu dışlamıyor, yalnız
kanıtlanabilir olanı önce yapıyor.

Panelin projeleri başlatma biçimi de bilinçli: `devbox up`'ı **alt süreç olarak**
çalıştırıyor, kütüphane olarak çağırmıyor. Böylece çöken bir proje çekirdeği
düşürmüyor, her projenin günlüğü kendi halka tamponunda duruyor ve komut
satırından çalıştırılan `devbox up` ile arayüzden başlatılan proje birebir aynı
kodu çalıştırıyor — "arayüzde çalışıyor ama CLI'da çalışmıyor" durumu oluşmuyor.

Proje kaydı Laragon'un `www` klasörü kuralını benimsemiyor: proje dizini
istediğiniz yerde durabilir, kayıt yalnız bir işaretçi. Kaydın tuttuğu tek kalıcı
bilgi dizin yolu; ad, alan adı ve sunucu her okumada `devbox.yaml`'dan
tazeleniyor — aksi hâlde dosyada alan adını değiştirdiğinizde arayüz eski değeri
gösterir ve hangisinin doğru olduğu belirsizleşir. Dizini silinen bir proje
kayıttan kendiliğinden düşmüyor, "dizini eksik" diye işaretleniyor: sessizce
silmek geri alınamayan bir karar.

Tarayıcı `Authorization` başlığı gönderemediği için panele çerez tabanlı bir
oturum var: `devbox ui` adresi `?jeton=…` ile açıyor, sunucu çerezi kurup jetonsuz
adrese yönlendiriyor — jeton adres çubuğunda ve geçmişte kalmıyor. Çerezin
getirdiği CSRF riski üç katmanla kapatılıyor: `SameSite=Strict`, durum değiştiren
isteklerde `Origin` denetimi ve zaten var olan `Host` denetimi.

VS Code eklentisi `editors/vscode` altında. İş mantığının tamamı (çekirdek
süreçle konuşan katman ve `devbox.yaml` düzenleyicisi) VS Code API'sinden
bağımsız iki dosyada; onlar düz node ile ve **gerçek bir `devboxd`'ye karşı**
sınanıyor, CI'da da öyle koşuyor. Doğrulanmayan tek kısım editör yapıştırması
(durum çubuğu, komut kaydı) — çalıştırmak VS Code gerektiriyor. Sınır eklentinin
kendi README'sinde açıkça yazılı.

Kaynak göstergeleri sürecin **kendisini** ölçüyor, ağacını değil: panelde
"(ana süreç)" diye etiketli. Ağaç toplamı Linux'ta /proc'u taramak, Windows'ta
anlık görüntü API'siyle bütün süreç listesini gezmek demek — ikincisi bu ortamda
çalıştırılıp doğrulanamayacak hatırı sayılır miktarda syscall kodu. İşlemci
tarafında API birikmiş süreyi veriyor, oranı iki yoklama arasındaki farktan
arayüz hesaplıyor; böylece ölçüm paketi durum tutmuyor.

Şablon tarafında: `devbox new laravel magaza` bir "Laravel şablonu" açmıyor,
**Laravel'in kendi kurucusunu** çağırıyor (`composer create-project`). Şablon
tutmak, çerçevenin dosya düzenini bu depoya kopyalamak demek olurdu; o kopya ilk
günden eskimeye başlar ve kullanıcı bir yıl önceki iskeletle başlayıp sorunu
DevBox'a yazar. DevBox'ın işi ortam kurmak, çerçeve dağıtmak değil. Kurulan
iskelet sonra **algılamaya** veriliyor — şablon adına değil, kurucunun diskte
bıraktığına bakılıyor.

Bunu gerçek kurucularla denerken bir hata çıktı: `create-vite` verilen yolu
çalışma dizinine ekliyor, yani mutlak yol verilince proje
`/tmp/yeni/tmp/yeni/arayuz` gibi bir yere kuruluyordu. Komut artık hedefin üst
dizininde çalışıp yalnız adı alıyor. Regresyon testi, çağrıldığı dizini ve
argümanlarını yazan sahte bir kurucuyla bunu sabitliyor.

Yan servis tarafında: `devbox.yaml`'daki `services` bloğu Redis, Meilisearch ve
MinIO'yu projeyle birlikte açıyor; her projenin kendi veri dizini var, portlar
tahsis ediliyor ve bağlantı bilgileri süreçlere ortam değişkeni olarak
geçiyor (`REDIS_URL`, `MEILISEARCH_HOST`, `AWS_ENDPOINT`…). Hepsi yalnız
loopback'i dinliyor. İkili indirilmiyor, **bulunuyor**: manifest yayın
altyapısı olmadığı için doğrulanmış indirme yapılamıyor, o yüzden PATH'te ya da
DevBox'ın runtime dizininde aranıyor ve bulunamazsa kurulum yolunu söyleyen
açık bir hata veriliyor. Sürüm sabitleme (`redis@7`) de aynı altyapıyı
bekliyor; şimdilik istenen sürüm karşılanamazsa uyarı veriliyor.

Kapanış tarafında çıkan bir hata: `devbox up`'ı Ctrl+C ile kapatmak 20 saniye
sürüyordu. Sebep, denetçinin durdurma sinyalini yalnız başlattığı sürece
göndermesiydi. Oysa yapılandırmadaki komutlar çoğunlukla sarmalayıcı
(`sh -c ...`, `npm run dev`); sarmalayıcı ölse bile asıl işi yapan torun ayakta
kalıyor ve boruları açık tuttuğu için `cmd.Wait()` dönmüyordu — iki kez
`StopTimeout` bekleniyordu. Sinyal artık süreç ağacına gidiyor ve SIGINT değil
SIGTERM: POSIX, iş denetimi kapalıyken arka plana atılmış komutların SIGINT'i
yok saymasını şart koşuyor. Kapanış 20 saniyeden 1,5 saniyeye indi.

Zamanlanmış görev tarafında: Laragon'da böyle bir şey yok, Windows'un Görev
Zamanlayıcısı ise depoya yazılamaz. Oysa Laravel'in `schedule:run`'ı dakikada
bir çalışmak zorunda. `devbox.yaml`'daki `cron` bloğu bunu üstleniyor.
Zamanlama ifadesi **yapılandırma okunurken** çözülüyor: yazım hatası, görevin
aylarca sessizce hiç çalışmaması yerine `devbox up`ta hemen görünüyor. Bir
çalıştırma bitmeden sırası gelen ikincisi atlanıyor — `schedule:run` zaman zaman
bir dakikadan uzun sürer ve onu paralel başlatmak aynı kuyruk işini iki kez
işlemek demektir.

`devbox.yaml` tarafında: ortam makineye değil **depoya** yazılıyor. Ekip
arkadaşı klonlayıp `devbox up` diyor; aynı PHP sürümü, aynı uzantılar, aynı web
sunucusu, aynı alan adı geliyor. `devbox init` projeyi tanıyıp makul
varsayılanları öneriyor — WordPress için Apache seçiyor, çünkü kalıcı bağlantılar
`.htaccess` yeniden yazma kurallarına dayanıyor; Next.js için `proxy` seçip
geliştirme sunucusunu `processes`'e ekliyor.

Bilinmeyen alanlar hata veriyor: `worker: 4` yazan biri (doğrusu `workers`)
sessizce varsayılanla çalışmak yerine yazım hatasını hemen görüyor.

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

Go 1.23+ yeterli. Tek dış bağımlılık `gopkg.in/yaml.v3` — `devbox.yaml` için.
YAML'i elle ayrıştırmak ilk bakışta mümkün görünüyor (bize yalnız iç içe
eşlemeler ve dizeler lazım) ama biçimin yüzeyi geniş: çok satırlı dizeler,
çapalar, alıntılama kuralları, girinti incelikleri. Her biri kullanıcının yazıp
da çalışmayacağı bir şey. Tek, olgun ve kendi bağımlılığı olmayan bir kütüphane
bu riskten ucuz.

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
