# Performans ölçümleri

Bu belge, DevBox'ın maliyetini sayılarla veriyor. Amaç övünmek değil:
her sayı bir kararın bedelini gösteriyor ve bir sonraki değişiklik onu
kötüleştirirse fark edilebilsin diye burada duruyor.

> **Ölçümlerin alındığı ortam:** Linux x86-64 konteyner, 4 çekirdek, Go
> 1.24. **Windows'ta hiçbiri ölçülmedi** — projenin asıl hedefi Windows
> olduğu için bu, sayıların en önemli sınırı. Windows'ta TLS, süreç
> başlatma ve dosya sistemi maliyetleri farklıdır; bu tablo oradaki
> davranışı tahmin etmek için kullanılamaz.

## Soğuk başlatma

`devbox up` çağrısından "hazır" satırının yazılmasına kadar geçen süre.
Proje `server: proxy`, ilk çalıştırmada kök CA ve site sertifikaları da
üretiliyor.

| Ölçüm | Süre |
|---|---|
| Soğuk başlatma (yeni veri dizini, CA üretimi dahil) | **33 ms** |
| Üç ardışık ölçümde sapma | yok (33 / 33 / 33 ms) |

PHP havuzu ya da veritabanı içeren projeler bunun üstüne kendi
başlatma sürelerini ekliyor; onlar DevBox'ın değil, motorların
maliyeti.

## İstek gecikmesi

Aynı makinede çalışan bir Go arka ucuna, kalıcı bağlantı üzerinden
1000 istek, 10 eşzamanlı.

| Yol | İstek/s | Ortanca | p95 |
|---|---|---|---|
| Doğrudan arka uç (kenar yok) | 3616 | 2,14 ms | 6,01 ms |
| Kenar üzerinden, denetleyici kapalı | 3082 | 2,50 ms | 6,86 ms |
| Kenar üzerinden, denetleyici açık | 2682 | 3,07 ms | 8,08 ms |

Okunuşu:

- **Kenarın kendi maliyeti ~360 µs** (TLS sonlandırma + konak
  yönlendirme + ters vekil). Bir PHP isteğinin milisaniyelerle ölçülen
  süresinin yanında görünmez.
- **Denetleyicinin maliyeti ~570 µs** ve bu, "öntanımlı açık"
  kararının bedeli. Karşılığında her isteğin gövdesi ve başlıkları
  elde oluyor. Kapatmak isteyen `inspect: disabled: true` yazıyor.
- Ölçüm Python istemcisiyle alındı; istemcinin kendi maliyeti üç
  satırda da var. Bu yüzden **mutlak sayılar değil, aradaki fark**
  anlamlı.

TLS el sıkışması ayrı ölçüldü: ilk bağlantıda **~4 ms**.

## Bellek

| Durum | RSS |
|---|---|
| `devbox up`, tek proje, denetleyici kapalı | 18,0 MB |
| `devbox up`, tek proje, denetleyici açık (boş kayıt) | 18,2 MB |

Denetleyicinin kayıt biriktikçe büyümesi sınırlı: en fazla 200 istek,
gövde başına en fazla 64 KB. En kötü durumda ~12 MB ekliyor.

## Mikro ölçümler

`go test ./... -bench .` ile koşuyor. Sayılar 4 çekirdekli bir Linux
konteynerinde alındı.

| Ölçüm | Süre | Ayırma |
|---|---|---|
| Kenar yönlendirme (10 projeli makine, tam eşleşme) | 1569 ns/op | 11 |
| Kenar yönlendirme (joker) | 283 ns/op | 4 |
| Denetleyici kapalı (ara katman geçişi) | 1466 ns/op | 10 |
| Denetleyici açık | 2004 ns/op | 20 |
| FastCGI kayıt yazma (1 KB) | 26,7 ns/op | 1 |

Kenar yönlendirme ölçümündeki ayırmaların çoğu `httptest` kayıt
nesnesinden geliyor; jokerli satır (gövde yazmadığı için) yönlendirmenin
kendi maliyetine daha yakın.

## Kapanış

Ctrl+C'den sürecin çıkmasına kadar:

| Durum | Süre |
|---|---|
| Proje + yan servis + konteyner | **1,4–1,9 s** |
| (Düzeltmeden önce) | 20,4 s |

20 saniyelik hâlin sebebi, durdurma sinyalinin yalnız başlatılan sürece
gönderilmesiydi; `sh -c`, `npm run dev` gibi sarmalayıcılarda torun
ayakta kalıyor ve boruları açık tuttuğu için `cmd.Wait()` dönmüyordu.
Sinyal artık süreç ağacına gidiyor.

## Ölçümleri yeniden almak

```bash
go test ./internal/edge/ ./internal/inspect/ ./internal/fastcgi/ \
  -run XXX -bench . -benchtime 200000x
```

Soğuk başlatma ve gecikme ölçümleri elle alındı; adımları bu belgenin
git geçmişinde duruyor.
