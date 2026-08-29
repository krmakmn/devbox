# DevBox — VS Code eklentisi

DevBox projesini VS Code içinden yönetir: durum çubuğunda projenin
durumu, tek komutla başlat/durdur, günlük, posta kutusu ve Xdebug
anahtarı.

## Ne yapıyor

| Komut | Ne yapar |
|---|---|
| `DevBox: Projeyi başlat` | Açık klasöre karşılık gelen projeyi çekirdek süreç üzerinden başlatır |
| `DevBox: Projeyi durdur` | Durdurur |
| `DevBox: Siteyi tarayıcıda aç` | `https://<alan-adı>` |
| `DevBox: Posta kutusunu aç` | `https://mail.<alan-adı>` |
| `DevBox: Denetim panelini aç` | `devbox ui` ile aynı panel |
| `DevBox: Günlüğü göster` | Projenin günlüğünü çıktı penceresinde |
| `DevBox: Xdebug'ı aç/kapat` | `devbox.yaml`'daki `php.xdebug` satırını çevirir |

Durum çubuğu, çekirdek süreç kapalıyken de bir şey söyler; sessiz
kalmak, kullanıcının eklentinin çalışıp çalışmadığını anlayamaması
demek.

## Kurulum ve geliştirme

```bash
npm install
npm run compile
npm test                       # yalnız birim testleri
DEVBOX_BIN=/yol/devbox npm test  # gerçek bir devboxd'ye karşı da koşar
```

Eklentiyi denemek için VS Code'da bu klasörü açıp F5'e basın.

## Neyin doğrulandığı, neyin doğrulanmadığı

Bu depoda kural, yazılan her şeyin çalıştığının gösterilmesi. Burada
sınır şu:

- **Doğrulandı:** çekirdek süreçle konuşan katman (`src/daemon.ts`) ve
  `devbox.yaml` düzenleyicisi (`src/xdebug.ts`) düz node ile ve gerçek
  bir `devboxd`'ye karşı sınandı — proje listesi, dizin eşleştirme,
  panel adresi, hata yolları. Kod `tsc --strict` ile derleniyor.
- **Doğrulanmadı:** VS Code yapıştırması (`src/extension.ts`) — durum
  çubuğu, komut kaydı, bildirimler. Çalıştırmak VS Code gerektiriyor ve
  bu geliştirme ortamında yok. Mantığın tamamı bilerek `daemon.ts` ve
  `xdebug.ts` içine alındı ki doğrulanmayan kısım olabildiğince ince
  kalsın.

`.vsix` paketi henüz üretilmiyor; yayın altyapısı Faz 8'de.
