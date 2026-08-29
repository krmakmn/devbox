# ACME uyumluluk denemesi

Bu dizin **ayrı bir Go modülü**. Sebebi: DevBox'ın tek dış bağımlılığı
`gopkg.in/yaml.v3` ve öyle kalmalı. Buradaki program bir ACME istemci
kütüphanesi (`lego`) kullanıyor — ama yalnız denemede, ürünün kendisinde
değil. Ayrı modül olduğu için `go test ./...` ve `go build ./...` bunu
görmüyor.

## Neden bir dış istemci

`internal/acme`'nin birim testleri, protokolü benim anladığım gibi
sınıyor. Yanlış anladığım bir yer varsa test de aynı yanlışı yapar.
Bağımsız yazılmış, Let's Encrypt'e karşı milyonlarca kez koşmuş bir
istemcinin akışı baştan sona tamamlaması, protokolü gerçekten doğru
uyguladığımızın kanıtı.

Aynı yaklaşım depoda başka yerlerde de var: FastCGI istemcisi stdlib'in
`net/http/fcgi` sunucusuna, SMTP yakalayıcı stdlib'in `net/smtp`
istemcisine karşı sınanıyor.

## Çalıştırma

```bash
go build -o /tmp/devbox ../../cmd/devbox
DEVBOX_HOME=/tmp/acme-deneme /tmp/devbox acme serve \
  -addr 127.0.0.1:14000 -map api.magaza.test=127.0.0.1:15080 &

go run . http://127.0.0.1:14000/acme/directory api.magaza.test 15080
```

Program hesap açar, sipariş oluşturur, http-01 meydan okumasını sunar,
sertifikayı alır ve içeriğini yazdırır. CI bunu her itmede koşuyor.
