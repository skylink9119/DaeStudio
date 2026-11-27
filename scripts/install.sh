#!/bin/sh
# ==============================================================================
# DaeStudio (daed-next) Universal One-Click Installer
# Supports:
#   - OpenWrt / iStoreOS / ImmortalWrt (procd + LuCI menu + dae init self-healing)
#   - Debian / Ubuntu / Arch / Armbian / CentOS / PVE (systemd)
#   - Architectures: x86_64 (amd64), i386 (386), aarch64 (arm64)
# ==============================================================================

set -e
umask 077
INSTALL_TMP="$(mktemp -d /tmp/daed-next-install.XXXXXX)"
trap 'rm -rf "$INSTALL_TMP"' EXIT
trap 'exit 1' INT TERM

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO="${DAED_NEXT_REPO:-skylink9119/DaeStudio}"

echo "============================================================"
echo "  DaeStudio (daed-next) 安装向导"
echo "============================================================"

if [ "$(id -u)" != "0" ]; then
    echo "[错误] 请使用 root 权限运行此安装脚本 (sudo sh install.sh)"
    exit 1
fi

# 1. Detect CPU architecture
UNAME_M="$(uname -m)"
case "$UNAME_M" in
    x86_64|amd64)
        ARCH="amd64"
        ALT_ARCH="x86_64"
        ;;
    i386|i486|i586|i686|x86)
        ARCH="386"
        ALT_ARCH="x86_32"
        ;;
    aarch64|arm64)
        ARCH="arm64"
        ALT_ARCH="aarch64"
        ;;
    *)
        echo "[错误] 暂不支持的 CPU 架构: $UNAME_M (需 x86_64 / i386 / aarch64)"
        exit 1
        ;;
esac
echo "[1/5] 检测到系统架构: linux/$ARCH ($UNAME_M)"

# 2. Locate or download binary
BIN_SRC=""
if [ -f "$SCRIPT_DIR/daed-next-linux-$ARCH" ]; then
    BIN_SRC="$SCRIPT_DIR/daed-next-linux-$ARCH"
elif [ -n "$ALT_ARCH" ] && [ -f "$SCRIPT_DIR/daed-next-linux-$ALT_ARCH" ]; then
    BIN_SRC="$SCRIPT_DIR/daed-next-linux-$ALT_ARCH"
elif [ -f "$SCRIPT_DIR/daed-next" ]; then
    BIN_SRC="$SCRIPT_DIR/daed-next"
elif [ -n "$REPO" ]; then
    DL_URL="https://github.com/$REPO/releases/latest/download/daed-next-linux-$ARCH"
    echo "[下载] 正在从 $DL_URL 下载二进制..."
    if command -v curl >/dev/null 2>&1; then
        curl -fSL "$DL_URL" -o "$INSTALL_TMP/daed-next-bin"
        curl -fSL "https://github.com/$REPO/releases/latest/download/SHA256SUMS" -o "$INSTALL_TMP/SHA256SUMS"
    else
        wget -O "$INSTALL_TMP/daed-next-bin" "$DL_URL"
        wget -O "$INSTALL_TMP/SHA256SUMS" "https://github.com/$REPO/releases/latest/download/SHA256SUMS"
    fi
    BIN_SRC="$INSTALL_TMP/daed-next-bin"
    EXPECTED="$(awk -v name="daed-next-linux-$ARCH" '$2 == name {print $1}' "$INSTALL_TMP/SHA256SUMS")"
    ACTUAL="$(sha256sum "$BIN_SRC" | awk '{print $1}')"
    if [ -z "$EXPECTED" ] || [ "$EXPECTED" != "$ACTUAL" ]; then
        echo "[错误] 安装包校验失败，未安装"; exit 1
    fi
else
    echo "[错误] 未在安装包同目录找到 daed-next 二进制文件"
    exit 1
fi

# Backup existing config.dae safely if present
mkdir -p /etc/dae/daed-next /usr/share/dae /var/log/dae
if [ -f /etc/dae/config.dae ] && [ ! -f /etc/dae/config.dae.orig_bak ]; then
    cp -p /etc/dae/config.dae /etc/dae/config.dae.orig_bak
    echo "[备份] 已自动备份原有配置至 /etc/dae/config.dae.orig_bak"
fi

chmod 0600 /etc/dae/config.dae.orig_bak 2>/dev/null || true
umask 022

# 3. Install for OpenWrt/iStoreOS (procd) vs Generic Linux (systemd)
if [ -f /etc/openwrt_release ] || [ -f /etc/rc.common ]; then
    echo "[2/5] 识别为 OpenWrt / iStoreOS 系统，正在安装服务与 LuCI 菜单..."
    /etc/init.d/daed-next stop </dev/null >/dev/null 2>&1 || true
    cp -f "$BIN_SRC" /usr/bin/daed-next
    chmod 0755 /usr/bin/daed-next

    # Auto-heal /etc/init.d/dae & /etc/config/dae so dae core is managed cleanly across reboots
    if [ ! -f /etc/init.d/dae ]; then
        cat << 'DAEINITEOF' > /etc/init.d/dae
