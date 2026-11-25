#!/usr/bin/env python3
"""Fetch pinned official dae binaries and their corresponding source."""
import hashlib
import io
import json
from pathlib import Path
import tarfile
import urllib.request
import zipfile

ROOT = Path(__file__).resolve().parents[1]
MANIFEST = json.loads((ROOT / "scripts/dae-core.json").read_text())
DEST = ROOT / "dist/core"

def download(url):
    with urllib.request.urlopen(url, timeout=120) as response:
        return response.read()

def main():
    DEST.mkdir(parents=True, exist_ok=True)
    for arch, upstream in (("amd64", "x86_64"), ("386", "x86_32"), ("arm64", "arm64")):
        name = f"dae-linux-{upstream}.zip"
        spec = MANIFEST["assets"][name]
        cache = DEST / name
        data = cache.read_bytes() if cache.exists() else download(spec["url"])
        if hashlib.sha256(data).hexdigest() != spec["sha256"]:
            raise RuntimeError(f"Official archive checksum mismatch: {name}")
        cache.write_bytes(data)
        with zipfile.ZipFile(io.BytesIO(data)) as archive:
            (DEST / f"dae-linux-{arch}").write_bytes(archive.read(f"dae-linux-{upstream}"))
            for filename in ("geoip.dat", "geosite.dat"):
                (DEST / filename).write_bytes(archive.read(filename))
        print(f"Verified dae {MANIFEST['version']} {arch}", flush=True)
    source = DEST / "dae-source.tar.gz"
    if not source.exists():
        source.write_bytes(download(f"https://codeload.github.com/daeuniverse/dae/tar.gz/refs/tags/{MANIFEST['version']}"))
    with tarfile.open(source) as archive:
        member = next(m for m in archive.getmembers() if m.name.count("/") == 1 and m.name.rsplit("/",1)[-1] == "LICENSE")
        (DEST / "DAE-LICENSE").write_bytes(archive.extractfile(member).read())
    (DEST / "CORE-NOTICE.txt").write_text(
        f"Bundled dae {MANIFEST['version']}\n"
        "Upstream: https://github.com/daeuniverse/dae\n"
        "License: see DAE-LICENSE. Corresponding source: dae-source.tar.gz\n"
        "Official archive SHA-256 hashes: see dae-core.json\n"
        "geoip.dat and geosite.dat are distributed from the same official dae archives.\n")
    (DEST / "dae-core.json").write_text(json.dumps(MANIFEST, indent=2)+"\n")

if __name__ == "__main__":
    main()
