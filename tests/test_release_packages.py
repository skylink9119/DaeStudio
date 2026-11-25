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

class ReleasePackages(unittest.TestCase):
    def check_payload(self, files, core, machine, prefix):
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