#!/bin/sh /etc/rc.common
START=99
STOP=10
USE_PROCD=1

PROG="/usr/bin/dae"

start_service() {
    config_load "dae"
    local enabled config_file log_maxbackups log_maxsize
    config_get_bool enabled "config" "enabled" "1"
    [ "$enabled" -eq "1" ] || return 0
    config_get config_file "config" "config_file" "/etc/dae/config.dae"
    config_get log_maxbackups "config" "log_maxbackups" "1"
    config_get log_maxsize "config" "log_maxsize" "5"

    [ -x "$PROG" ] || return 1
    [ -f "$config_file" ] || return 1
    mkdir -p /var/log/dae
    chmod 0600 "$config_file" 2>/dev/null || true

    procd_open_instance "dae"
    procd_set_param command "$PROG" run -c "$config_file" --logfile /var/log/dae/dae.log --logfile-maxbackups "$log_maxbackups" --logfile-maxsize "$log_maxsize"
    procd_set_param limits memlock="unlimited" nofile="1048576 1048576"
    procd_set_param respawn
    procd_close_instance
}

reload_service() {
    local pid
    pid="$(pgrep -f "^$PROG run" | head -n1)"
    if [ -n "$pid" ]; then
        "$PROG" reload "$pid"
    else
        restart
    fi
}
DAEINITEOF
        chmod 0755 /etc/init.d/dae
        echo "[补齐] 已自动创建 /etc/init.d/dae 系统守护脚本"
    fi

    if [ ! -f /etc/config/dae ] || ! grep -q "config dae 'config'" /etc/config/dae 2>/dev/null; then
        cat << 'DAECFG' > /etc/config/dae
config dae 'config'
	option enabled '1'
	option config_file '/etc/dae/config.dae'
	option log_maxbackups '1'
	option log_maxsize '5'
DAECFG
        chmod 0600 /etc/config/dae
    fi

    cat << 'INITEOF' > /etc/init.d/daed-next
#!/bin/sh /etc/rc.common
START=99
STOP=10
USE_PROCD=1

PROG="/usr/bin/daed-next"
DATA_DIR="/etc/dae/daed-next"
CONF_FILE="/etc/dae/config.dae"

start_service() {
    mkdir -p "$DATA_DIR"
    procd_open_instance
    procd_set_param command "$PROG" -listen ":2023" -data "$DATA_DIR" -config "$CONF_FILE" -dae-bin "/usr/bin/dae"
    procd_set_param limits memlock="unlimited" nofile="1048576 1048576"
    procd_set_param respawn
    procd_set_param stdout 1
    procd_set_param stderr 1
    procd_close_instance
}
INITEOF
    chmod 0755 /etc/init.d/daed-next

    # Install LuCI Menu entry if LuCI is present
    if [ -d /usr/share/luci/menu.d ]; then
        mkdir -p /usr/share/luci/menu.d /usr/share/rpcd/acl.d /www/luci-static/resources/view/daed-next
        cat << 'MENUEOF' > /usr/share/luci/menu.d/luci-app-daed-next.json
{
  "admin/services/daed-next": {
    "title": "DaeStudio 面板",
    "order": 15,
    "action": { "type": "view", "path": "daed-next/main" },
    "depends": { "acl": [ "luci-app-daed-next" ] }
  }
}
MENUEOF
        cat << 'ACLEOF' > /usr/share/rpcd/acl.d/luci-app-daed-next.json
{
  "luci-app-daed-next": {
    "description": "Grant access to DaeStudio LuCI view",
    "read": { "ubus": { "luci": ["*"] } }
  }
}
ACLEOF
        cat << 'VIEWEOF' > /www/luci-static/resources/view/daed-next/main.js
