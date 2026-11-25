#!/usr/bin/env python3
"""End-to-end automated test suite for DaeStudio (daed-next)."""

import base64
import io
import json
import os
import socket
import sys
import tempfile
import threading
import unittest
import urllib.request
from http.server import ThreadingHTTPServer

ROOT_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(ROOT_DIR, "scripts"))

import preview_server as srv  # noqa: E402


class InMemoryHandlerInvoker:
    """Invokes DaeStudioHandler directly in memory when loopback sockets are sandboxed."""

    def __init__(self, ctx: srv.AppContext):
        self.ctx = ctx

    def call(self, method: str, path: str, payload=None) -> tuple[int, bytes]:
        body_bytes = json.dumps(payload).encode("utf-8") if payload is not None else b""
        raw_req = (
            f"{method} {path} HTTP/1.1\r\n"
            f"Host: localhost\r\n"
            f"Content-Type: application/json\r\n"
            f"Content-Length: {len(body_bytes)}\r\n\r\n"
        ).encode("utf-8") + body_bytes

        rfile = io.BytesIO(raw_req)
        wfile = io.BytesIO()

        class DummySocket:
            def makefile(self, mode, *args, **kwargs):
                return rfile if "r" in mode else wfile

            def sendall(self, data):
                wfile.write(data)

            def close(self):
                pass

        srv.DaeStudioHandler.ctx = self.ctx
        srv.DaeStudioHandler(DummySocket(), ("127.0.0.1", 12345), None)
        resp_raw = wfile.getvalue()
        header_part, _, resp_body = resp_raw.partition(b"\r\n\r\n")
        status_line = header_part.splitlines()[0].decode("utf-8")
        status_code = int(status_line.split()[1])
        return status_code, resp_body


