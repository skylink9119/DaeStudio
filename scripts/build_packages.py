#!/usr/bin/env python3
"""
Builds clean, zero-xattr multi-format Linux / OpenWrt / iStoreOS / Debian installer packages:
  - OpenWrt / iStoreOS .ipk packages (x86_64, i386_pentium4, arm64)
  - Self-extracting .run packages (x86_64, arm64) for iStoreOS Web UI & 1-command CLI install
  - Debian / Ubuntu / PVE .deb packages (amd64, i386, arm64)
  - Offline .tar.gz bundles (x86_64, amd64, x86_32, arm64, universal)
"""

import gzip
import hashlib
import io
import os
import stat
import tarfile
import time

ROOT_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DIST_DIR = os.path.join(ROOT_DIR, "dist")
VERSION = os.environ.get("DAED_NEXT_VERSION", "2.0.0")
MTIME = int(time.time())


def _tar_bytes(entries: list[tuple[str, bytes, int]], fmt=tarfile.USTAR_FORMAT) -> bytes:
    """Create a deterministic gzipped tarball with root:root ownership and no macOS xattrs."""
    raw_buf = io.BytesIO()
    seen_dirs = set()

    with tarfile.open(fileobj=raw_buf, mode="w", format=fmt) as tf:
        for rel_path, data, mode in entries:
            clean = rel_path.lstrip("/")
            parts = clean.split("/")
            for idx in range(1, len(parts)):
                dpath = "./" + "/".join(parts[:idx])
                if dpath not in seen_dirs:
                    seen_dirs.add(dpath)
                    di = tarfile.TarInfo(name=dpath)
                    di.type = tarfile.DIRTYPE
                    di.mode = 0o755
                    di.uid = 0
                    di.gid = 0
                    di.uname = "root"
                    di.gname = "root"
                    di.mtime = MTIME
                    tf.addfile(di)

            ti = tarfile.TarInfo(name="./" + clean)
            ti.size = len(data)
            ti.mode = mode
            ti.uid = 0
            ti.gid = 0
            ti.uname = "root"
            ti.gname = "root"
            ti.mtime = MTIME
            tf.addfile(ti, io.BytesIO(data))

    gz_buf = io.BytesIO()
    with gzip.GzipFile(filename="", mode="wb", fileobj=gz_buf, compresslevel=9, mtime=MTIME) as gz:
        gz.write(raw_buf.getvalue())
    return gz_buf.getvalue()


def _bundle_tar_gz(out_path: str, files: dict[str, tuple[bytes, int]]) -> None:
    raw_buf = io.BytesIO()
    with tarfile.open(fileobj=raw_buf, mode="w", format=tarfile.USTAR_FORMAT) as tf:
        di = tarfile.TarInfo(name="daed-next-install")
        di.type = tarfile.DIRTYPE
        di.mode = 0o755
        di.uid = 0
        di.gid = 0
        di.uname = "root"
        di.gname = "root"
        di.mtime = MTIME
        tf.addfile(di)

        for name, (content, mode) in files.items():
            ti = tarfile.TarInfo(name=f"daed-next-install/{name}")
            ti.size = len(content)
            ti.mode = mode
            ti.uid = 0
            ti.gid = 0
            ti.uname = "root"
            ti.gname = "root"
            ti.mtime = MTIME
            tf.addfile(ti, io.BytesIO(content))

    with open(out_path, "wb") as f:
        with gzip.GzipFile(filename="", mode="wb", fileobj=f, compresslevel=9, mtime=MTIME) as gz:
            gz.write(raw_buf.getvalue())


