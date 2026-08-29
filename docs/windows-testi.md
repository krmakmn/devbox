# Windows'ta çalıştırma ve deneme kılavuzu

Bu belge, DevBox'ı gerçek bir Windows makinesinde baştan sona çalıştırıp
denemek içindir. Amaç yalnız "kurup bakmak" değil: bu deponun en büyük
açık riski, yığının bir insanın oturduğu Windows masaüstünde hiç
çalıştırılmamış olması. Aşağıdaki adımlar o boşluğu kapatacak biçimde
sıralandı ve her adımda **ne görmeniz gerektiği** yazılı.

Sonunda doldurup geri bildirebileceğiniz bir kontrol listesi var.

---

## 0. Neyin zaten denendiği, neyin size kaldığı

Sürekli tümleştirme (CI) `windows-latest` koşucusunda gerçek Windows
Server üzerinde şunları koşuyor
(`.github/workflows/windows-smoke.yml`):

| Denenen | Durum |
|---|---|
| Kök sertifikanın makine güven deposuna kurulması | ✅ parmak iziyle doğrulandı |
| Gerçek NRPT kuralı (`Add-DnsClientNrptRule`) | ✅ eklendi ve doğrulandı |
| Çözücünün `127.0.0.53:53`'e bağlanması | ✅ |
| `Resolve-DnsName magaza.test` (NRPT üzerinden) | ✅ `A 127.0.0.1`, `AAAA ::1` |
| `https://magaza.test` — portsuz, `-k` olmadan | ✅ MVP kabul kriteri |
| Kök kaldırılınca aynı isteğin reddedilmesi | ✅ olumsuz kontrol |
| HTTP → HTTPS yönlendirmesi | ✅ |
| Posta kutusu, denetleyici, SMTP yakalayıcı | ✅ |
| Port alınamadığında doğru davranış | ✅ "hazır" denmiyor, teşhis veriliyor |
| Kurulumun geri alınması (kural + sertifika) | ✅ |
| Birim testleri (yarış dedektörüyle) | ✅ |

### Gerçek bir masaüstünde doğrulananlar

Aşağıdakiler bir insanın Windows makinesinde, elle çalıştırılarak
doğrulandı. CI'nin hiç göremediği şeyler bunlar:

| Denenen | Sonuç |
|---|---|
| Kaynaktan derleme (Go 1.27, windows/amd64) | ✅ |
| Kök sertifika **onay penceresi** gerçekten açılıyor | ✅ |
| Sertifikanın `Cert:\CurrentUser\Root`'ta olması | ✅ parmak iziyle |
| `devbox dns install` + üç ayrı çözümleme denetimi | ✅ |
| Windows'un PHP'yi bulup havuzu kurması (16 işçi) | ✅ |
| **Tarayıcıda `https://magaza.test` — SSL uyarısı yok** | ✅ MVP kabul kriteri |
| **İki site aynı anda, paylaşılan kenar üzerinden** | ✅ |
| Birini durdurunca diğerinin çalışmaya devam etmesi | ✅ |
| **İş Nesneleri:** zorla öldürmede 19 `php-cgi.exe`'nin hepsi öldü | ✅ |
| Çekirdek süreç + panelden proje başlatma | ✅ |
| SMTP yakalayıcı: gönderilen posta kutuya düştü | ✅ |
| **Ağdan erişim ayrımı:** site 200, denetleyici 403 | ✅ |
| 443'ü tek süreç dinliyor (`::`, çift yığın) | ✅ |

Bu masaüstünde 80 ve 443 boştu; rezerve port yoluna girilmedi.

İş Nesneleri sınavı özellikle önemli: `internal/proc`'un Windows'a özgü
`syscall`/`kernel32` kodu bugüne kadar yalnız Linux'ta sınanmıştı. Üç
`devbox` süreci (çekirdek + iki proje) zorla öldürüldü ve 19 çocuk
sürecin hepsi onlarla birlikte gitti. Laragon'un en bilinen kusuru tam
buydu: `php-cgi.exe` arkada birikir ve sonraki başlatmada port
çakışması yapar.

Hâlâ **denenmemiş** olanlar: Firefox'un NSS deposu (o makinede Firefox
kurulu değildi), üç tarayıcının hepsi, denetleyici arayüzünde "tekrar
gönder", gerçek Apache/Nginx, veritabanları ve Laragon'dan göç.

