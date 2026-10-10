#!/bin/sh
set -eu

REPO="${CFIP_REPO:-gxbrave/cfip-aliyun-ddns}"
VERSION="${CFIP_VERSION:-v1.0.1}"
MIRROR="${CFIP_MIRROR:-github}"
ASSET="cfip-ddns-linux-amd64.tar.gz"
BASE="https://github.com/$REPO/releases/download/$VERSION"
if [ "$MIRROR" = "ghfast" ]; then
  BASE="https://ghfast.top/$BASE"
elif [ "$MIRROR" != "github" ]; then
  echo 'CFIP_MIRROR 只能是 github 或 ghfast' >&2
  exit 1
fi
TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT
curl -fL --retry 3 --connect-timeout 10 "$BASE/$ASSET" -o "$TMP_DIR/$ASSET"
curl -fL --retry 3 --connect-timeout 10 "$BASE/$ASSET.sha256" -o "$TMP_DIR/$ASSET.sha256"
(cd "$TMP_DIR" && sha256sum -c "$ASSET.sha256")
tar -xzf "$TMP_DIR/$ASSET" -C "$TMP_DIR"
cd "$TMP_DIR"
exec ./scripts/install.sh
