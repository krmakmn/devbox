# Windows duman testi

Bu dizin, `devbox`'ı **gerçek Windows'ta** uçtan uca çalıştıran CI işinin
parçalarını tutuyor.

## Neden var

Bu depodaki en büyük açık risk uzun süre şuydu: yığın gerçek bir Windows
makinesinde hiç çalıştırılmamıştı. Birim testleri Windows'ta koşuyordu ama
`devbox up`'ın kendisi, kök sertifika kurulumu, NRPT kuralı ve
`https://magaza.test` adresinin tarayıcı güveniyle açılması hiç
denenmemişti.

GitHub Actions'ın `windows-latest` koşucusu gerçek bir Windows Server ve
süreçler **yükseltilmiş yönetici belirteciyle** çalışıyor. Bu, o boşluğun
büyük kısmını kapatmayı mümkün kılıyor.

## Neyi kapsıyor

- `devbox trust install -machine` → kök sertifika makine geneli depoda
- `devbox dns install` → gerçek NRPT kuralı (`Add-DnsClientNrptRule`)
- Yerel çözücünün `.test` adlarını gerçekten çözmesi
- `devbox up` → kenar vekili, sertifika üretimi, süreç yönetimi
- `curl.exe` ile `https://magaza.test` — **`-k` olmadan**, yani Windows'un
  kendi güven zinciri üzerinden. MVP'nin kabul kriteri tam olarak bu.
- Posta kutusu ve HTTP denetleyicisinin ayakta olması
- Kapanışta süreçlerin ve NRPT kuralının temizlenmesi

## Neyi kapsamıyor

- **UAC onay penceresi.** Koşucu zaten yükseltilmiş; `devbox privileged`
  yolunun kendisi (ShellExecuteExW "runas") tetiklenmiyor. Yalnız
  sardığı işlemler sınanıyor.
- **Kök sertifika onay penceresi.** Masaüstü oturumu yok; bu yüzden
  `-machine` kullanılıyor. Kullanıcı deposu yolu hâlâ bir insanın
  "Evet" demesini gerektiriyor.
- **Tarayıcı.** `curl.exe` Schannel üzerinden Windows güven deposunu
  okuyor, yani zincir doğrulaması gerçek; ama Chrome/Edge/Firefox'un
  kendi davranışı denenmiyor.
- **Firefox NSS.** Koşucuda Firefox yok.
