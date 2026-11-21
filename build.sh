#!/usr/bin/env bash
# Собирает Voice2Us для macOS (.app + .dmg) и Windows (.exe).
# Использование: ./build.sh [mac|win|all]   (по умолчанию all)
# Версия: VERSION=1.2.3 ./build.sh  (по умолчанию 1.0.0)
# Требуется: go, Xcode CLT, mingw-w64 (brew install mingw-w64).
set -euo pipefail
cd "$(dirname "$0")"

TARGET="${1:-all}"
VERSION="${VERSION:-1.0.0}"
mkdir -p dist

build_mac() {
echo "==> сборка macOS (universal: arm64 + amd64)..."
for arch in arm64 amd64; do
	GOOS=darwin GOARCH=$arch CGO_ENABLED=1 \
		CC="clang -arch $([ $arch = arm64 ] && echo arm64 || echo x86_64)" \
		go build -trimpath -ldflags "-s -w" -o dist/voice2us-macos-$arch .
done
lipo -create -output dist/voice2us-macos dist/voice2us-macos-arm64 dist/voice2us-macos-amd64
rm -f dist/voice2us-macos-arm64 dist/voice2us-macos-amd64

APP="dist/Voice2Us.app"
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
cp dist/voice2us-macos "$APP/Contents/MacOS/voice2us"
cp assets/icon.icns "$APP/Contents/Resources/icon.icns"
cat > "$APP/Contents/Info.plist" <<'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleName</key>
	<string>Voice2Us</string>
	<key>CFBundleDisplayName</key>
	<string>Voice2Us</string>
	<key>CFBundleExecutable</key>
	<string>voice2us</string>
	<key>CFBundleIdentifier</key>
	<string>ru.jagyar.voice2us</string>
	<key>CFBundlePackageType</key>
	<string>APPL</string>
	<key>CFBundleIconFile</key>
	<string>icon</string>
	<key>CFBundleShortVersionString</key>
	<string>@VERSION@</string>
	<key>CFBundleVersion</key>
	<string>1</string>
	<key>NSHighResolutionCapable</key>
	<true/>
	<key>NSMicrophoneUsageDescription</key>
	<string>Микрофон нужен для голосового чата с другом.</string>
	<key>LSMinimumSystemVersion</key>
	<string>11.0</string>
</dict>
</plist>
EOF
sed -i.bak "s/@VERSION@/$VERSION/" "$APP/Contents/Info.plist" && rm "$APP/Contents/Info.plist.bak"
codesign --force -s - "$APP" 2>/dev/null || true
echo "    готово: $APP"

rm -f dist/Voice2Us-mac.zip dist/Voice2Us.dmg
hdiutil create -quiet -volname "Voice2Us $VERSION" -srcfolder "$APP" -ov -format UDZO dist/Voice2Us.dmg
echo "    готово: dist/Voice2Us.dmg"
}

build_win() {

echo "==> сборка Windows (x64)..."
if [ "$(go env GOOS)" = windows ]; then
	CGO_ENABLED=1 go build -trimpath -ldflags "-H windowsgui -s -w" -o dist/Voice2Us.exe .
else
	GOOS=windows GOARCH=amd64 CGO_ENABLED=1 CC=x86_64-w64-mingw32-gcc \
		go build -trimpath -ldflags "-H windowsgui -s -w" -o dist/Voice2Us.exe .
fi
echo "    готово: dist/Voice2Us.exe"
}

case "$TARGET" in
	mac) build_mac ;;
	win) build_win ;;
	all) build_mac; build_win ;;
	*) echo "usage: $0 [mac|win|all]"; exit 1 ;;
esac

if [ -z "${CI:-}" ]; then
	echo "==> контрольные тесты..."
	go test -race -count=1 -timeout 120s ./... >/dev/null && echo "    тесты: OK"
fi

echo "==> итог в dist/:"
ls -lh dist/