'use strict';
'require view';
return view.extend({
	render: function() {
		var panelUrl = 'http://' + window.location.hostname + ':2023';
		var isHttps = window.location.protocol === 'https:';
		return E('div', { 'style': 'display:flex;flex-direction:column;gap:10px;height:calc(100vh - 110px);min-height:680px;' }, [
			E('div', { 'style': 'display:flex;align-items:center;justify-content:space-between;padding:8px 14px;background:#141923;color:#e6edf3;border-radius:8px;' }, [
				E('div', { 'style': 'font-weight:600;font-size:13.5px;' }, 'DaeStudio · dae 控制中心'),
				E('a', { 'href': panelUrl, 'target': '_blank', 'rel': 'noopener noreferrer', 'class': 'cbi-button cbi-button-action', 'style': 'text-decoration:none;' }, '全屏打开面板 (:2023)')
			]),
			isHttps
				? E('div', { 'style': 'padding:28px;border:1px solid rgba(128,128,128,0.2);border-radius:10px;background:#0b0e14;color:#cbd5e1;text-align:center;' }, [
					E('p', { 'style': 'margin:0 0 12px;' }, '当前 LuCI 为 HTTPS 访问，请点击下方按钮独立打开 HTTP (:2023) 面板：'),
					E('a', { 'href': panelUrl, 'target': '_blank', 'rel': 'noopener noreferrer', 'class': 'cbi-button cbi-button-apply', 'style': 'text-decoration:none;' }, '打开 ' + panelUrl)
				])
				: E('iframe', { 'src': panelUrl, 'style': 'width:100%;flex:1;border:1px solid rgba(128,128,128,0.2);border-radius:10px;background:#0b0e14;' })
		]);
	},
	handleSaveApply: null, handleSave: null, handleReset: null
});
VIEWEOF
        rm -f /tmp/luci-indexcache*
        rm -rf /tmp/luci-modulecache/*
        /etc/init.d/rpcd reload </dev/null >/dev/null 2>&1 || true
    fi

    echo "[3/5] 正在检测 dae 内核与 GeoIP / GeoSite 分流数据库..."
    if [ "${DAED_SKIP_BOOTSTRAP:-0}" != "1" ]; then
        if [ ! -x /usr/bin/dae ] || [ ! -f /usr/share/dae/geoip.dat ] || [ ! -f /usr/share/dae/geosite.dat ]; then
            /usr/bin/daed-next -bootstrap-env -dae-bin /usr/bin/dae -config /etc/dae/config.dae || \
                echo "[提示] 在线自动下载内核或分流库未完成（可能当前网络受限），稍后可在面板网页端一键下载安装。"
        else
            echo "[自检] 已检测到 /usr/bin/dae 内核与分流数据库"
        fi
    fi
    if [ -x /usr/bin/dae ]; then
        /etc/init.d/dae enable </dev/null >/dev/null 2>&1 || true
    fi

    echo "[4/5] 正在启用开机自启并启动 DaeStudio 服务..."
    /etc/init.d/daed-next enable </dev/null >/dev/null 2>&1 || true
    /etc/init.d/daed-next restart </dev/null >/dev/null 2>&1
else
    echo "[2/5] 识别为通用 Linux (systemd) 系统，正在注册服务..."
    systemctl stop daed-next >/dev/null 2>&1 || true
    cp -f "$BIN_SRC" /usr/local/bin/daed-next
    chmod 0755 /usr/local/bin/daed-next

    DAE_PATH="$(command -v dae || echo /usr/bin/dae)"
    if [ ! -f /etc/systemd/system/dae.service ] && [ ! -f /lib/systemd/system/dae.service ] && [ ! -f /usr/lib/systemd/system/dae.service ]; then
        cat << DAESVCEOF > /etc/systemd/system/dae.service
[Unit]
Description=dae Transparent Proxy Kernel
After=network-online.target
Wants=network-online.target

[Service]
Type=notify
ExecStart=$DAE_PATH run --disable-timestamp -c /etc/dae/config.dae
ExecReload=$DAE_PATH reload \$MAINPID
Restart=on-failure
RestartSec=3s
LimitNPROC=512
LimitNOFILE=1048576
LimitMEMLOCK=infinity

[Install]
WantedBy=multi-user.target
DAESVCEOF
    fi

    cat << SVCEOF > /etc/systemd/system/daed-next.service
[Unit]
Description=DaeStudio (daed-next) Control Plane for dae
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
ExecStart=/usr/local/bin/daed-next -listen :2023 -data /etc/dae/daed-next -config /etc/dae/config.dae -dae-bin $DAE_PATH
Restart=on-failure
RestartSec=3s
LimitMEMLOCK=infinity
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
SVCEOF

    echo "[3/5] 正在检测 dae 内核与 GeoIP / GeoSite 分流数据库..."
    if [ "${DAED_SKIP_BOOTSTRAP:-0}" != "1" ]; then
        if [ ! -x "$DAE_PATH" ] || [ ! -f /usr/share/dae/geoip.dat ] || [ ! -f /usr/share/dae/geosite.dat ]; then
            /usr/local/bin/daed-next -bootstrap-env -dae-bin "$DAE_PATH" -config /etc/dae/config.dae || \
                echo "[提示] 在线自动下载内核或分流库未完成，稍后可在面板网页端一键下载安装。"
        else
            echo "[自检] 已检测到 $DAE_PATH 内核与分流数据库"
        fi
    fi

    echo "[4/5] 正在重载 systemd 并启动 DaeStudio..."
    systemctl daemon-reload
    if [ -x "$DAE_PATH" ]; then
        systemctl enable dae >/dev/null 2>&1 || true
    fi
    systemctl enable --now daed-next
fi

# 5. Detect LAN IP for convenience
LAN_IP="$(ip -4 addr show br-lan 2>/dev/null | awk '/inet / {split($2,a,"/"); print a[1]; exit}')"
if [ -z "$LAN_IP" ]; then
    LAN_IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
fi
[ -z "$LAN_IP" ] && LAN_IP="<设备IP>"

echo "[5/5] 安装完成！"
echo "============================================================"
echo "  访问地址 : http://${LAN_IP}:2023"
echo "  LuCI入口 : 软路由后台 -> 服务 -> DaeStudio 面板"
echo "============================================================"
