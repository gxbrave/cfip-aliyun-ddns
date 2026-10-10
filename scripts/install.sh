#!/bin/sh
set -eu
[ "$(id -u)" = 0 ] || { echo '请使用 root 或 sudo 运行'; exit 1; }
[ "$(uname -m)" = x86_64 ] || { echo '仅支持 x64'; exit 1; }
cd "$(dirname "$0")/.."
[ -f cfip-ddns ] || { echo '请先运行 scripts/build.sh，或在解压后的发行包内运行'; exit 1; }
getent passwd cfip-ddns >/dev/null || useradd --system --user-group --home-dir /opt/cfip-ddns --shell /usr/sbin/nologin cfip-ddns
if systemctl is-active --quiet cfip-ddns.service; then systemctl stop cfip-ddns.service; fi
usermod --home /opt/cfip-ddns cfip-ddns
install -d -m 750 -o cfip-ddns -g cfip-ddns /opt/cfip-ddns
install -m 755 cfip-ddns cfst /opt/cfip-ddns/
install -m 644 ip.txt /opt/cfip-ddns/ip.txt
# Preserve existing configuration; migrate the previous installation layout.
if [ ! -e /opt/cfip-ddns/.env ]; then
 if [ -f /etc/cfip-ddns.env ]; then install -m 640 -o root -g cfip-ddns /etc/cfip-ddns.env /opt/cfip-ddns/.env
 elif [ -f .env ]; then install -m 640 -o root -g cfip-ddns .env /opt/cfip-ddns/.env
 else install -m 640 -o root -g cfip-ddns .env.example /opt/cfip-ddns/.env; fi
fi
if [ -d /var/lib/cfip-ddns ]; then
 # Keep every legacy file inside the application folder, even on conflicts.
 install -d -m 700 -o cfip-ddns -g cfip-ddns /opt/cfip-ddns/migration-backup
 cp -a /var/lib/cfip-ddns/. /opt/cfip-ddns/migration-backup/
 for name in bestip.txt selected-ip.txt; do
  if [ -f "/var/lib/cfip-ddns/$name" ] && [ ! -e "/opt/cfip-ddns/$name" ]; then
   install -m 600 -o cfip-ddns -g cfip-ddns "/var/lib/cfip-ddns/$name" "/opt/cfip-ddns/$name"
  fi
 done
fi
if [ -f /etc/cfip-ddns.env ]; then
 install -d -m 700 -o cfip-ddns -g cfip-ddns /opt/cfip-ddns/migration-backup
 install -m 600 -o root -g root /etc/cfip-ddns.env /opt/cfip-ddns/migration-backup/legacy.env
fi
install -m 644 deploy/cfip-ddns.service /opt/cfip-ddns/cfip-ddns.service
ln -sfn /opt/cfip-ddns/cfip-ddns.service /etc/systemd/system/cfip-ddns.service
systemctl daemon-reload
# Migration has completed; systemd retains only the required unit symlink outside /opt.
rm -f /etc/cfip-ddns.env
if [ -d /var/lib/cfip-ddns ]; then rm -rf /var/lib/cfip-ddns; fi
echo '安装完成。编辑 /opt/cfip-ddns/.env 后运行：systemctl enable --now cfip-ddns'