Ağdan erişim ayrımı sınavı özellikle önemli: paylaşılan kenar araya
girince proje sürecinin gördüğü uzak adres her zaman 127.0.0.1 oluyor,
yani kısıtı kenarın uygulaması gerekiyor. Gerçek bir LAN adresi
(192.168.x.x) üzerinden doğrulandı — site 200, denetleyici 403.

**Bilinen eksik:** kenar ortaklaştırıldı ama SMTP portu (1025)
ortaklaştırılmadı. İki proje birden çalışırken portu ilk başlayan alır,
ikincisi uyarı verip devam eder — sessizce yanlış davranmıyor ama
ikinci projenin postaları yakalanmıyor. Düzgün çözüm tek bir posta
yakalayıcının çekirdekte durması.

### CI'nin yapamadıkları

Bunlar bir insanın oturduğu makineyi gerektiriyor:

- **UAC onay penceresi.** Koşucu zaten yükseltilmiş çalışıyor; yükseltme
  yolunun (`ShellExecuteExW "runas"`) kendisi hiç tetiklenmiyor.
- **Kök sertifika onay penceresi.** Masaüstü oturumu yok; CI `-machine`
  kullanıyor. Öntanımlı (kullanıcı deposu) yolu bir insanın "Evet"
  demesini gerektiriyor.
- **Tarayıcılar.** CI `curl.exe` kullanıyor — zincir doğrulaması gerçek
  ama Chrome, Edge ve Firefox'un kendi davranışı denenmiyor.
- **Firefox'un NSS deposu.** Koşucuda Firefox yok.
- **Windows'un gerçek PHP, Apache ve Nginx derlemeleri.**
- **İş Nesneleri (Job Objects) baskı altında** — DevBox zorla
  öldürüldüğünde çocukların gerçekten ölmesi.

---

## 1. Gerekenler

- Windows 10 (21H2+) ya da Windows 11
- **PowerShell.** Bu belgedeki komutların tamamı PowerShell içindir,
  Komut İstemi (cmd.exe) değil. İsteminiz `PS D:\devbox>` gibi
  görünmeli; `D:\devbox>` görüyorsanız cmd'desiniz ve `$env:PATH`,
  `Set-Content`, `Get-NetTCPConnection` gibi komutlar çalışmaz.
  Başlat → "PowerShell".
- **Komutları tek tek çalıştırın.** Kod blokları birden çok satır
  içeriyor; hepsini tek satıra yapıştırırsanız kabuk onları tek bir
  komutun argümanları sayar. Örneğin üç satırlık derleme bloğu tek
  satırda `malformed import path "$env:PATH"` verir.
- **Go 1.23+** — kaynaktan derlemek için: https://go.dev/dl/
- İsteğe bağlı ama önerilen: Chrome, Edge **ve** Firefox (üçünün de
  denenmesi gerekiyor)
- İsteğe bağlı: PHP for Windows, Apache (Apache Lounge), Nginx

Kurulu bir Laragon ya da XAMPP varsa **kapatın** — 80 ve 443 portlarını
tutuyorlar. Kaldırmanıza gerek yok; göç adımında onlara ihtiyacınız
olacak.

### 1.1 Ön kontrol: 80 ve 443 gerçekten boş mu?

Bunu baştan yapın. Windows'ta 80 numaralı portu bir program tutmuyor
olsa bile **işletim sistemi rezerve etmiş** olabilir; Hyper-V, WSL2 ya
da Docker Desktop açıksa bu çok sık oluyor. CI koşucusunda da tam olarak
bu çıktı.

```powershell
# Rezerve aralıklar (yönetici gerekmez)
netsh int ipv4 show excludedportrange protocol=tcp

# Kim dinliyor?
Get-NetTCPConnection -State Listen -LocalPort 80,443 -ErrorAction SilentlyContinue |
  Select-Object LocalAddress, LocalPort, OwningProcess
```

Çıktıda `80` bir aralığın içinde görünüyorsa ya da `OwningProcess 4`
(System = `http.sys`, genelde IIS ya da WinNAT) yazıyorsa, DevBox 80'i
alamaz. Üç seçeneğiniz var:

1. Sebebi kaldırın: IIS'i (`W3SVC`) durdurun, Docker Desktop'ı kapatın.
2. Rezervasyonu serbest bırakın (yönetici): `net stop winnat`, sonra
   DevBox'ı başlatın, işiniz bitince `net start winnat`.
3. Başka bir port kullanın: `devbox up -http :8080`. **443 boşsa MVP
   deneyimi bozulmaz** — `https://magaza.test` yine portsuz açılır,
   yalnız düz HTTP yönlendirmesi 8080'den olur.

