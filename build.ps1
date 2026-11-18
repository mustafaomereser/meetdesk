# Meetdesk derleme betiği. Çıktılar dist/ klasörüne yazılır.
#   Meetdesk.exe                -> Windows, konsolsuz (normal kullanım)
#   Meetdesk-console.exe        -> Windows, konsollu (-headless ve hata ayıklama)
#   Meetdesk-macos-arm64        -> Apple Silicon (M1/M2/M3/M4)
#   Meetdesk-macos-amd64        -> Intel Mac
$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot
New-Item -ItemType Directory -Force dist | Out-Null

$env:CGO_ENABLED = '0'

# Buluşma sunucusu adresi repoda değil: RD_HUB_SERVER ya da yerel .hubserver dosyası.
$hub = $env:RD_HUB_SERVER
if (-not $hub -and (Test-Path .hubserver)) { $hub = (Get-Content .hubserver -Raw).Trim() }
$hubLd = ''
if ($hub) { $hubLd = " -X main.HubServer=$hub" } else { Write-Warning 'Buluşma sunucusu yok: sunucusuz mod derleniyor' }

$env:GOOS = 'windows'; $env:GOARCH = 'amd64'
go vet ./...
go build -trimpath -ldflags "-H=windowsgui -s -w$hubLd" -o dist/Meetdesk.exe .
go build -trimpath -ldflags "-s -w$hubLd" -o dist/Meetdesk-console.exe .
go build -trimpath -o dist/probe.exe ./cmd/probe

$env:GOOS = 'darwin'
foreach ($arch in 'arm64', 'amd64') {
    $env:GOARCH = $arch
    go vet ./...
    go build -trimpath -ldflags "-s -w$hubLd" -o "dist/Meetdesk-macos-$arch" .
}

Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED
Get-ChildItem dist | Select-Object Name, @{n = 'MB'; e = { [math]::Round($_.Length / 1MB, 1) } }
