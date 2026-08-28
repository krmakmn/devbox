# DevBox

Windows için yerel geliştirme ortamı — Laragon'un kolaylığı, DDEV'in yeniden
üretilebilirliği, Herd'ün cilası.

> **Durum: planlama.** Henüz kod yok. Bu depo şimdilik mimariyi ve yol haritasını
> taşıyor. Başlangıç noktası: **[docs/yol-haritasi.md](docs/yol-haritasi.md)**

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

## Karar bekleyenler

Yol haritasının 11. bölümünde beş açık soru var: TLD tercihi, lisans modeli,
konteyner sürücüsünün kapsamı, desteklenecek en eski Windows sürümü ve GUI
çerçevesi.
