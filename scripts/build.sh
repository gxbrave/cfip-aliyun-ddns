#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
mkdir -p dist
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o dist/cfip-ddns ./cmd/cfip-ddns
install -m 755 cfst dist/cfst
install -m 644 ip.txt dist/ip.txt
install -m 644 .env.example dist/.env.example
mkdir -p dist/deploy dist/scripts
install -m 644 deploy/cfip-ddns.service dist/deploy/cfip-ddns.service
install -m 755 scripts/install.sh dist/scripts/install.sh
install -m 644 README.md dist/README.md
mkdir -p dist/third_party
cp -R third_party/CloudflareSpeedTest dist/third_party/
tar -czf dist/cfip-ddns-linux-amd64.tar.gz -C dist cfip-ddns cfst ip.txt .env.example deploy scripts README.md third_party
(cd dist && sha256sum cfip-ddns-linux-amd64.tar.gz > cfip-ddns-linux-amd64.tar.gz.sha256)
