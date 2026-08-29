# DevBox ikililerini imzalar (taslak — çalıştırılmadı, doğrulanmadı).
#
# Neden bu betik CI'da koşmuyor:
#   Kod imzalama sertifikasının özel anahtarı CI gizli değişkenine
#   konmaz. CI'ya konan bir anahtar, CI'yı ele geçiren herkesin
#   DevBox adına imza atabilmesi demektir. İmzalama, imza için ayrılmış
#   bir makinede ya da bir imzalama hizmetinde (Azure Trusted Signing,
#   SignPath) yapılır.
#
# Kullanım:
#   .\sign.ps1 -Files devbox.exe,DevBox-0.0.0-x64.msi -Thumbprint <parmak izi>

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string[]]$Files,
    [Parameter(Mandatory = $true)][string]$Thumbprint,
    [string]$TimestampUrl = "http://timestamp.digicert.com"
)

$ErrorActionPreference = "Stop"

# Zaman damgası olmadan imza, sertifika süresi dolduğunda geçersiz olur.
# Yıllar sonra indirilen bir sürümün açılabilmesi buna bağlı.
foreach ($file in $Files) {
    if (-not (Test-Path $file)) {
        throw "Dosya yok: $file"
    }

    Write-Host "imzalanıyor: $file"
    & signtool.exe sign `
        /sha1 $Thumbprint `
        /fd SHA256 `
        /td SHA256 `
        /tr $TimestampUrl `
        /d "DevBox — Windows için yerel geliştirme ortamı" `
        /du "https://github.com/krmakmn/devbox" `
        $file
    if ($LASTEXITCODE -ne 0) { throw "signtool başarısız: $file" }

    # İmzayı doğrula: imzalama sessizce başarısız olabiliyor.
    & signtool.exe verify /pa /v $file
    if ($LASTEXITCODE -ne 0) { throw "imza doğrulanamadı: $file" }
}

Write-Host "`n$($Files.Count) dosya imzalandı ve doğrulandı."
