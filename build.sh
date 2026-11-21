#!/usr/bin/env bash
# Собирает Voice2Us.app + Voice2Us.dmg для macOS и Voice2Us.exe для Windows.
# Требуется: go, Xcode CLT, mingw-w64 (brew install mingw-w64).
set -euo pipefail
cd "$(dirname "$0")"

mkdir -p dist

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
mkdir -p "$APP/Contents/MacOS"
cp dist/voice2us-macos "$APP/Contents/MacOS/voice2us"
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
	<key>CFBundleShortVersionString</key>
	<string>1.0.0</string>
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
codesign --force -s - "$APP" 2>/dev/null || true
echo "    готово: $APP"

echo "==> упаковка DMG..."
rm -f dist/Voice2Us.dmg
hdiutil create -volname Voice2Us -srcfolder "$APP" -ov -format UDZO dist/Voice2Us.dmg >/dev/null
echo "    готово: dist/Voice2Us.dmg"

echo "==> сборка Windows (x64)..."
GOOS=windows GOARCH=amd64 CGO_ENABLED=1 CC=x86_64-w64-mingw32-gcc \
	go build -trimpath -ldflags "-H windowsgui -s -w" -o dist/Voice2Us.exe .
echo "    готово: dist/Voice2Us.exe"

( cd dist && zip -qr Voice2Us-mac.zip Voice2Us.app && zip -qr Voice2Us-windows.zip Voice2Us.exe )

echo "==> контрольные тесты..."
go test -race -count=1 -timeout 120s ./... >/dev/null && echo "    тесты: OK"

echo "==> итог в dist/:"
ls -lh dist/ | awk '{print "    " $9, $5}'
