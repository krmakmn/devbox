# Güvenlik gözden geçirmesi

Yol haritası bu faz için **bağımsız bir güvenlik denetimi** öngörüyordu.
Bu belge o değil ve onun yerine geçmez: aynı kişinin yazdığı kodu aynı
kişinin denetlemesi, gözden kaçanı ikinci kez kaçırmak demektir. Bu,
saldırı yüzeyinin sistematik bir dökümü ve alınan kararların gerekçesi —
bağımsız denetim yaptıracak kişinin nereden başlayacağını bilmesi için.

## Saldırı yüzeyi

DevBox'ın dışarıya baktığı her yer:

| Yüzey | Dinlediği yer | Kimlik doğrulama | Not |
|---|---|---|---|
| Kenar vekili (site) | `:80`, `:443` — **tüm arayüzler** | yok | Kasıtlı: siteyi telefondan denemek |
| Posta kutusu | kenarın arkasında | **yalnız geri döngü** | Yakalanmış postalar |
| HTTP denetleyicisi | kenarın arkasında | **yalnız geri döngü** | Kaydedilmiş gövdeler ve başlıklar |
| Çekirdek API (`devboxd`) | `127.0.0.1:0` | jeton + Host + Origin | Denetim paneli buradan |
| ACME sunucusu | `127.0.0.1:14000` | JWS imzası + nonce | |
| SMTP yakalayıcı | `127.0.0.1:1025` | yok (kasıtlı) | Yalnız geri döngü |
| Yerel çözücü | `127.0.0.53:53` | yok | Yalnız `*.test` yanıtlar |
| Veritabanları | `127.0.0.1:<port>` | parolasız (kasıtlı) | Yalnız geri döngü |
| Konteynerler | `127.0.0.1:<port>` | imaja bağlı | `-p` geri döngüye sabit |

### Bu gözden geçirmede bulunan ve düzeltilen

**Hata ayıklama yüzeyleri yerel ağa açıktı.** Kenar 80/443'ü tüm
arayüzlerde dinliyor (siteyi telefondan denemek için, bilinçli bir
karar). Ama kenarın arkasında yalnız site yoktu: posta kutusu yakalanmış
postaları, HTTP denetleyicisi ise kaydedilmiş istek gövdelerini ve
`Authorization` başlıklarını gösteriyordu. Aynı kahvehane ağındaki
biri `http://inspect.magaza.test/` adresini açabilirdi.

İkisi artık `edge.LoopbackOnly` ile sarılı: karar `RemoteAddr`'a
dayanıyor, `Host` ya da `X-Forwarded-For` gibi istemcinin yazabildiği
alanlara değil. Site herkese açık kalıyor, hata ayıklama yüzeyleri
kapandı.

**Tarayıcı açma komutu kabuktan geçiyordu.** `devbox ui`, Windows'ta
adresi `cmd /c start <adres>` ile açıyordu ve `cmd`, adresteki `&`
karakterini komut ayracı sayar. Adres bugün bizim ürettiğimiz uç nokta
ve jetondan geliyor, yani sömürülebilir değildi; ama adresi başka bir
yerden alan bir değişiklik bunu sessizce komut çalıştırmaya
dönüştürürdü. Artık `rundll32 url.dll,FileProtocolHandler` kullanılıyor:
kabuk yok. Ayrıca şema ve denetim karakteri denetimi ikinci katman
olarak eklendi.

## Bilinçli olarak açık bırakılanlar

Bunlar bulgu değil, karar. Bir denetim bunları da sorgulamalı.

**Veritabanları parolasız.** Yerel geliştirme veritabanları yalnız geri
döngüyü dinliyor ve parola sormuyor. Ağa açık bir kurulumda kabul
edilemez; burada kasıtlı, çünkü her projeye parola yönetmek DevBox'ın
çözmeye çalıştığı sürtünmenin ta kendisi. Risk: makinede çalışan başka
bir kullanıcı ya da kötü niyetli bir yerel süreç bağlanabilir.

**SMTP yakalayıcı her kimlik bilgisini kabul ediyor.** Uygulamaların
`.env` dosyasındaki parola ne olursa olsun çalışsın diye. Yalnız geri
döngüde ve postayı asla röle etmiyor (röle ayrıca yazılmadıkça).

**Denetleyici `Authorization` başlıklarını kaydediyor.** "Neden 401
aldım" sorusunun cevabı çoğu zaman tam orada. Kayıt yalnız bellekte,
DevBox kapanınca gidiyor ve artık yalnız geri döngüden erişilebiliyor.

**Yerel CA'nın özel anahtarı diskte.** `0600` izinle, kullanıcının veri
dizininde. Bu anahtarı ele geçiren biri, o makinenin tarayıcılarının
güvendiği herhangi bir alan adı için sertifika üretebilir. Alternatifi
(donanım anahtar deposu) yerel bir geliştirme aracı için orantısız.

