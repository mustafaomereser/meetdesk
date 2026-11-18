#!/usr/bin/env bash
# Windows exe kaynakları: ikon + sürüm bilgisi (Gezgin'de ikon, Görev Yöneticisi'nde "Meetdesk").
# Çıktı: rsrc_windows_amd64.syso (proje kökünde; go build bunu Windows derlemesine kendiliğinden ekler).
# Gerekenler: packaging/meetdesk.ico (go run ./cmd/mkicon) ve MinGW windres.
set -euo pipefail
cd "$(dirname "$0")/.."
WINDRES="${WINDRES:-$(command -v windres || command -v x86_64-w64-mingw32-windres || true)}"
[ -n "$WINDRES" ] || { echo "UYARI: windres yok, exe kaynakları güncellenmedi" >&2; exit 0; }
VERSION="$(sed -n 's/^const appVersion = "\(.*\)"/\1/p' main.go | tr -d '\r')"
IFS=. read -r MA MI PA <<<"$VERSION"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
cp packaging/meetdesk.ico "$tmp/meetdesk.ico"
cat > "$tmp/meetdesk.rc" <<RC
1 ICON "meetdesk.ico"
1 VERSIONINFO
FILEVERSION ${MA:-0},${MI:-0},${PA:-0},0
PRODUCTVERSION ${MA:-0},${MI:-0},${PA:-0},0
FILEOS 0x40004
FILETYPE 0x1
BEGIN
  BLOCK "StringFileInfo"
  BEGIN
    BLOCK "040904B0"
    BEGIN
      VALUE "CompanyName", "Meetdesk"
      VALUE "FileDescription", "Meetdesk"
      VALUE "FileVersion", "$VERSION"
      VALUE "InternalName", "Meetdesk"
      VALUE "OriginalFilename", "Meetdesk.exe"
      VALUE "ProductName", "Meetdesk"
      VALUE "ProductVersion", "$VERSION"
    END
  END
  BLOCK "VarFileInfo"
  BEGIN
    VALUE "Translation", 0x409, 1200
  END
END
RC
( cd "$tmp" && "$WINDRES" -i meetdesk.rc -o rsrc.syso -O coff -F pe-x86-64 )
cp "$tmp/rsrc.syso" rsrc_windows_amd64.syso
echo "  exe kaynakları: rsrc_windows_amd64.syso ($VERSION)"