class TestDaeStudio(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmpdir = tempfile.mkdtemp(prefix="daed_next_test_")
        cls.cfg_path = os.path.join(cls.tmpdir, "config.dae")
        cls.ctx = srv.AppContext(cls.tmpdir, cls.cfg_path)
        cls.invoker = InMemoryHandlerInvoker(cls.ctx)
        cls.use_tcp = False
        cls.probe_port = 443

        # Attempt real loopback socket if allowed by environment
        try:
            probe_sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
            probe_sock.bind(("127.0.0.1", 0))
            probe_sock.listen(5)
            port = probe_sock.getsockname()[1]
            test_client = socket.create_connection(("127.0.0.1", port), timeout=0.5)
            test_client.close()
            conn, _ = probe_sock.accept()
            conn.close()

            cls.probe_sock = probe_sock
            cls.probe_port = port

            def _accept_loop():
                while True:
                    try:
                        c, _ = cls.probe_sock.accept()
                        c.close()
                    except Exception:
                        break

            threading.Thread(target=_accept_loop, daemon=True).start()

            srv.DaeStudioHandler.ctx = cls.ctx
            cls.httpd = ThreadingHTTPServer(("127.0.0.1", 0), srv.DaeStudioHandler)
            cls.port = cls.httpd.server_address[1]
            threading.Thread(target=cls.httpd.serve_forever, daemon=True).start()
            cls.use_tcp = True
        except PermissionError:
            probe_sock.close()
            cls.use_tcp = False

    @classmethod
    def tearDownClass(cls):
        if getattr(cls, "use_tcp", False):
            cls.httpd.shutdown()
            cls.probe_sock.close()

    def _raw_get(self, path: str) -> tuple[int, bytes]:
        if self.use_tcp:
            with urllib.request.urlopen(f"http://127.0.0.1:{self.port}{path}", timeout=5) as r:
                return r.status, r.read()
        return self.invoker.call("GET", path)

    def _api(self, path: str, payload=None) -> dict:
        if self.use_tcp:
            url = f"http://127.0.0.1:{self.port}{path}"
            if payload is None:
                with urllib.request.urlopen(url, timeout=5) as resp:
                    return json.loads(resp.read().decode("utf-8"))
            data = json.dumps(payload).encode("utf-8")
            req = urllib.request.Request(url, data=data, headers={"Content-Type": "application/json"}, method="POST")
            with urllib.request.urlopen(req, timeout=5) as resp:
                return json.loads(resp.read().decode("utf-8"))
        method = "GET" if payload is None else "POST"
        code, body = self.invoker.call(method, path, payload)
        self.assertIn(code, (200, 401), msg=f"Unexpected HTTP {code}: {body}")
        return json.loads(body.decode("utf-8"))

    def test_01_bootstrap_and_static_assets(self):
        boot = self._api("/api/v1/bootstrap")
        self.assertIn("state", boot)
        self.assertIn("configDae", boot)
        self.assertIn("global {", boot["configDae"])
        self.assertIn("routing {", boot["configDae"])
        self.assertEqual(srv.validate_syntax(boot["configDae"]), [])
        self.assertTrue(os.path.exists(self.cfg_path))

        for asset in ["/", "/css/app.css", "/js/app.js", "/js/store.js", "/js/presets.js", "/js/charts.js"]:
            status, content = self._raw_get(asset)
            self.assertEqual(status, 200)
            self.assertGreater(len(content), 100)

    def test_02_clash_and_singbox_and_base64_subscriptions(self):
        clash_yaml = """
proxies:
  - { name: '🇭🇰 香港 IEPL Clash', type: vless, server: 127.0.0.1, port: %d, uuid: 11111111-1111-1111-1111-111111111111, tls: true, servername: hk.test }
  - { name: '🇯🇵 日本 Hy2 Clash', type: hysteria2, server: 127.0.0.1, port: %d, password: pass, sni: jp.test }
  - { name: '剩余流量: 999GB', type: ss, server: 127.0.0.1, port: 8388, cipher: aes-256-gcm, password: x }
""" % (self.probe_port, self.probe_port)

        res = self._api(
            "/api/v1/subscriptions/parse",
            {
                "name": "Clash 测试源",
                "tag": "clash_sub",
                "rawContent": clash_yaml,
                "excludeKeywords": "剩余流量|到期",
            },
        )
        self.assertTrue(res["ok"])
        self.assertEqual(res["detectedFormat"], "clash")
        self.assertEqual(res["addedCount"], 2)
        self.assertEqual(srv.validate_syntax(res["configDae"]), [])

        sb_json = json.dumps(
            {
                "outbounds": [
                    {
                        "type": "trojan",
                        "tag": "🇺🇸 美国 Singbox 节点",
                        "server": "127.0.0.1",
                        "server_port": self.probe_port,
                        "password": "secret",
                        "tls": {"server_name": "us.test"},
                    }
                ]
            }
        )
        res_sb = self._api("/api/v1/subscriptions/parse", {"name": "Singbox 源", "tag": "sb_sub", "rawContent": sb_json})
        self.assertTrue(res_sb["ok"])
        self.assertEqual(res_sb["detectedFormat"], "singbox")
        self.assertEqual(res_sb["addedCount"], 1)

        b64_uris = base64.b64encode(f"vless://uuid@127.0.0.1:{self.probe_port}?security=tls#SG-Base64-Node\n".encode()).decode()
        res_b64 = self._api("/api/v1/nodes/import", {"rawText": b64_uris})
        self.assertTrue(res_b64["ok"])
        self.assertEqual(res_b64["detectedFormat"], "base64")
        self.assertEqual(res_b64["addedCount"], 1)

    def test_03_active_concurrent_latency_prober(self):
        boot = self._api("/api/v1/bootstrap")
        local_nodes = [n["id"] for n in boot["state"]["nodes"] if n["address"] == "127.0.0.1"]
        self.assertGreaterEqual(len(local_nodes), 3)

        probe_res = self._api("/api/v1/nodes/probe", {"nodeIds": local_nodes, "timeoutMs": 800})
        self.assertTrue(probe_res["ok"])
        self.assertEqual(len(probe_res["results"]), len(local_nodes))
        if self.use_tcp:
            for r in probe_res["results"]:
                self.assertTrue(r["alive"])
                self.assertGreaterEqual(r["latencyMs"], 1)

    def test_04_bidirectional_routing_dsl_and_snapshots_and_import(self):
        dsl = """
        # OpenAI 分流测试
        domain(geosite:openai) && !dip(geoip:cn) -> ai_service
        # [Disabled] l4proto(udp) && dport(443) -> block
        fallback: proxy
        """
        parsed = self._api("/api/v1/routing/parse-dsl", {"dsl": dsl})
        self.assertTrue(parsed["ok"])
        self.assertEqual(parsed["fallback"], "proxy")
        self.assertEqual(len(parsed["rules"]), 2)
        self.assertTrue(parsed["rules"][0]["enabled"])
        self.assertFalse(parsed["rules"][1]["enabled"])
        self.assertEqual(len(parsed["rules"][0]["conditions"]), 2)

        snap_res = self._api("/api/v1/snapshots", {"name": "单元测试快照"})
        self.assertTrue(snap_res["ok"])
        snap_id = snap_res["snapshots"][0]["id"]

        restore_res = self._api("/api/v1/snapshots/restore", {"id": snap_id})
        self.assertTrue(restore_res["ok"])
        self.assertEqual(srv.validate_syntax(restore_res["configDae"]), [])

        imp_res = self._api("/api/v1/config/import", {"configDae": restore_res["configDae"]})
        self.assertTrue(imp_res["ok"])
        self.assertEqual(srv.validate_syntax(imp_res["configDae"]), [])

        dns_dsl = """
        upstream {
          cndns: 'https://223.5.5.5/dns-query'
          fallbackdns: 'https://1.1.1.1/dns-query'
        }
        bind: '127.0.0.1:15353'
        ipversion_prefer: 4
        routing {
          request {
            qname(geosite:cn) -> cndns
            fallback: fallbackdns
          }
        }
        """
        dns_parsed = self._api("/api/v1/dns/parse-dsl", {"dsl": dns_dsl})
        self.assertTrue(dns_parsed["ok"])
        self.assertEqual(dns_parsed["dns"]["bind"], "127.0.0.1:15353")
        self.assertEqual(dns_parsed["dns"]["ipVersionPrefer"], 4)
        self.assertEqual(dns_parsed["dns"]["requestFallback"], "fallbackdns")
        self.assertEqual(len(dns_parsed["dns"]["upstreams"]), 2)

    def test_05_service_lifecycle_and_validation(self):
        for action in ("validate", "reload", "stop", "start"):
            r = self._api("/api/v1/service/action", {"action": action})
            self.assertTrue(r["ok"], msg=f"Action {action} failed: {r}")


if __name__ == "__main__":
    unittest.main(verbosity=2)