DevBox portu alamazsa proje **hazır ilan edilmez** ve size hangi
aralığın rezerve olduğunu söyleyen bir hata verir. Bu davranış, bu
kılavuz doğrulanırken bulunan bir kusurun düzeltilmesiyle geldi:
eskiden önce "hazır" yazıyor, sonra anlamsız bir soket hatası
veriyordu.

---

## 2. Derle

Depoyu klonlayın ve derleyin:

```powershell
git clone https://github.com/krmakmn/devbox.git
cd devbox
git checkout claude/laragon-dev-environment-upgrade-09mip4
go build -o devbox.exe .\cmd\devbox
.\devbox.exe version
```

> Dal adı geçici: bu kılavuzdaki Windows düzeltmeleri henüz `main`'e
> alınmadı. `main` üzerinde denerseniz burada anlatılan davranışların
> bir kısmını göremezsiniz.

Beklenen çıktı:

```
devbox 0.1.0-gelistirme (windows/amd64, go1.2x.x)
```

PATH'e ekleyin. **Kalıcı olanı seçin** — aksi hâlde her yeni PowerShell
penceresinde `devbox` bulunamaz ve bu kılavuz birden çok pencere
gerektiriyor:

```powershell
[Environment]::SetEnvironmentVariable('PATH', "$PWD;" + [Environment]::GetEnvironmentVariable('PATH','User'), 'User')
```

Bu, **sonra açtığınız** pencerelerde geçerli olur. Şu anki pencere için
ayrıca:

```powershell
$env:PATH = "$PWD;$env:PATH"
```

> Yalnız geçici olanı çalıştırırsanız, başka bir pencerede
> `devbox : The term 'devbox' is not recognized…` hatası alırsınız.
> `$PWD` de o anki dizini gösterdiği için, komutu depo dizini dışında
> çalıştırmak işe yaramaz.

---

## 3. İlk kurulum — iki komut, iki onay penceresi

### 3.1 Kök sertifika

```powershell
devbox trust install
```

**Ne görmelisiniz:**

1. Bir **Windows Güvenlik Uyarısı** penceresi açılıyor: "Sertifika
   deposuna sertifika eklemek üzeresiniz…" — **Evet** deyin.
2. Firefox kuruluysa onun profillerine de ekleniyor (ayrı pencere
   çıkmaz).
3. Çıktıda hedeflerin listesi:

```
✓ Windows güven deposu (Chrome, Edge) — kullanıcı
✓ Firefox profili: <profil adı>

2 hedefe kuruldu. Açık tarayıcıları yeniden başlatın.
```

**Denemesi gereken:**
- [ ] Onay penceresi **gerçekten açılıyor mu?** (CI bunu göremiyor)
- [ ] "Hayır" derseniz komut donmadan makul bir hata veriyor mu?
- [ ] Firefox kuruluysa profil bulundu mu?

Doğrulama:

```powershell
devbox trust status
Get-ChildItem Cert:\CurrentUser\Root | Where-Object { $_.Subject -like "*DevBox*" }
```

> **Yönetici olarak kuruyorsanız:** `devbox trust install -machine`
> makine geneli depoya kurar; onay penceresi çıkmaz ama yönetici hakkı
> ister. Otomatik kurulum ve uzak oturum için olan yol budur.

### 3.2 Alan adı çözümlemesi

Çözücüyü ayrı bir pencerede başlatın:

```powershell
devbox dns serve
```

Başka bir pencerede kuralı ekleyin:

```powershell
devbox dns install
```

**Ne görmelisiniz:**

