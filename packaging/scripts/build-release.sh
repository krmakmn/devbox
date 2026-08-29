#!/bin/sh
# DevBox sürüm ikililerini üretir.
#
# Bu betik çalıştırılabilir ve doğrulandı — imzalama ve MSI adımlarının
# aksine, çapraz derleme bu ortamda yapılabiliyor.
#
# Kullanım: packaging/scripts/build-release.sh v0.1.0

set -eu

SURUM="${1:-devel}"
CIKTI="${CIKTI:-dist}"

mkdir -p "$CIKTI"

# -trimpath: ikilinin içinde derleyen makinenin dizin yapısı kalmasın.
# -s -w: hata ayıklama simgeleri çıkarılsın; ikili küçülüyor.
BAYRAKLAR="-trimpath"
LDFLAGS="-s -w -X main.version=$SURUM"

for hedef in "windows/amd64" "windows/arm64" "linux/amd64" "darwin/arm64"; do
    GOOS="${hedef%/*}"
    GOARCH="${hedef#*/}"
    ad="devbox-$SURUM-$GOOS-$GOARCH"
    [ "$GOOS" = "windows" ] && ad="$ad.exe"

    echo "derleniyor: $ad"
    GOOS="$GOOS" GOARCH="$GOARCH" CGO_ENABLED=0 \
        go build $BAYRAKLAR -ldflags "$LDFLAGS" -o "$CIKTI/$ad" ./cmd/devbox
done

# Sağlama toplamları: winget manifesti ve indirenler için.
( cd "$CIKTI" && sha256sum devbox-* > "SHA256SUMS-$SURUM.txt" )

echo
echo "üretilenler:"
ls -la "$CIKTI"