def build_self_extracting_run(out_path: str, tar_gz_path: str, arch_label: str) -> None:
    with open(tar_gz_path, "rb") as f:
        payload = f.read()

    header = f"""#!/bin/sh
# DaeStudio (daed-next) Self-Extracting Installer ({arch_label})
# Supports OpenWrt / iStoreOS (Web Manual Install & CLI) and Linux systemd
set -e
TMP_EXTRACT="$(mktemp -d /tmp/daed-next-run.XXXXXX)"
cleanup() {{ rm -rf "$TMP_EXTRACT"; }}
trap cleanup EXIT INT TERM

ARCHIVE_LINE="$(awk '/^__ARCHIVE_BELOW__$/ {{print NR + 1; exit 0;}}' "$0")"
tail -n +"$ARCHIVE_LINE" "$0" | gunzip -c | tar -xf - -C "$TMP_EXTRACT"
sh "$TMP_EXTRACT/daed-next-install/install.sh"
exit 0
__ARCHIVE_BELOW__
""".encode("utf-8")

    with open(out_path, "wb") as f:
        f.write(header)
        f.write(payload)
    os.chmod(out_path, 0o755)


def build_openwrt_ipk(out_path: str, bin_bytes: bytes, pkg_arch: str, desc_arch: str) -> None:
    init_bytes = open(os.path.join(ROOT_DIR, "deploy/openwrt/daed-next.init"), "rb").read()
    menu_bytes = open(os.path.join(ROOT_DIR, "deploy/openwrt/luci/menu.json"), "rb").read()
    acl_bytes = open(os.path.join(ROOT_DIR, "deploy/openwrt/luci/acl.json"), "rb").read()
    view_bytes = open(os.path.join(ROOT_DIR, "deploy/openwrt/luci/view.js"), "rb").read()

    # Align procd command flags in init script with /etc/dae/daed-next
    init_script = b"""#!/bin/sh /etc/rc.common
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
"""
    _ = init_bytes

    data_entries = [
        ("usr/share/doc/daed-next/LICENSE", open(os.path.join(ROOT_DIR, "LICENSE"), "rb").read(), 0o644),
        ("usr/bin/daed-next", bin_bytes, 0o755),
        ("etc/init.d/daed-next", init_script, 0o755),
        ("usr/share/luci/menu.d/luci-app-daed-next.json", menu_bytes, 0o644),
        ("usr/share/rpcd/acl.d/luci-app-daed-next.json", acl_bytes, 0o644),
        ("www/luci-static/resources/view/daed-next/main.js", view_bytes, 0o644),
    ]
    data_tgz = _tar_bytes(data_entries)
    installed_size = sum(len(x[1]) for x in data_entries)

    control_text = (
        f"Package: luci-app-daed-next\n"
        f"Version: {VERSION}-1\n"
        f"Depends: libc\n"
        f"License: MIT\n"
        f"Section: luci\n"
        f"Architecture: {pkg_arch}\n"
        f"Installed-Size: {installed_size}\n"
        f"Maintainer: DaeStudio\n"
        f"Description: DaeStudio (daed-next) visual control plane for dae ({desc_arch})\n"
    ).encode("utf-8")

    postinst = b"""#!/bin/sh
[ -n "${IPKG_INSTROOT}" ] && exit 0
mkdir -p /etc/dae/daed-next /usr/share/dae /var/log/dae
if [ -f /etc/dae/config.dae ] && [ ! -f /etc/dae/config.dae.orig_bak ]; then
    cp -p /etc/dae/config.dae /etc/dae/config.dae.orig_bak
    chmod 0600 /etc/dae/config.dae.orig_bak
fi
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
if [ -x /usr/bin/dae ]; then
    /etc/init.d/dae enable </dev/null >/dev/null 2>&1 || true
fi
chmod 0755 /usr/bin/daed-next /etc/init.d/daed-next
rm -f /tmp/luci-indexcache*
rm -rf /tmp/luci-modulecache/*
/etc/init.d/rpcd reload </dev/null >/dev/null 2>&1 || true
/etc/init.d/daed-next enable </dev/null >/dev/null 2>&1 || true
/etc/init.d/daed-next restart </dev/null >/dev/null 2>&1 || true
exit 0
"""

    prerm = b"""#!/bin/sh
[ -n "${IPKG_INSTROOT}" ] && exit 0
/etc/init.d/daed-next stop </dev/null >/dev/null 2>&1 || true
/etc/init.d/daed-next disable </dev/null >/dev/null 2>&1 || true
exit 0
"""

    postrm = b"""#!/bin/sh
[ -n "${IPKG_INSTROOT}" ] && exit 0
rm -f /tmp/luci-indexcache*
rm -rf /tmp/luci-modulecache/*
/etc/init.d/rpcd reload >/dev/null 2>&1 || true
exit 0
"""

    preinst = b"#!/bin/sh\nexit 0\n"
    if pkg_arch == "all":
        preinst = b"#!/bin/sh\n[ -n \"${IPKG_INSTROOT}\" ] && exit 0\ncase \"$(uname -m)\" in aarch64|arm64) exit 0;; *) echo \"ARM64 package requires an ARM64 device\" >&2; exit 1;; esac\n"
    control_tgz = _tar_bytes([
        ("preinst", preinst, 0o755),
        ("control", control_text, 0o644),
        ("postinst", postinst, 0o755),
        ("prerm", prerm, 0o755),
        ("postrm", postrm, 0o755),
    ])

    ipk_bytes = _tar_bytes([
        ("debian-binary", b"2.0\n", 0o644),
        ("control.tar.gz", control_tgz, 0o644),
        ("data.tar.gz", data_tgz, 0o644),
    ])
    with open(out_path, "wb") as f:
        f.write(ipk_bytes)