1. **UAC penceresi** açılıyor ("Bu uygulamanın cihazınızda değişiklik
   yapmasına izin veriyor musunuz?") — **Evet** deyin.
2. Çıktı: `NRPT kuralı eklendi: .test → 127.0.0.53`

**Denemesi gereken:**
- [ ] UAC penceresi açılıyor mu? (CI'da hiç tetiklenmiyor)
- [ ] "Hayır" derseniz komut açık bir hata veriyor mu, donuyor mu?

Doğrulama — **üç ayrı kontrol**, çünkü CI'da tam burası düştü:

```powershell
# 1) Kural NRPT'de mi?
Get-DnsClientNrptRule | Where-Object { $_.Namespace -contains '.test' } | Format-List

# 2) Çözücümüz doğrudan sorulduğunda yanıt veriyor mu?
Resolve-DnsName -Name magaza.test -Server 127.0.0.53

# 3) Windows'un kendi çözücüsü NRPT'yi uyguluyor mu?
Resolve-DnsName -Name magaza.test
nslookup magaza.test
```

Beklenen çıktı (3) için:

```
Name        Type TTL Section IPAddress
----        ---- --- ------- ---------
magaza.test AAAA 10  Answer  ::1
magaza.test A    10  Answer  127.0.0.1
```

AAAA'nın önce gelmesi normal; `::1` doğru cevap.

> Üçü de CI'da geçiyor. Sizde (3) çalışmazsa (1) ve (2)'nin çıktısını
> bildirin — sorunun çözücüde mi yoksa NRPT'nin uygulanmasında mı
> olduğunu ayırt eden şey tam olarak bu. **Çözücüyü açık bırakmayı
> unutmayın:** `devbox dns serve` penceresini kapatırsanız NRPT kuralı
> yerinde kalır ama soracak kimse olmaz.
>
> Geçici çözüm: `devbox dns install -hosts` hosts dosyasına yazar
> (joker desteklemez, her alan adını tek tek ekler).

---

## 4. İlk proje

```powershell
mkdir C:\kod\magaza
cd C:\kod\magaza
"<h1>Merhaba DevBox</h1>" | Set-Content index.html -Encoding utf8

devbox init
devbox up
```

**Ne görmelisiniz** (PHP kurulu değilse):

```
  magaza hazır: https://magaza.test

  sunucu    : devbox
  php       : yok — yalnız statik dosyalar
              kurmak için: devbox runtime install php@8.3
  çözücü    : 127.0.0.53:53
  posta     : smtp 127.0.0.1:1025, kutu https://mail.magaza.test
  denetleyici: https://inspect.magaza.test

  Ctrl+C ile durdurun.
```

> Statik bir klasörü sunmak için PHP gerekmiyor. Bu, bu kılavuz
> yazılırken bulunan bir kusurdu: DevBox `php-cgi bulunamadı` deyip
> duruyordu ve içinde yalnız `index.html` olan bir klasör açılamıyordu.
> Ayrıca dizin isteğinde `index.html` hiç aranmıyordu.

### 4.1 Asıl sınav — üç tarayıcıda uyarısız HTTPS

Sırayla açın:

- [ ] **Chrome** → `https://magaza.test` — kilit simgesi, uyarı yok
- [ ] **Edge** → `https://magaza.test` — uyarı yok
- [ ] **Firefox** → `https://magaza.test` — uyarı yok *(Firefox ayrı bir
      sertifika deposu kullanır; bu adım NSS kurulumunu sınıyor)*

Uyarı çıkarsa tarayıcıyı tamamen kapatıp açın (sertifika deposu süreç
başlarken okunuyor), sonra tekrar deneyin.

Komut satırından da doğrulanabilir — `-k` **olmadan**:

```powershell
curl.exe --ssl-revoke-best-effort https://magaza.test/
```

`curl.exe` Windows'ta Schannel kullandığı için bu, işletim sisteminin
güven zincirini sınar.

**`--ssl-revoke-best-effort` neden gerekiyor?** Bayrak olmadan curl şunu
verir:

```
curl: (35) schannel: CRYPT_E_NO_REVOCATION_CHECK — The revocation
function was unable to check revocation for the certificate.
```

Bu bir güven hatası değil: curl'ün Schannel arka ucu sertifika için iptal
listesi (CRL/OCSP) arıyor, yerel geliştirme sertifikasında öyle bir şey
yok. Olmayacak da — CRL yayımlamak yerel bir araç için gerçek sunucu
altyapısı demek; mkcert de koymuyor. Windows, yerel olarak kurulmuş bir
köke zincirlenen sertifikada iptal denetimini zaten zorunlu tutmuyor;
katı davranan curl'ün kendisi. **Tarayıcılar bu hatayı vermez** — Chrome,
Edge ve Firefox'ta ekstra bir şey yapmanız gerekmez.

Bayrak yalnız iptal aramasını kapatıyor, zincir doğrulamasını değil.
Duman testi bunu olumsuz kontrolle kanıtlıyor: kök güven deposundan
çıkarılınca aynı istek reddediliyor, geri kurulunca yeniden geçiyor.
Yani `-k` ile aynı şey değil — `-k` doğrulamayı tümden kapatır.

### 4.1.1 Günlük kullanım — her proje için terminal gerekmez

Yukarıdaki `devbox up` akışı **tek projeyi elle denemek** içindir. Gerçek
kullanımda projeleri çekirdek süreç yönetir ve hepsi aynı anda çalışır:

```powershell
# Her proje için bir kez
cd C:\kod\magaza
devbox init
devbox project add

# Bilgisayar başına bir kez, arka planda
devbox daemon

# Paneli aç, projeleri listeden başlat/durdur
devbox ui
```

Çekirdek süreç 80 ve 443'ü **tek başına** dinler ve istekleri alan adına
göre projelere dağıtır; çözücüyü de o çalıştırır. Her proje kendi
işleyicisini yalnız geri döngüde açar.

Bu, bir mimari düzeltmenin sonucu: önceden her proje kendi kenarını
80/443'te açıyordu, dolayısıyla **aynı anda tek proje** çalışabiliyordu —
ikincisi portu alamıyordu. Laragon'un yerine geçmek isteyen bir araçta
en temel eksik buydu.

- [ ] İki projeyi panelden aynı anda başlatın; ikisi de kendi alan
      adından açılıyor mu?
- [ ] Birini durdurun; onun alan adı 404 verirken diğeri çalışmaya devam
      ediyor mu?

### 4.2 Yan yüzeyler

- [ ] `https://mail.magaza.test` açılıyor mu?
- [ ] `https://inspect.magaza.test` açılıyor, az önceki istekleri
      gösteriyor mu?
- [ ] Denetleyicide bir isteğe tıklayıp **Tekrar gönder** çalışıyor mu?

### 4.3 Ağdan erişim ayrımı

Aynı ağdaki başka bir cihazdan (telefon):

- [ ] `https://magaza.test` — **çalışmalı** (telefonun bu adı çözmesi
      için hosts kaydı ya da yönlendirici ayarı gerekir; pratikte
      makinenizin IP'siyle `https://<ip>` deneyin)
- [ ] `https://inspect.magaza.test` — **403 vermeli.** Denetleyici ve
      posta kutusu yalnız makinenin kendisinden açılabiliyor.

---

## 5. Süreç yönetimi — Job Objects

DevBox'ın en Windows'a özgü parçası bu: çocuk süreçler bir İş Nesnesine
bağlı ve DevBox ölünce onlar da ölmeli. Laragon'un en can sıkıcı
davranışı tam olarak burada başarısız olması (php-cgi.exe arkada kalır).

`devbox up` çalışırken, ayrı bir pencerede:

```powershell
Get-Process devbox, php-cgi -ErrorAction SilentlyContinue | Format-Table Id, ProcessName
```

Sonra DevBox'ı **zorla** öldürün (Ctrl+C değil):

```powershell
Stop-Process -Name devbox -Force
Start-Sleep -Seconds 2
Get-Process php-cgi, httpd, nginx -ErrorAction SilentlyContinue
```

- [ ] Zorla öldürmeden sonra **hiçbir çocuk süreç kalmıyor** mu?
- [ ] Görev Yöneticisi'nden öldürüldüğünde de aynı mı?

Bu testin geçmesi, `internal/proc`'un iş nesnesi kodunun gerçekten
çalıştığı anlamına gelir — bugüne kadar yalnız Linux'ta sınandı.

---

## 6. PHP

PHP for Windows'u indirin (https://windows.php.net/download/ — **Non
Thread Safe** sürüm) ve bir dizine açın, örneğin `C:\php`.

```powershell
cd C:\kod\magaza
"<?php phpinfo();" | Set-Content index.php -Encoding utf8
devbox up -php C:\php\php-cgi.exe
```

- [ ] `https://magaza.test` phpinfo çıktısını gösteriyor mu?
- [ ] `devbox ps` php işçilerini listeliyor mu?
- [ ] Sayfayı arka arkaya 20 kez yenileyin — hata ya da takılma var mı?

Laravel denemesi (Composer kuruluysa):

```powershell
cd C:\kod
devbox new laravel magaza2
cd magaza2
devbox up -php C:\php\php-cgi.exe
```

- [ ] Laravel karşılama sayfası açılıyor mu?

---

## 7. Apache ve Nginx

DevBox yapılandırmayı üretiyor ve sunucuyu kendisi yönetiyor. Windows
derlemeleri:

- Apache: https://www.apachelounge.com/download/
- Nginx: https://nginx.org/en/download.html

```powershell
# devbox.yaml içinde: server: nginx
devbox up -server-bin C:\nginx\nginx.exe -php C:\php\php-cgi.exe
```

- [ ] Nginx ayağa kalkıyor ve site açılıyor mu?
- [ ] `server: apache` ile `httpd.exe` aynı şekilde çalışıyor mu?
- [ ] Üretilen yapılandırma dosyası (`devbox up` çıktısında yolu var)
      Windows yollarını doğru yazmış mı?

Bu adım, `internal/webserver`'ın altın dosya testlerinin **gerçek
sunucularca kabul edilip edilmediğini** Windows'ta sınıyor. (Linux'ta
zaten sınanıyor.)

---

## 8. Veritabanı

PostgreSQL ya da MariaDB'nin Windows derlemesi kuruluysa:

```powershell
devbox db create magaza -engine postgres
devbox db list
devbox db snapshot magaza -tag ilk
devbox db restore magaza -tag ilk
```

- [ ] Örnek açılıyor ve bağlantı kabul ediyor mu?
- [ ] Anlık görüntü ve geri yükleme çalışıyor mu?
- [ ] `devbox db` ile açılan süreçler DevBox kapanınca kapanıyor mu?

---

## 9. Port çakışmaları

DevBox, Hyper-V'nin ayırdığı port aralıklarını tanımalı. Docker Desktop
ya da WSL2 kuruluysa bu aralıklar doludur:

```powershell
netsh int ipv4 show excludedportrange protocol=tcp
```

- [ ] `devbox up` bu aralıklardaki bir portu seçmeye çalışıyor mu?
- [ ] IIS çalışıyorken `devbox up` **hangi sürecin 80'i tuttuğunu**
      söylüyor mu?

---

## 10. Göç

Laragon ya da XAMPP kuruluysa:

```powershell
devbox migrate                 # ne bulduğunu gösterir, hiçbir şey değiştirmez
devbox migrate -apply
devbox project list
```

- [ ] Siteleriniz doğru bulundu mu?
- [ ] Belge kökleri (`public/`) doğru algılandı mı?
- [ ] XAMPP'ın sanal konak adları takma ad olarak korundu mu?
- [ ] Laragon/XAMPP kurulumunuza **dokunulmadı** mı?

---

## 11. Denetim paneli

```powershell
devbox daemon      # ayrı bir pencerede
devbox ui
```

- [ ] Panel tarayıcıda açılıyor ve adres çubuğunda jeton **görünmüyor**
      mu?
- [ ] Projeler listeleniyor, tek tıkla başlayıp duruyor mu?
- [ ] Günlükler canlı akıyor mu?

---

## 12. Temizlik

Denemeyi bitirdiğinizde makineyi eski hâline döndürmek için:

```powershell
devbox dns uninstall      # NRPT kuralı (UAC ister)
devbox trust uninstall    # kök sertifika
Remove-Item -Recurse -Force $env:LOCALAPPDATA\DevBox
```

Firefox'tan kaldırmak için komutun yazdığı `certutil` satırını
çalıştırın.

- [ ] `Get-DnsClientNrptRule` çıktısında `.test` kuralı kalmadı mı?
- [ ] `Get-ChildItem Cert:\CurrentUser\Root` içinde DevBox kökü kalmadı
      mı?

---

## Geri bildirim için kontrol listesi

Aşağıdakini kopyalayıp doldurun; her satır bir doğrulama boşluğunu
kapatıyor.

```
Windows sürümü      :
DevBox sürümü       :

[ ] trust install — onay penceresi açıldı ve kurulum tamamlandı
[ ] dns install — UAC penceresi açıldı ve kural eklendi
[ ] Resolve-DnsName magaza.test çalıştı
[ ] Chrome    — https://magaza.test uyarısız
[ ] Edge      — https://magaza.test uyarısız
[ ] Firefox   — https://magaza.test uyarısız
[ ] mail.magaza.test açıldı
[ ] inspect.magaza.test açıldı, tekrar gönderme çalıştı
[ ] inspect ağdan 403 verdi
[ ] Zorla öldürmede çocuk süreç kalmadı
[ ] PHP çalıştı (sürüm:        )
[ ] Nginx çalıştı (sürüm:      )
[ ] Apache çalıştı (sürüm:     )
[ ] Veritabanı çalıştı (motor: )
[ ] Göç doğru siteleri buldu
[ ] Denetim paneli çalıştı
[ ] Temizlik makineyi eski hâline döndürdü

Karşılaşılan sorunlar:
```

Sorun bildirirken şunlar en çok işe yarıyor: komutun tam çıktısı,
`devbox dns status` ve `devbox trust status` çıktıları, ve varsa
`%LOCALAPPDATA%\DevBox` altındaki günlükler.