**`devbox share` makineyi internete açıyor.** Komut her seferinde
uyarıyor; açılan şey hata ayıklama yüzeylerini de kapsayabilir. (Tünel
sağlayıcısı isteği dışarıdan getirdiği için `RemoteAddr` geri döngü
görünür — yani `LoopbackOnly` koruması tünelde geçerli **değil**. Tünel
açarken bunu bilerek yapıyorsunuz.)

## Ayrıcalık yüzeyi

Yol haritası "helper IPC ve LPE yüzeyi" demişti. O yüzey **yok**: Faz
1'de kalıcı ayrıcalıklı yardımcı servis fikri terk edildi.

- Ayrıcalık gerektiren işlemler yılda birkaç kez çalışan tek seferlik
  işler (NRPT kuralı, hosts dosyası, kök sertifika).
- Her biri için DevBox kendini **yalnız o işlem için** yeniden
  başlatıyor (`devbox privileged <işlem>`), UAC onayıyla.
- "Şu komutu çalıştır" tarzı genel bir ayrıcalıklı işlem yok.
- Dinleyen bir ayrıcalıklı süreç olmadığı için saldırılacak IPC de yok.

Ayrıcalıklı yolda alınan argümanlar tip ve içerik olarak doğrulanıyor;
PowerShell'e geçen değerler için enjeksiyon testleri var
(`internal/nrpt`, `internal/hostsfile`).

## Girdi doğrulama noktaları

Güvenilmez girdinin sisteme girdiği yerler ve karşılandığı denetim:

| Girdi | Nereden | Denetim |
|---|---|---|
| `devbox.yaml` | depodan (klonlanan proje) | bilinmeyen alan reddi; `root` ve konteyner bağlamaları proje dışına çıkamaz; cron ifadesi okunurken çözülür |
| Yakalanan posta | uygulamanın gönderdiği | HTML gövde ayrı uç noktada, kendi katı CSP'siyle ve `sandbox` ile; ek dosyalar `application/octet-stream` |
| Kaydedilen istek | siteden geçen trafik | arayüzde asla HTML olarak basılmıyor |
| ACME istekleri | konteyner/istemci | JWS imzası, tek kullanımlık nonce, imzadaki `url` eşleşmesi; sertifika yalnız doğrulanmış adlara |
| Arşivler (runtime) | uzak indirme | SHA256 doğrulaması; `../` girdileri reddediliyor (Zip Slip) |
| Manifestler | uzak | ed25519 imzası; **imzalama anahtarı üretilmediği için uzak manifest tümden reddediliyor** |
| PHP istekleri | tarayıcı | `cgi.fix_pathinfo` hilesi kapalı; `Proxy` başlığı düşürülüyor (httpoxy); gizli dosyalar 403 |
| API istekleri | tarayıcı/CLI | jeton (sabit süreli karşılaştırma), `Host` denetimi (DNS yeniden bağlama), çerezle gelen durum değiştiren isteklerde `Origin` |

## Bağımlılık yüzeyi

Tek dış bağımlılık: `gopkg.in/yaml.v3`. Bu, tedarik zinciri yüzeyini
neredeyse tümden kapatıyor — ve o yüzden bu depoda bir bağımlılık eklemek
her seferinde ayrıca gerekçelendiriliyor.

Ayrı modüllerde (ürüne girmeyen) iki test bağımlılığı var:
`tests/acme-client` (lego) ve `editors/vscode` (TypeScript araçları).

## Bağımsız denetim için başlangıç noktaları

Denetim yaptıracak kişiye öneri — en yüksek değerli üç yer:

1. **`internal/acme`** — JWS doğrulaması elle yazıldı. Özellikle imza
   doğrulamasının atlanabildiği bir yol, `kid`/`jwk` karışıklığı, nonce
   yeniden kullanımı.
2. **Ayrıcalıklı yol** (`internal/elevate`, `internal/nrpt`,
   `internal/hostsfile`) — argümanların PowerShell'e ulaşana kadarki
   yolculuğu. **Gerçek Windows'ta hiç çalıştırılmadı.**
3. **`internal/web`** — CGI değişkenlerinin kurulumu ve yol
   çözümlemesi; belge kökünün dışına çıkan bir istek.

## En büyük açık risk

Kod yolu değil, doğrulama boşluğu: **bu yığın gerçek bir Windows
makinesinde hiç çalıştırılmadı.** NRPT, Firefox NSS, UAC yükseltmesi ve
kök sertifika onay penceresi CI'nin kapsayamadığı yollar. Güvenlik
açısından en riskli kod (ayrıcalık yükseltme) tam olarak orada.
