"""Check every installer format contains the correct core and required data."""
import io
from pathlib import Path
import struct
import os
import sys
import tarfile
import unittest

DIST = Path(__file__).resolve().parents[1] / "dist"
sys.path.insert(0, str(DIST.parent / "scripts"))
from build_packages import VERSION as DEFAULT_VERSION
VERSION = os.environ.get("RELEASE_TAG", DEFAULT_VERSION).removeprefix("v")

def entries(data):
    with tarfile.open(fileobj=io.BytesIO(data), mode="r:gz") as archive:
        return {m.name.removeprefix("./"): archive.extractfile(m).read()
                for m in archive.getmembers() if m.isfile()}

def deb_data(path):
    raw = path.read_bytes()
    assert raw[:8] == b"!<arch>\n"
    pos = 8
    while pos < len(raw):
        header = raw[pos:pos+60]
        size = int(header[48:58])
        payload = raw[pos+60:pos+60+size]
        if header[:16].decode().strip().rstrip("/") == "data.tar.gz":
            return entries(payload)
        pos += 60 + size + size % 2
    raise AssertionError("missing data.tar.gz")

def protobuf_fields(data):
    pos = 0
    def varint():
        nonlocal pos
        value, shift = 0, 0
        while True:
            byte = data[pos]
            pos += 1
            value |= (byte & 127) << shift
            if byte < 128:
                return value
            shift += 7
    while pos < len(data):
        key = varint()
        wire = key & 7
        if wire == 2:
            size = varint()
            value = data[pos:pos + size]
            pos += size
            yield key >> 3, value
        elif wire == 0:
            varint()
        elif wire == 1:
            pos += 8
        elif wire == 5:
            pos += 4
        else:
            raise ValueError("Unsupported protobuf field")

def database_codes(path):
    codes = set()
    for field, entry in protobuf_fields(path.read_bytes()):
        if field == 1:
            for number, value in protobuf_fields(entry):
                if number == 1:
                    codes.add(value.decode().lower())
    return codes

class ReleasePackages(unittest.TestCase):
    def test_default_geo_references_exist(self):
        import re
        profile = (DIST.parent / "internal/model/default_profile.json").read_text()
        for kind in ("geosite", "geoip"):
            codes = database_codes(DIST / "core" / (kind + ".dat"))
            references = set(re.findall(kind + r":([a-zA-Z0-9_!.-]+)", profile))
            self.assertTrue(references)
            self.assertFalse(references - codes, f"Missing {kind} categories: {references - codes}")

    def check_payload(self, files, core, machine, prefix):
        profile = (DIST.parent / "internal/model/default_profile.json").read_bytes()
        panels = [data for name, data in files.items()
                  if name in ("usr/bin/daed-next", "usr/local/bin/daed-next") or "/daed-next-linux-" in name]
        self.assertTrue(panels, "missing panel binary")
        for panel in panels:
            self.assertTrue(profile in panel, "installer embeds an outdated default profile")
        self.assertEqual(files[core][:4], b"\x7fELF")
        self.assertEqual(struct.unpack("<H", files[core][18:20])[0], machine)
        for name in ("geoip.dat", "geosite.dat"):
            self.assertGreater(len(files[prefix+name]), 1000)
        for suffix in ("DAE-LICENSE", "CORE-NOTICE.txt", "dae-source.tar.gz", "dae-core.json"):
            self.assertTrue(any(n.endswith("/"+suffix) for n in files))
        self.assertFalse(any(n.endswith(("/state.json", "/config.dae")) for n in files))

    def test_all_installers(self):
        for arch, machine, corearch, debarch in (
            ("arm64",183,"arm64","arm64"), ("x86_64",62,"amd64","amd64"), ("x86_32",3,"386","i386")):
            with self.subTest(arch=arch):
                bundle=entries((DIST/f"daed-next-installer-{arch}.tar.gz").read_bytes())
                self.check_payload(bundle,f"daed-next-install/dae-linux-{corearch}",machine,"daed-next-install/")
                run=(DIST/f"daed-next-installer-{arch}.run").read_bytes()
                self.assertEqual(entries(run.split(b"\n__ARCHIVE_BELOW__\n",1)[1]),bundle)
                ipk=entries((DIST/f"luci-app-daed-next_{VERSION}_{arch}.ipk").read_bytes())
                self.check_payload(entries(ipk["data.tar.gz"]),"usr/bin/dae",machine,"usr/share/dae/")
                self.check_payload(deb_data(DIST/f"daed-next_{VERSION}_{debarch}.deb"),"usr/bin/dae",machine,"usr/share/dae/")
        universal=entries((DIST/"daed-next-installer-universal.tar.gz").read_bytes())
        for arch in ("amd64","386","arm64"):
            self.assertIn("daed-next-install/dae-linux-"+arch,universal)

if __name__ == "__main__":
    unittest.main()
