# Paketleme ve dağıtım

Bu dizin DevBox'ın Windows'ta nasıl dağıtılacağını tarif ediyor.

> **Doğrulama sınırı — önce bunu okuyun.** Buradaki hiçbir şey
> çalıştırılıp doğrulanmadı. Kod imzalama bir sertifika (yıllık ücretli,
> kimlik doğrulamalı), MSI üretimi WiX ve Windows, winget yayını ise
> gerçek bir GitHub sürümü gerektiriyor. Üçü de bu geliştirme ortamında
> yok. Dosyalar bu yüzden **taslak**: biçimleri ve akışları doğru olacak
> şekilde yazıldılar ama ilk gerçek çalıştırmada düzeltme gerektirmeleri
> beklenmeli.
>
> Bu depoda kural, yazılanın çalıştığının gösterilmesi. Burada
> gösteremiyoruz; onun yerine ne olduğunu açıkça yazıyoruz.

## Neden MSI ve winget

- **MSI**: kurumsal makinelerde grup ilkesiyle dağıtılabilen tek biçim.
  Laragon'un kurulum deneyimindeki en büyük eksik bu.
- **winget**: `winget install DevBox.DevBox` — Windows 11'de kutudan
  çıkan paket yöneticisi. Güncelleme yolu da buradan geliyor.

Chocolatey ve Scoop bilinçli olarak dışarıda: winget artık varsayılan ve
üç ayrı paketi güncel tutmak, üçünün de eskimesi demek.

## Kod imzalama

İmzasız bir kurulum, SmartScreen'in "bilinmeyen yayıncı" uyarısını
gösteriyor ve kullanıcıların çoğu orada duruyor. İmzalama olmadan
dağıtım anlamsız.

Gerekenler:
- Bir kod imzalama sertifikası (EV olması SmartScreen itibarını
  hemen veriyor; OV ile itibar zamanla birikiyor).
- Sertifikanın **CI'da değil**, imza için ayrılmış bir makinede ya da
  bir imzalama hizmetinde tutulması. Depoya ya da CI gizli değişkenine
  özel anahtar konmaz.

`scripts/sign.ps1` bu akışı tarif ediyor.

## Delta güncelleyici

Yol haritasında vardı; **yapılmadı**. Gerekçe: winget zaten güncelleme
yolunu sağlıyor ve kendi güncelleyicisini yazmak, imzalı manifest
altyapısının (Faz 1'de ertelenen) üzerine kurulmayı gerektiriyor. Kendi
güncelleyicisini yazan bir araç, kendi başına bir saldırı yüzeyi
demek — o yüzeyi açmadan önce winget'in yetip yetmediğini görmek daha
doğru.

## Çökme raporlama

Yol haritasında vardı; **yapılmadı**. Kullanıcı onaylı çökme raporlaması
bir sunucu, bir saklama ilkesi ve bir gizlilik metni gerektiriyor —
DevBox'ın yerel bir araç olma niteliğini değiştiren bir adım. Bugünkü
karşılığı: `devbox logs` ve denetim panelindeki günlük görüntüleyici.