def _ar_entry(name: str, data: bytes) -> bytes:
    hdr = (
        f"{name:<16}"
        f"{MTIME:<12}"
        f"{0:<6}"
        f"{0:<6}"
        f"{0o100644:<8o}"
        f"{len(data):<10}"
        "`\n"
    ).encode("ascii")
    pad = b"\n" if (len(data) % 2 == 1) else b""
    return hdr + data + pad


def build_debian_deb(out_path: str, bin_bytes: bytes, deb_arch: str) -> None:
    svc_bytes = b"""[Unit]
Description=DaeStudio (daed-next) Control Plane for dae
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
ExecStart=/usr/local/bin/daed-next -listen :2023 -data /etc/dae/daed-next -config /etc/dae/config.dae -dae-bin /usr/bin/dae
Restart=on-failure
RestartSec=3s
LimitMEMLOCK=infinity
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
"""
    data_entries = [
        ("usr/share/doc/daed-next/LICENSE", open(os.path.join(ROOT_DIR, "LICENSE"), "rb").read(), 0o644),
        ("usr/local/bin/daed-next", bin_bytes, 0o755),
        ("lib/systemd/system/daed-next.service", svc_bytes, 0o644),
    ]
    data_tgz = _tar_bytes(data_entries, fmt=tarfile.GNU_FORMAT)
    installed_kb = max(1, (len(bin_bytes) + len(svc_bytes)) // 1024)

    control_text = (
        f"Package: daed-next\n"
        f"Version: {VERSION}\n"
        f"Section: net\n"
        f"Priority: optional\n"
        f"Architecture: {deb_arch}\n"
        f"Installed-Size: {installed_kb}\n"
        f"Maintainer: DaeStudio\n"
        f"Description: DaeStudio (daed-next) high-performance web control panel for dae\n"
    ).encode("utf-8")

    postinst = b"""#!/bin/sh
set -e
mkdir -p /etc/dae/daed-next /usr/share/dae /var/log/dae
if [ -f /etc/dae/config.dae ] && [ ! -f /etc/dae/config.dae.orig_bak ]; then
    cp -p /etc/dae/config.dae /etc/dae/config.dae.orig_bak
    chmod 0600 /etc/dae/config.dae.orig_bak
fi
if [ ! -f /etc/systemd/system/dae.service ] && [ ! -f /lib/systemd/system/dae.service ] && [ ! -f /usr/lib/systemd/system/dae.service ]; then
    cat << 'DAESVCEOF' > /etc/systemd/system/dae.service
[Unit]
Description=dae Transparent Proxy Kernel
After=network-online.target
Wants=network-online.target

[Service]
Type=notify
ExecStart=/usr/bin/dae run --disable-timestamp -c /etc/dae/config.dae
ExecReload=/usr/bin/dae reload $MAINPID
Restart=on-failure
RestartSec=3s
LimitNPROC=512
LimitNOFILE=1048576
LimitMEMLOCK=infinity

[Install]
WantedBy=multi-user.target
DAESVCEOF
fi
if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload >/dev/null 2>&1 || true
    if [ -x /usr/bin/dae ]; then
        systemctl enable dae >/dev/null 2>&1 || true
    fi
    systemctl enable --now daed-next >/dev/null 2>&1 || true
fi
exit 0
"""

    prerm = b"""#!/bin/sh
set -e
if command -v systemctl >/dev/null 2>&1; then
    systemctl disable --now daed-next >/dev/null 2>&1 || true
fi
exit 0
"""

    control_tgz = _tar_bytes([
        ("control", control_text, 0o644),
        ("postinst", postinst, 0o755),
        ("prerm", prerm, 0o755),
    ], fmt=tarfile.GNU_FORMAT)

    deb_bytes = (
        b"!<arch>\n"
        + _ar_entry("debian-binary", b"2.0\n")
        + _ar_entry("control.tar.gz", control_tgz)
        + _ar_entry("data.tar.gz", data_tgz)
    )
    with open(out_path, "wb") as f:
        f.write(deb_bytes)


def main() -> None:
    install_sh = open(os.path.join(ROOT_DIR, "scripts/install.sh"), "rb").read()
    uninstall_sh = open(os.path.join(ROOT_DIR, "scripts/uninstall.sh"), "rb").read()
    license_txt = open(os.path.join(ROOT_DIR, "LICENSE"), "rb").read()

    amd64_bin = open(os.path.join(DIST_DIR, "daed-next-linux-amd64"), "rb").read()
    arm64_bin = open(os.path.join(DIST_DIR, "daed-next-linux-arm64"), "rb").read()
    x86_32_path = os.path.join(DIST_DIR, "daed-next-linux-386")
    x86_32_bin = open(x86_32_path, "rb").read() if os.path.exists(x86_32_path) else None

    # Ensure x86_64 alias binary exists alongside amd64
    x86_64_bin_path = os.path.join(DIST_DIR, "daed-next-linux-x86_64")
    with open(x86_64_bin_path, "wb") as f:
        f.write(amd64_bin)
    os.chmod(x86_64_bin_path, stat.S_IRWXU | stat.S_IRGRP | stat.S_IXGRP | stat.S_IROTH | stat.S_IXOTH)

    # 1. Offline .tar.gz bundles (clean USTAR, zero macOS xattr warnings)
    _bundle_tar_gz(
        os.path.join(DIST_DIR, "daed-next-installer-amd64.tar.gz"),
        {
            "daed-next-linux-amd64": (amd64_bin, 0o755),
            "install.sh": (install_sh, 0o755),
            "uninstall.sh": (uninstall_sh, 0o755),
            "LICENSE": (license_txt, 0o644),
        },
    )
    _bundle_tar_gz(
        os.path.join(DIST_DIR, "daed-next-installer-x86_64.tar.gz"),
        {
            "daed-next-linux-amd64": (amd64_bin, 0o755),
            "install.sh": (install_sh, 0o755),
            "uninstall.sh": (uninstall_sh, 0o755),
            "LICENSE": (license_txt, 0o644),
        },
    )
    _bundle_tar_gz(
        os.path.join(DIST_DIR, "daed-next-installer-arm64.tar.gz"),
        {
            "daed-next-linux-arm64": (arm64_bin, 0o755),
            "install.sh": (install_sh, 0o755),
            "uninstall.sh": (uninstall_sh, 0o755),
            "LICENSE": (license_txt, 0o644),
        },
    )
    uni_files = {
        "daed-next-linux-amd64": (amd64_bin, 0o755),
        "daed-next-linux-arm64": (arm64_bin, 0o755),
        "install.sh": (install_sh, 0o755),
        "uninstall.sh": (uninstall_sh, 0o755),
        "LICENSE": (license_txt, 0o644),
    }
    if x86_32_bin is not None:
        _bundle_tar_gz(
            os.path.join(DIST_DIR, "daed-next-installer-x86_32.tar.gz"),
            {
                "daed-next-linux-386": (x86_32_bin, 0o755),
                "install.sh": (install_sh, 0o755),
                "uninstall.sh": (uninstall_sh, 0o755),
                "LICENSE": (license_txt, 0o644),
            },
        )
        uni_files["daed-next-linux-386"] = (x86_32_bin, 0o755)

    _bundle_tar_gz(os.path.join(DIST_DIR, "daed-next-installer-universal.tar.gz"), uni_files)

    if x86_32_bin is not None:
        build_self_extracting_run(
            os.path.join(DIST_DIR, "daed-next-installer-x86_32.run"),
            os.path.join(DIST_DIR, "daed-next-installer-x86_32.tar.gz"),
            "x86_32 / i386",
        )

    # 2. Self-extracting .run packages (iStoreOS Manual Install & 1-file CLI install)
    build_self_extracting_run(
        os.path.join(DIST_DIR, "daed-next-installer-x86_64.run"),
        os.path.join(DIST_DIR, "daed-next-installer-x86_64.tar.gz"),
        "x86_64 / amd64",
    )
    build_self_extracting_run(
        os.path.join(DIST_DIR, "daed-next-installer-arm64.run"),
        os.path.join(DIST_DIR, "daed-next-installer-arm64.tar.gz"),
        "arm64 / aarch64",
    )

    # 3. OpenWrt / iStoreOS .ipk packages
    build_openwrt_ipk(
        os.path.join(DIST_DIR, f"luci-app-daed-next_{VERSION}_x86_64.ipk"),
        amd64_bin,
        "x86_64",
        "x86_64",
    )
    if x86_32_bin is not None:
        build_openwrt_ipk(
            os.path.join(DIST_DIR, f"luci-app-daed-next_{VERSION}_x86_32.ipk"),
            x86_32_bin,
            "i386_pentium4",
            "x86 32-bit",
        )
    # Use Architecture: all for arm64 .ipk so all OpenWrt arm64 subtargets (aarch64_generic, cortex-a53, cortex-a72) accept it directly
    build_openwrt_ipk(
        os.path.join(DIST_DIR, f"luci-app-daed-next_{VERSION}_arm64.ipk"),
        arm64_bin,
        "all",
        "aarch64 / arm64",
    )

    # 4. Debian / Ubuntu / PVE .deb packages
    build_debian_deb(os.path.join(DIST_DIR, f"daed-next_{VERSION}_amd64.deb"), amd64_bin, "amd64")
    if x86_32_bin is not None:
        build_debian_deb(os.path.join(DIST_DIR, f"daed-next_{VERSION}_i386.deb"), x86_32_bin, "i386")
    build_debian_deb(os.path.join(DIST_DIR, f"daed-next_{VERSION}_arm64.deb"), arm64_bin, "arm64")

    # 5. Generate SHA256SUMS for GitHub Releases & online verification
    sum_lines = []
    for fname in sorted(os.listdir(DIST_DIR)):
        if fname == "SHA256SUMS" or fname.startswith("."):
            continue
        fpath = os.path.join(DIST_DIR, fname)
        if os.path.isfile(fpath):
            h = hashlib.sha256()
            with open(fpath, "rb") as rf:
                for chunk in iter(lambda: rf.read(1 << 20), b""):
                    h.update(chunk)
            sum_lines.append(f"{h.hexdigest()}  {fname}\n")
    with open(os.path.join(DIST_DIR, "SHA256SUMS"), "w", encoding="utf-8") as sf:
        sf.writelines(sum_lines)


if __name__ == "__main__":
    main()
