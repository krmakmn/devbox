# Başlangıç

Sıfırdan çalışan bir siteye kadar. Her adım ne işe yaradığını söylüyor;
atlanabilir olanlar işaretli.

> Bu belgedeki komutlar Windows, Linux ve macOS'ta aynı. Yollar
> Windows'a göre yazıldı.

## 1. Kur

```powershell
winget install DevBox.DevBox
```

> **Not:** winget paketi henüz yayınlanmadı (bkz. `packaging/README.md`).
> Bugün için: sürüm ikilisini indirin ya da kaynaktan derleyin —
> `go build ./cmd/devbox`.

Kurulumun doğrulanması:

```powershell
devbox version
```

## 2. Kök sertifikayı güven depolarına kur — bir kez

```powershell
devbox trust install
```

Windows bir onay penceresi gösteriyor; "Evet" demeniz gerekiyor. Bu
adım olmadan tarayıcı her sitede sertifika uyarısı verir.

Firefox kendi sertifika deposunu taşıdığı için ayrıca ona da kuruluyor;
komut bunu kendiliğinden yapıyor.

## 3. `*.test` çözümlemesini aç — bir kez

```powershell
devbox dns install
```

Bu, Windows'un Ad Çözümleme İlkesi Tablosu'na (NRPT) bir kural ekliyor:
`.test` ile biten her ad DevBox'ın çözücüsüne gidiyor. Yönetici hakkı
istiyor ve UAC onayı çıkıyor.

NRPT kullanılamıyorsa DevBox `hosts` dosyasına düşüyor; o durumda her
yeni alan adı için komutu tekrar çalıştırmak gerekiyor (hosts joker
desteklemiyor).

## 4. Bir proje aç

Yeni proje:

```powershell
devbox new laravel magaza
cd magaza
devbox up
```

Var olan bir proje:

```powershell
cd C:\kod\magaza
devbox init      # devbox.yaml üretir
devbox up
```

Laragon ya da XAMPP'tan geliyorsanız hepsini birden:

```powershell
devbox migrate           # ne bulduğunu gösterir, hiçbir şey değiştirmez
devbox migrate -apply    # devbox.yaml'ları yazar
```

Tarayıcıda `https://magaza.test` — uyarısız.

## 5. Ne var elinizin altında

`devbox up` çalışırken:

| Adres | Ne |
|---|---|
| `https://magaza.test` | siteniz |
| `https://mail.magaza.test` | giden postalar (dışarı çıkmıyor) |
| `https://inspect.magaza.test` | geçen istek/yanıtlar, tekrar gönderme |

Posta kutusu ve denetleyici yalnız bu makineden açılabiliyor.

## Günlük kullanım

```powershell
devbox ui              # denetim paneli: projeler, başlat/durdur, günlük
devbox ps              # servislerin durumu
devbox logs -f web     # bir servisin günlüğü
devbox db create magaza -engine postgres
devbox db snapshot magaza gecis-oncesi
devbox share           # geçici genel adres (webhook denemesi için)
devbox lock write      # çalışan sürümleri kaydet (depoya ekleyin)
```

## Yönetici hakkı ne zaman gerekiyor

Yalnız üç işlem, hepsi tek seferlik:

- `devbox trust install` — kök sertifika
- `devbox dns install` — NRPT kuralı
- `devbox dns uninstall`

Günlük kullanım (`devbox up`, `devbox db`, `devbox ui`) yönetici hakkı
istemiyor. Windows'ta 1024 altı portlar ayrıcalıklı olmadığı için kenar
vekili 80 ve 443'ü normal kullanıcı olarak açabiliyor.

## Sık karşılaşılanlar

**Tarayıcı hâlâ uyarı veriyor.** Tarayıcıyı tümden kapatıp açın;
sertifika deposu süreç başlarken okunuyor. Firefox için:
`devbox trust status` kurulumun her iki depoda da göründüğünü söylüyor.

**Alan adı çözülmüyor.** `devbox dns status`. Kurumsal makinelerde
NRPT'yi grup ilkesi engelleyebiliyor; o durumda hosts geri düşüşü
devrede ve her alan adı elle eklenmeli.

**Port 80 dolu.** Genellikle IIS ya da Skype. `devbox up` hangi sürecin
tuttuğunu söylüyor.

**Proje panelde görünmüyor.** Kayıt ayrı: proje dizininde
`devbox project add`.

## Daha fazlası

- [Yol haritası](yol-haritasi.md) — ne yapıldı, ne yapılmadı, neden
- [Güvenlik](guvenlik.md) — saldırı yüzeyi ve kararların gerekçesi
- [Performans](performans.md) — ölçümler
