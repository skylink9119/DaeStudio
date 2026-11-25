#!/bin/sh
# Packages ready-to-share offline installers (.ipk, .run, .deb, .tar.gz) for x86_64, x86_32, arm64, and universal

set -e
ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
DIST_DIR="$ROOT_DIR/dist"

chmod +x "$ROOT_DIR/scripts/install.sh" "$ROOT_DIR/scripts/uninstall.sh" "$ROOT_DIR/scripts/build_packages.py"
python3 "$ROOT_DIR/scripts/fetch_core.py"
python3 "$ROOT_DIR/scripts/build_packages.py"

ls -lh "$DIST_DIR"/*.ipk "$DIST_DIR"/*.run "$DIST_DIR"/*.deb "$DIST_DIR"/*.tar.gz
