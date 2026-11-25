#!/bin/sh
# DaeStudio (daed-next) Uninstaller (keeps /etc/dae/config.dae safe!)

set -e

echo "正在卸载 DaeStudio (保留 /etc/dae/config.dae 配置文件)..."

if [ -f /etc/init.d/daed-next ]; then
    /etc/init.d/daed-next stop >/dev/null 2>&1 || true
    /etc/init.d/daed-next disable >/dev/null 2>&1 || true
    rm -f /etc/init.d/daed-next /usr/bin/daed-next
    rm -f /usr/share/luci/menu.d/luci-app-daed-next.json /usr/share/rpcd/acl.d/luci-app-daed-next.json
    rm -rf /www/luci-static/resources/view/daed-next /tmp/luci-indexcache* /tmp/luci-modulecache/*
    /etc/init.d/rpcd reload >/dev/null 2>&1 || true
elif command -v systemctl >/dev/null 2>&1; then
    systemctl disable --now daed-next >/dev/null 2>&1 || true
    rm -f /etc/systemd/system/daed-next.service /usr/local/bin/daed-next
    systemctl daemon-reload >/dev/null 2>&1 || true
fi

echo "卸载完成（原 /etc/dae/config.dae 未删除）。"
