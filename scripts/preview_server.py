#!/usr/bin/env python3
"""
DaeStudio (daed-next) Local Preview & Fallback Server (Zero External Dependencies)
Mirrors the exact /api/v1/* contract of the Go single-binary server so you can preview,
develop, convert subscriptions, compile/import config.dae, and probe nodes immediately
even on hosts without Go installed.
"""

import argparse
import base64
import concurrent.futures
import hashlib
import json
import math
import os
import platform
import re
import secrets
import shutil
import socket
import subprocess
import threading
import time
import urllib.parse
import urllib.request
from datetime import datetime, timezone
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer

ROOT_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
WEB_DIR = os.path.join(ROOT_DIR, "web")

REGION_REGEX_MAP = {
    "HK": r"HK|Hong\s*Kong|香港|港",
    "TW": r"TW|Taiwan|台湾|台|新北|台北|彰化",
    "JP": r"JP|Japan|日本|东京|大阪|埼玉",
    "SG": r"SG|Singapore|新加坡|狮城",
    "US": r"US|United\s*States|美国|美|硅谷|洛杉矶|西雅图|圣何塞",
    "KR": r"KR|Korea|韩国|首尔|韩",
    "DE": r"DE|Germany|德国|法兰克福",
    "UK": r"UK|GB|Britain|英国|伦敦",
}


def now_iso() -> str:
    return datetime.now(timezone.utc).astimezone().isoformat(timespec="seconds")


def now_hms() -> str:
    return datetime.now().strftime("%H:%M:%S")


def sanitize_ident(s: str) -> str:
    s = (s or "").strip()
    if not s:
        return ""
    out = []
    for i, ch in enumerate(s):
        if ch.isalpha() or ch == "_" or (i > 0 and (ch.isdigit() or ch == "-")):
            out.append(ch)
        elif i == 0 and ch.isdigit():
            out.append("n_" + ch)
        else:
            out.append("_")
    return "".join(out)


def esc_sq(s: str) -> str:
    return (s or "").replace("'", "\\'")


def default_state() -> dict:
    ts = now_iso()
    return {
        "version": "1.0.0",
        "updatedAt": ts,
        "auth": {"enabled": False, "username": "admin", "passwordHash": ""},
        "global": {
            "wanInterface": "auto",
            "lanInterfaces": ["br-lan"],
            "dialMode": "domain++",
            "logLevel": "info",
            "tproxyPort": 12345,
            "tproxyPortProtect": True,
            "soMarkFromDae": 0,
            "allowInsecure": False,
            "autoConfigKernelParameter": True,
            "checkInterval": "30s",
            "checkTolerance": "50ms",
            "tcpCheckUrl": "http://cp.cloudflare.com,1.1.1.1,2606:4700:4700::1111",
            "tcpCheckHttpMethod": "HEAD",
            "udpCheckDns": "dns.google:53,8.8.8.8,2001:4860:4860::8888",
            "sniffingTimeout": "100ms",
            "tlsImplementation": "tls",
            "utlsImitate": "chrome_auto",
            "mptcp": False,
            "disableWaitingNetwork": False,
        },
        "subscriptions": [
            {
                "id": "sub-main",
                "tag": "airport_main",
                "name": "主力高速订阅 (多格式自动识别)",
                "url": "https://sub.example.com/api/v1/client/subscribe?token=demo",
                "format": "auto",
                "userAgent": "clash.meta",
                "enabled": True,
                "autoUpdateHours": 6,
                "excludeKeywords": "剩余流量|套餐到期|官网|重置",
                "persistConverted": False,
                "nodeCount": 6,
                "usedUploadBytes": 12884901888,
                "usedDownloadBytes": 79456894976,
                "totalTrafficBytes": 536870912000,
                "expireUnix": int(time.time()) + 90 * 86400,
                "updatedAt": ts,
            }
        ],
        "nodes": [
            {
                "id": "node-hk-01",
                "tag": "hk_iepl_01",
                "name": "🇭🇰 香港 IEPL 专线 01",
                "protocol": "vless",
                "address": "1.1.1.1",
                "port": 443,
                "uri": "vless://00000000-0000-0000-0000-000000000001@1.1.1.1:443?encryption=none&security=reality&sni=www.microsoft.com&fp=chrome&pbk=demo#HK-IEPL-01",
                "subId": "sub-main",
                "subTag": "airport_main",
                "region": "HK",
                "enabled": True,
                "latencyMs": 28,
                "alive": True,
                "checkedAt": ts,
            },
            {
                "id": "node-hk-02",
                "tag": "hk_hy2_02",
                "name": "🇭🇰 香港 Hysteria2 极速 02",
                "protocol": "hysteria2",
                "address": "1.0.0.1",
                "port": 8443,
                "uri": "hysteria2://demopass@1.0.0.1:8443?sni=bing.com&insecure=0#HK-Hy2-02",
                "subId": "sub-main",
                "subTag": "airport_main",
                "region": "HK",
                "enabled": True,
                "latencyMs": 31,
                "alive": True,
                "checkedAt": ts,
            },
            {
                "id": "node-jp-01",
                "tag": "jp_tokyo_01",
                "name": "🇯🇵 日本 东京 BGP 01",
                "protocol": "vless",
                "address": "8.8.8.8",
                "port": 443,
                "uri": "vless://00000000-0000-0000-0000-000000000002@8.8.8.8:443?encryption=none&security=tls&sni=example.jp#JP-Tokyo-01",
                "subId": "sub-main",
                "subTag": "airport_main",
                "region": "JP",
                "enabled": True,
                "latencyMs": 54,
                "alive": True,
                "checkedAt": ts,
            },
            {
                "id": "node-sg-01",
                "tag": "sg_direct_01",
                "name": "🇸🇬 新加坡 狮城直连 01",
                "protocol": "trojan",
                "address": "8.8.4.4",
                "port": 443,
                "uri": "trojan://demopass@8.8.4.4:443?sni=example.sg#SG-Direct-01",
                "subId": "sub-main",
                "subTag": "airport_main",
                "region": "SG",
                "enabled": True,
                "latencyMs": 65,
                "alive": True,
                "checkedAt": ts,
            },
            {
                "id": "node-us-01",
                "tag": "us_gpt_01",
                "name": "🇺🇸 美国 硅谷 AI 原生解锁 01",
                "protocol": "vless",
                "address": "208.67.222.222",
                "port": 443,
                "uri": "vless://00000000-0000-0000-0000-000000000003@208.67.222.222:443?encryption=none&security=reality&sni=apple.com#US-AI-01",
                "subId": "sub-main",
                "subTag": "airport_main",
                "region": "US",
                "enabled": True,
                "latencyMs": 142,
                "alive": True,
                "checkedAt": ts,
            },
            {
                "id": "node-tw-01",
                "tag": "tw_hinet_01",
                "name": "🇹🇼 台湾 动画疯解锁 01",
                "protocol": "ss",
                "address": "9.9.9.9",
                "port": 8388,
                "uri": "ss://MjAyMi1ibGFrZTMtYWVzLTI1Ni1nY206ZGVtb2tleQ==@9.9.9.9:8388#TW-HiNet-01",
                "subId": "sub-main",
                "subTag": "airport_main",
                "region": "TW",
                "enabled": True,
                "latencyMs": 46,
                "alive": True,
                "checkedAt": ts,
            },
            {
                "id": "node-custom-01",
                "tag": "self_hosted_jp",
                "name": "🛠️ 自建日本落地 VLESS",
                "protocol": "vless",
                "address": "1.1.1.1",
                "port": 443,
                "uri": "vless://00000000-0000-0000-0000-000000000099@1.1.1.1:443?encryption=none&security=reality&sni=yahoo.co.jp#Self-JP",
                "subId": "",
                "subTag": "",
                "region": "JP",
                "enabled": True,
                "latencyMs": 49,
                "alive": True,
                "checkedAt": ts,
            },
        ],
        "groups": [
            {
                "id": "grp-proxy",
                "name": "proxy",
                "description": "主力默认出海策略组（自动优选移动平均最低延迟）",
                "enabled": True,
                "policy": "min_moving_avg",
                "fixedIndex": 0,
                "mode": "smart",
                "filter": {
                    "subTags": ["airport_main"],
                    "regions": [],
                    "includeKeywords": [],
                    "excludeKeywords": ["ExpireAt", "剩余流量", "官网"],
                    "includeRegex": "",
                    "excludeRegex": "",
                    "manualNodeTags": ["self_hosted_jp"],
                },
                "rawFilterDsl": "",
            },
            {
                "id": "grp-ai",
                "name": "ai_service",
                "description": "OpenAI / Claude / Gemini 专用组（自动排除香港节点）",
                "enabled": True,
                "policy": "min_moving_avg",
                "fixedIndex": 0,
                "mode": "smart",
                "filter": {
                    "subTags": ["airport_main"],
                    "regions": ["US", "JP", "SG", "TW"],
                    "includeKeywords": [],
                    "excludeKeywords": ["HK", "香港"],
                    "includeRegex": "",
                    "excludeRegex": "",
                    "manualNodeTags": [],
                },
                "rawFilterDsl": "",
            },
            {
                "id": "grp-media",
                "name": "streaming",
                "description": "Netflix / YouTube / Disney+ 港台日新流媒体高速组",
                "enabled": True,
                "policy": "min",
                "fixedIndex": 0,
                "mode": "smart",
                "filter": {
                    "subTags": ["airport_main"],
                    "regions": ["HK", "TW", "JP", "SG"],
                    "includeKeywords": [],
                    "excludeKeywords": [],
                    "includeRegex": "",
                    "excludeRegex": "",
                    "manualNodeTags": [],
                },
                "rawFilterDsl": "",
            },
        ],
        "dns": {
            "mode": "visual",
            "upstreams": [
                {"tag": "alidns", "url": "udp://223.5.5.5:53", "note": "阿里公共 DNS（国内极速解析）"},
                {"tag": "dnspod", "url": "udp://119.29.29.29:53", "note": "腾讯 DNSPod"},
                {"tag": "googledns", "url": "tcp+udp://8.8.8.8:53", "note": "Google 无污染海外 DNS"},
                {"tag": "cloudflare", "url": "tcp+udp://1.1.1.1:53", "note": "Cloudflare 海外 DNS"},
            ],
            "requestRules": [
                {"id": "dns-req-1", "enabled": True, "expr": "qname(geosite:category-ads-all)", "target": "reject", "comment": "DNS 级广告与追踪域名拦截"},
                {"id": "dns-req-2", "enabled": True, "expr": "qname(geosite:cn)", "target": "alidns", "comment": "国内域名使用阿里 DNS 解析"},
            ],
            "requestFallback": "googledns",
            "responseRules": [
                {"id": "dns-resp-1", "enabled": True, "expr": "upstream(googledns)", "target": "accept", "comment": "海外可信上游直接放行"},
                {"id": "dns-resp-2", "enabled": True, "expr": "ip(geoip:private) && !qname(geosite:cn)", "target": "googledns", "comment": "防 DNS 污染回退重查"},
            ],
            "responseFallback": "accept",
            "rawDsl": "",
        },
        "routing": {
            "mode": "visual",
            "fallback": "proxy",
            "rawDsl": "",
            "rules": [
                {
                    "id": "rt-1",
                    "enabled": True,
                    "name": "绕过系统核心网络进程防回环",
                    "category": "system",
                    "conditions": [{"negate": False, "field": "pname", "value": "NetworkManager, systemd-resolved, dnsmasq"}],
                    "outbound": "must_direct",
                },
                {
                    "id": "rt-2",
                    "enabled": True,
                    "name": "组播与广播地址直连",
                    "category": "system",
                    "conditions": [{"negate": False, "field": "dip", "value": "224.0.0.0/3, 'ff00::/8'"}],
                    "outbound": "direct",
                },
                {
                    "id": "rt-3",
                    "enabled": True,
                    "name": "局域网私有 IP 直连",
                    "category": "direct",
                    "conditions": [{"negate": False, "field": "dip", "value": "geoip:private"}],
                    "outbound": "direct",
                },
                {
                    "id": "rt-5",
                    "enabled": True,
                    "name": "AI 服务分流 (OpenAI / Anthropic / Gemini)",
                    "category": "ai",
                    "conditions": [{"negate": False, "field": "domain", "value": "geosite:openai, geosite:anthropic, geosite:google-gemini, keyword:claude, keyword:perplexity"}],
                    "outbound": "ai_service",
                },
                {
                    "id": "rt-6",
                    "enabled": True,
                    "name": "国际流媒体走流媒体策略组",
                    "category": "media",
                    "conditions": [{"negate": False, "field": "domain", "value": "geosite:netflix, geosite:disney, geosite:youtube, geosite:bahamut"}],
                    "outbound": "streaming",
                },
                {
                    "id": "rt-7",
                    "enabled": True,
                    "name": "中国大陆域名直连",
                    "category": "direct",
                    "conditions": [{"negate": False, "field": "domain", "value": "geosite:cn"}],
                    "outbound": "direct",
                },
                {
                    "id": "rt-8",
                    "enabled": True,
                    "name": "中国大陆目标 IP 直连",
                    "category": "direct",
                    "conditions": [{"negate": False, "field": "dip", "value": "geoip:cn"}],
                    "outbound": "direct",
                },
            ],
        },
        "snapshots": [],
    }


def render_group_filter(g: dict) -> list[str]:
    if g.get("mode") == "raw" and (g.get("rawFilterDsl") or "").strip():
        lines = []
        for ln in g["rawFilterDsl"].splitlines():
            t = ln.strip()
            if not t:
                continue
            lines.append(t if (t.startswith("filter:") or t.startswith("#")) else f"filter: {t}")
        return lines

    f = g.get("filter") or {}
    parts = []
    subs = [sanitize_ident(x) for x in (f.get("subTags") or []) if sanitize_ident(x)]
    if subs:
        parts.append(f"subtag({', '.join(subs)})")

    regs = [REGION_REGEX_MAP.get(r.upper(), esc_sq(r)) for r in (f.get("regions") or []) if r]
    if regs:
        parts.append(f"name(regex: '{'|'.join(regs)}')")

    for kw in f.get("includeKeywords") or []:
        if kw.strip():
            parts.append(f"name(keyword: '{esc_sq(kw.strip())}')")

    if (f.get("includeRegex") or "").strip():
        parts.append(f"name(regex: '{esc_sq(f['includeRegex'].strip())}')")

    for kw in f.get("excludeKeywords") or []:
        if kw.strip():
            parts.append(f"!name(keyword: '{esc_sq(kw.strip())}')")

    if (f.get("excludeRegex") or "").strip():
        parts.append(f"!name(regex: '{esc_sq(f['excludeRegex'].strip())}')")

    lines = []
    if parts:
        lines.append("filter: " + " && ".join(parts))
    manuals = [sanitize_ident(t) for t in (f.get("manualNodeTags") or []) if sanitize_ident(t)]
    if manuals:
        lines.append(f"filter: name({', '.join(manuals)})")
    return lines


def render_routing_rule_expr(r: dict) -> str:
    conds = r.get("conditions") or []
    if (r.get("rawExpr") or "").strip() and not conds:
        lhs = r["rawExpr"].strip()
    else:
        segs = []
        for c in conds:
            field = (c.get("field") or "").strip()
            val = (c.get("value") or "").strip()
            if field and val:
                segs.append(f"{'!' if c.get('negate') else ''}{field}({val})")
        lhs = " && ".join(segs)
    if not lhs:
        return ""
    return f"{lhs} -> {(r.get('outbound') or 'direct').strip()}"


def render_config_dae(st: dict) -> str:
    out = [
        "# ==============================================================================",
        "# Generated by DaeStudio (daed-next) — Modern High-Performance dae Control Plane",
        "# Validate syntax anytime with: dae validate -c /etc/dae/config.dae",
        "# ==============================================================================",
        "",
        "global {",
    ]
    g = st.get("global") or {}
    if (g.get("wanInterface") or "").strip():
        out.append(f"    wan_interface: {g['wanInterface'].strip()}")
    lans = [x.strip() for x in (g.get("lanInterfaces") or []) if x.strip()]
    if lans:
        out.append(f"    lan_interface: {','.join(lans)}")
    if g.get("tproxyPort"):
        out.append(f"    tproxy_port: {int(g['tproxyPort'])}")
    out.append(f"    tproxy_port_protect: {'true' if g.get('tproxyPortProtect', True) else 'false'}")
    if g.get("logLevel"):
        out.append(f"    log_level: {g['logLevel']}")
    if g.get("dialMode"):
        out.append(f"    dial_mode: {g['dialMode']}")
    out.append(f"    allow_insecure: {'true' if g.get('allowInsecure') else 'false'}")
    out.append(f"    auto_config_kernel_parameter: {'true' if g.get('autoConfigKernelParameter', True) else 'false'}")
    for key, dae_k, quoted in [
        ("checkInterval", "check_interval", False),
        ("checkTolerance", "check_tolerance", False),
        ("tcpCheckUrl", "tcp_check_url", True),
        ("tcpCheckHttpMethod", "tcp_check_http_method", False),
        ("udpCheckDns", "udp_check_dns", True),
        ("sniffingTimeout", "sniffing_timeout", False),
        ("tlsImplementation", "tls_implementation", False),
    ]:
        val = (g.get(key) or "").strip()
        if val:
            out.append(f"    {dae_k}: '{esc_sq(val)}'" if quoted else f"    {dae_k}: {val}")
    if g.get("tlsImplementation") == "utls" and g.get("utlsImitate"):
        out.append(f"    utls_imitate: {g['utlsImitate']}")
    if g.get("mptcp"):
        out.append("    mptcp: true")
    out.extend(["}", "", "subscription {"])

    converted_sub_ids = set()
    for sub in st.get("subscriptions") or []:
        if not sub.get("enabled", True):
            continue
        if sub.get("persistConverted"):
            converted_sub_ids.add(sub.get("id"))
            continue
        tag = sanitize_ident(sub.get("tag", ""))
        url = (sub.get("url") or "").strip()
        if tag and url:
            if sub.get("name"):
                out.append(f"    # {sub['name']}")
            out.append(f"    {tag}: '{esc_sq(url)}'")
    out.extend(["}", "", "node {"])

    for n in st.get("nodes") or []:
        if not n.get("enabled", True) or not (n.get("uri") or "").strip():
            continue
        if not n.get("subId") or n.get("subId") in converted_sub_ids:
            tag = sanitize_ident(n.get("tag") or n.get("id"))
            out.append(f"    {tag}: '{esc_sq(n['uri'].strip())}'")
    out.extend(["}", "", "dns {"])

    dns = st.get("dns") or {}
    if dns.get("mode") == "dsl" and (dns.get("rawDsl") or "").strip():
        for ln in dns["rawDsl"].strip().splitlines():
            out.append("    " + ln)
    else:
        out.append("    upstream {")
        for up in dns.get("upstreams") or []:
            tag = sanitize_ident(up.get("tag", ""))
            url = (up.get("url") or "").strip()
            if tag and url:
                if up.get("note"):
                    out.append(f"        # {up['note']}")
                out.append(f"        {tag}: '{esc_sq(url)}'")
        out.append("    }")
        out.append("    routing {")
        out.append("        request {")
        for r in dns.get("requestRules") or []:
            if r.get("enabled", True) and r.get("expr"):
                if r.get("comment"):
                    out.append(f"            # {r['comment']}")
                out.append(f"            {r['expr'].strip()} -> {r.get('target', 'googledns').strip()}")
        out.append(f"            fallback: {dns.get('requestFallback') or 'googledns'}")
        out.append("        }")
        out.append("        response {")
        for r in dns.get("responseRules") or []:
            if r.get("enabled", True) and r.get("expr"):
                if r.get("comment"):
                    out.append(f"            # {r['comment']}")
                out.append(f"            {r['expr'].strip()} -> {r.get('target', 'accept').strip()}")
        out.append(f"            fallback: {dns.get('responseFallback') or 'accept'}")
        out.append("        }")
        out.append("    }")
    out.extend(["}", "", "group {"])

    for grp in st.get("groups") or []:
        if not grp.get("enabled", True):
            continue
        name = sanitize_ident(grp.get("name", ""))
        if not name:
            continue
        if grp.get("description"):
            out.append(f"    # {grp['description']}")
        out.append(f"    {name} {{")
        for fl in render_group_filter(grp):
            out.append("        " + fl)
        pol = (grp.get("policy") or "min_moving_avg").strip()
        if pol == "fixed":
            pol = f"fixed({int(grp.get('fixedIndex') or 0)})"
        out.append(f"        policy: {pol}")
        if (grp.get("tcpCheckUrl") or "").strip():
            out.append(f"        tcp_check_url: '{esc_sq(grp['tcpCheckUrl'].strip())}'")
        out.append("    }")
    out.extend(["}", "", "routing {"])

    rt = st.get("routing") or {}
    if rt.get("mode") == "dsl" and (rt.get("rawDsl") or "").strip():
        for ln in rt["rawDsl"].strip().splitlines():
            out.append("    " + ln)
    else:
        for r in rt.get("rules") or []:
            expr = render_routing_rule_expr(r)
            if not expr:
                continue
            if r.get("name"):
                out.append(f"    # {r['name']}")
            out.append(f"    {expr}" if r.get("enabled", True) else f"    # [Disabled] {expr}")
        out.append("")
        out.append(f"    fallback: {(rt.get('fallback') or 'direct').strip()}")
    out.append("}")
    return "\n".join(out) + "\n"


def validate_syntax(cfg: str) -> list[str]:
    errs = []
    depth = 0
    in_sq = False
    esc = False
    for line_no, raw in enumerate(cfg.splitlines(), start=1):
        for ch in raw:
            if esc:
                esc = False
                continue
            if ch == "\\" and in_sq:
                esc = True
                continue
            if not in_sq and ch == "#":
                break
            if ch == "'":
                in_sq = not in_sq
            elif not in_sq:
                if ch == "{":
                    depth += 1
                elif ch == "}":
                    depth -= 1
                    if depth < 0:
                        errs.append(f"第 {line_no} 行存在多余的右花括号 '}}'")
                        depth = 0
        if in_sq:
            errs.append(f"第 {line_no} 行单引号未闭合")
            in_sq = False
    if depth != 0:
        errs.append(f"花括号层级未闭合（差值 {depth}）")
    if "routing" not in cfg:
        errs.append("缺少必选的 routing { ... } 路由块")
    return errs


COND_RE = re.compile(r"^(!?)([a-zA-Z0-9_]+)\((.*)\)$")


def parse_routing_dsl(dsl: str) -> tuple[list[dict], str]:
    rules = []
    fallback = "direct"
    comment = ""
    for idx, raw in enumerate(dsl.splitlines(), start=1):
        t = raw.strip()
        if not t:
            continue
        if t.startswith("#"):
            c = t[1:].strip()
            if c.startswith("[Disabled]"):
                r = _parse_single_rt_line(c[len("[Disabled]"):].strip(), comment, idx, False)
                if r:
                    rules.append(r)
                    comment = ""
                    continue
            comment = c
            continue
        if t.startswith("fallback:"):
            fallback = t.split(":", 1)[1].strip()
            comment = ""
            continue
        r = _parse_single_rt_line(t, comment, idx, True)
        if r:
            rules.append(r)
        comment = ""
    return rules, fallback


def _parse_single_rt_line(line: str, comment: str, idx: int, enabled: bool):
    if "->" not in line:
        return None
    lhs, ob = [x.strip() for x in line.split("->", 1)]
    if not lhs or not ob:
        return None
    conds = []
    all_ok = True
    for seg in lhs.split("&&"):
        m = COND_RE.match(seg.strip())
        if m:
            conds.append({"negate": m.group(1) == "!", "field": m.group(2), "value": m.group(3).strip()})
        else:
            all_ok = False
            break
    rule = {
        "id": f"rt-imp-{int(time.time() * 1000) % 100000}-{idx}",
        "enabled": enabled,
        "name": comment or f"规则 #{idx} ({ob})",
        "category": "custom",
        "conditions": conds if (all_ok and conds) else [],
        "rawExpr": "" if (all_ok and conds) else lhs,
        "outbound": ob,
    }
    return rule


def extract_top_blocks(src: str) -> dict[str, str]:
    blocks = {}
    i, n = 0, len(src)
    while i < n:
        while i < n and src[i] in " \t\r\n":
            i += 1
        if i < n and src[i] == "#":
            while i < n and src[i] != "\n":
                i += 1
            continue
        start = i
        while i < n and (src[i].isalnum() or src[i] in "_-"):
            i += 1
        name = src[start:i].strip()
        while i < n and src[i] in " \t\r\n":
            i += 1
        if not name or i >= n or src[i] != "{":
            while i < n and src[i] != "\n":
                i += 1
            continue
        i += 1
        body_start = i
        depth = 1
        in_sq = False
        while i < n and depth > 0:
            ch = src[i]
            if ch == "\\" and in_sq and i + 1 < n:
                i += 2
                continue
            if not in_sq and ch == "#":
                while i < n and src[i] != "\n":
                    i += 1
                continue
            if ch == "'":
                in_sq = not in_sq
            elif not in_sq:
                if ch == "{":
                    depth += 1
                elif ch == "}":
                    depth -= 1
                    if depth == 0:
                        blocks[name] = src[body_start:i]
                        i += 1
                        break
            i += 1
    return blocks


def parse_dns_dsl(raw: str) -> dict:
    body = (raw or "").strip()
    top = extract_top_blocks(body)
    if "dns" in top and "upstream" not in top and "routing" not in top:
        body = top["dns"].strip()
        top = extract_top_blocks(body)

    dns_cfg = {
        "mode": "visual",
        "bind": "",
        "ipVersionPrefer": 0,
        "upstreams": [],
        "requestRules": [],
        "requestFallback": "",
        "responseRules": [],
        "responseFallback": "accept",
        "rawDsl": body,
    }
    depth = 0
    for line in body.splitlines():
        trimmed = line.strip()
        if not trimmed or trimmed.startswith("#"):
            continue
        if depth == 0:
            if trimmed.startswith("bind:"):
                dns_cfg["bind"] = trimmed.split(":", 1)[1].strip().strip("'\"")
            elif trimmed.startswith("ipversion_prefer:"):
                try:
                    v = int(trimmed.split(":", 1)[1].strip().strip("'\""))
                    if v in (4, 6):
                        dns_cfg["ipVersionPrefer"] = v
                except ValueError:
                    pass
        depth += trimmed.count("{") - trimmed.count("}")

    if "upstream" in top:
        pending_note = ""
        for line in top["upstream"].splitlines():
            t = line.strip()
            if not t:
                pending_note = ""
                continue
            if t.startswith("#"):
                pending_note = t.lstrip("#").strip()
                continue
            if ":" in t:
                k, v = t.split(":", 1)
                tag = sanitize_ident(k.strip())
                url_val = v.split("#", 1)[0].strip().strip("'\"")
                if tag and url_val:
                    dns_cfg["upstreams"].append({"tag": tag, "url": url_val, "note": pending_note})
                pending_note = ""

    def _parse_dns_rules(block_src: str, prefix: str, default_fb: str):
        rules = []
        fb = default_fb
        comment = ""
        seq = 0
        for line in (block_src or "").splitlines():
            t = line.strip()
            if not t:
                comment = ""
                continue
            enabled = True
            if re.match(r"^#\s*\[Disabled\]", t, re.IGNORECASE):
                enabled = False
                t = re.sub(r"^#\s*\[Disabled\]\s*", "", t, flags=re.IGNORECASE).strip()
            elif t.startswith("#"):
                comment = t.lstrip("#").strip()
                continue
            if t.startswith("fallback:"):
                val = t.split(":", 1)[1].split("#", 1)[0].strip()
                if val:
                    fb = val
                comment = ""
                continue
            if "->" in t:
                lhs, rhs = t.split("->", 1)
                expr = lhs.strip()
                target = rhs.split("#", 1)[0].strip()
                if expr and target:
                    seq += 1
                    rules.append({
                        "id": f"{prefix}-{seq}",
                        "enabled": enabled,
                        "expr": expr,
                        "target": target,
                        "comment": comment,
                    })
                comment = ""
        return rules, fb

    if "routing" in top:
        rt_blocks = extract_top_blocks(top["routing"])
        if "request" in rt_blocks:
            reqs, req_fb = _parse_dns_rules(rt_blocks["request"], "dns-req", dns_cfg["upstreams"][0]["tag"] if dns_cfg["upstreams"] else "alidns")
            dns_cfg["requestRules"] = reqs
            dns_cfg["requestFallback"] = req_fb
        if "response" in rt_blocks:
            resps, resp_fb = _parse_dns_rules(rt_blocks["response"], "dns-resp", "accept")
            dns_cfg["responseRules"] = resps
            dns_cfg["responseFallback"] = resp_fb

    if not dns_cfg["upstreams"]:
        dns_cfg["mode"] = "dsl"
        return dns_cfg
    if not dns_cfg["requestFallback"]:
        dns_cfg["requestFallback"] = dns_cfg["upstreams"][0]["tag"]
    return dns_cfg


def parse_config_dae(cfg: str, base_state: dict) -> dict:
    errs = validate_syntax(cfg)
    if errs:
        raise ValueError("配置文件语法错误: " + "; ".join(errs))
    blocks = extract_top_blocks(cfg)
    st = json.loads(json.dumps(base_state))
    st["updatedAt"] = now_iso()

    if "routing" in blocks:
        rules, fb = parse_routing_dsl(blocks["routing"])
        st["routing"]["mode"] = "visual"
        st["routing"]["rules"] = rules
        st["routing"]["fallback"] = fb
        st["routing"]["rawDsl"] = blocks["routing"].strip()
    if "dns" in blocks:
        st["dns"]["mode"] = "dsl"
        st["dns"]["rawDsl"] = blocks["dns"].strip()
    if "group" in blocks:
        gblocks = extract_top_blocks(blocks["group"])
        if gblocks:
            new_groups = []
            for idx, (gname, gbody) in enumerate(gblocks.items(), start=1):
                filters = []
                pol = "min_moving_avg"
                for ln in gbody.splitlines():
                    t = ln.strip()
                    if t.startswith("filter:"):
                        filters.append(t)
                    elif t.startswith("policy:"):
                        p = t.split(":", 1)[1].strip()
                        pol = "fixed" if p.startswith("fixed(") else p
                new_groups.append(
                    {
                        "id": f"grp-imp-{idx}",
                        "name": gname,
                        "description": "",
                        "enabled": True,
                        "policy": pol,
                        "fixedIndex": 0,
                        "mode": "raw",
                        "filter": {},
                        "rawFilterDsl": "\n".join(filters),
                    }
                )
            st["groups"] = new_groups
    return st


def decode_b64_flex(s: str) -> str:
    clean = re.sub(r"\s+", "", s)
    clean += "=" * ((4 - len(clean) % 4) % 4)
    for alt in (None, b"-_"):
        try:
            raw = base64.b64decode(clean, altchars=alt) if alt else base64.b64decode(clean)
            return raw.decode("utf-8", errors="ignore")
        except Exception:
            pass
    raise ValueError("invalid base64")


def detect_region(name: str) -> str:
    for code, pat in REGION_REGEX_MAP.items():
        if re.search(pat, name or "", re.IGNORECASE):
            return code
    return "OTHER"


def parse_node_uri(uri_str: str, sub_id: str, sub_tag: str, seq: int):
    uri_str = uri_str.strip()
    if "://" not in uri_str:
        return None
    scheme, rest = uri_str.split("://", 1)
    scheme = scheme.lower()
    if scheme == "hy2":
        scheme = "hysteria2"

    if scheme == "vmess":
        try:
            vm = json.loads(decode_b64_flex(rest))
            name = str(vm.get("ps") or f"VMess-{seq}")
            host = str(vm.get("add") or "")
            port = int(vm.get("port") or 443)
            if not host:
                return None
            return {
                "id": f"node-{int(time.time()*1000)%1000000}-{seq}",
                "tag": sanitize_ident(f"{name}_{seq}".lower()),
                "name": name,
                "protocol": "vmess",
                "address": host,
                "port": port,
                "uri": uri_str,
                "subId": sub_id,
                "subTag": sub_tag,
                "region": detect_region(name),
                "enabled": True,
                "latencyMs": -1,
                "alive": False,
            }
        except Exception:
            return None

    parsed = urllib.parse.urlsplit(uri_str)
    host = parsed.hostname or ""
    port = parsed.port or 443
    name = urllib.parse.unquote(parsed.fragment or "") or f"{scheme.upper()}-{host}:{port}"
    if not host:
        return None
    return {
        "id": f"node-{int(time.time()*1000)%1000000}-{seq}",
        "tag": sanitize_ident(f"{name}_{seq}".lower()),
        "name": name,
        "protocol": scheme,
        "address": host,
        "port": port,
        "uri": uri_str,
        "subId": sub_id,
        "subTag": sub_tag,
        "region": detect_region(name),
        "enabled": True,
        "latencyMs": -1,
        "alive": False,
    }


def parse_clash_yaml(raw: str, sub_id: str, sub_tag: str) -> list[dict]:
    items = []
    in_proxies = False
    cur = None
    for line in raw.splitlines():
        t = line.strip()
        if not t or t.startswith("#"):
            continue
        if not line.startswith((" ", "\t")) and not t.startswith("-"):
            if t.startswith("proxies:"):
                in_proxies = True
                continue
            if in_proxies:
                break
        if not in_proxies:
            continue
        if t.startswith("-"):
            if cur:
                items.append(cur)
                cur = None
            rest = t[1:].strip()
            if rest.startswith("{") and rest.endswith("}"):
                d = {}
                for part in rest[1:-1].split(","):
                    if ":" in part:
                        k, v = part.split(":", 1)
                        d[k.strip()] = v.strip().strip("'\"")
                items.append(d)
            else:
                cur = {}
                if ":" in rest:
                    k, v = rest.split(":", 1)
                    cur[k.strip()] = v.strip().strip("'\"")
        elif cur is not None and ":" in t:
            k, v = t.split(":", 1)
            cur[k.strip()] = v.strip().strip("'\"")
    if cur:
        items.append(cur)

    nodes = []
    for idx, p in enumerate(items, start=1):
        tp = (p.get("type") or "").lower()
        srv = p.get("server") or ""
        prt = p.get("port") or "443"
        nm = urllib.parse.quote(p.get("name") or f"{tp}-{idx}")
        sni = p.get("servername") or p.get("sni") or ""
        uri = ""
        if not srv:
            continue
        if tp == "vless":
            uri = f"vless://{p.get('uuid','')}@{srv}:{prt}?encryption=none&security={'tls' if p.get('tls')=='true' else 'none'}&sni={sni}#{nm}"
        elif tp == "hysteria2":
            uri = f"hysteria2://{urllib.parse.quote(p.get('password',''))}@{srv}:{prt}?sni={sni}#{nm}"
        elif tp == "trojan":
            uri = f"trojan://{urllib.parse.quote(p.get('password',''))}@{srv}:{prt}?sni={sni}#{nm}"
        elif tp == "ss":
            cred = base64.urlsafe_b64encode(f"{p.get('cipher','aes-256-gcm')}:{p.get('password','')}".encode()).decode().rstrip("=")
            uri = f"ss://{cred}@{srv}:{prt}#{nm}"
        if uri:
            n = parse_node_uri(uri, sub_id, sub_tag, idx)
            if n:
                nodes.append(n)
    return nodes


def parse_singbox_json(raw: str, sub_id: str, sub_tag: str) -> list[dict]:
    doc = json.loads(raw)
    nodes = []
    for idx, ob in enumerate(doc.get("outbounds") or [], start=1):
        tp = (ob.get("type") or "").lower()
        srv = ob.get("server") or ""
        prt = ob.get("server_port") or 443
        nm = urllib.parse.quote(ob.get("tag") or f"sb-{idx}")
        sni = ((ob.get("tls") or {}).get("server_name")) or ""
        uri = ""
        if not srv:
            continue
        if tp == "shadowsocks":
            cred = base64.urlsafe_b64encode(f"{ob.get('method','aes-256-gcm')}:{ob.get('password','')}".encode()).decode().rstrip("=")
            uri = f"ss://{cred}@{srv}:{prt}#{nm}"
        elif tp == "vless":
            uri = f"vless://{ob.get('uuid','')}@{srv}:{prt}?encryption=none&security=tls&sni={sni}#{nm}"
        elif tp == "trojan":
            uri = f"trojan://{urllib.parse.quote(ob.get('password',''))}@{srv}:{prt}?sni={sni}#{nm}"
        elif tp == "hysteria2":
            uri = f"hysteria2://{urllib.parse.quote(ob.get('password',''))}@{srv}:{prt}?sni={sni}#{nm}"
        if uri:
            n = parse_node_uri(uri, sub_id, sub_tag, idx)
            if n:
                nodes.append(n)
    return nodes


def parse_subscription_any(raw: str, sub_id: str, sub_tag: str, exclude_re_str: str) -> tuple[list[dict], str]:
    txt = (raw or "").strip()
    if not txt:
        raise ValueError("订阅内容为空")
    exc_re = re.compile(exclude_re_str) if (exclude_re_str or "").strip() else None

    def _flt(arr):
        return [n for n in arr if not (exc_re and exc_re.search(n["name"]))]

    if txt.startswith("{"):
        try:
            sb_nodes = parse_singbox_json(txt, sub_id, sub_tag)
            if sb_nodes:
                return _flt(sb_nodes), "singbox"
        except Exception:
            pass

    if "proxies:" in txt:
        cl_nodes = parse_clash_yaml(txt, sub_id, sub_tag)
        if cl_nodes:
            return _flt(cl_nodes), "clash"

    fmt = "uri"
    decoded = txt
    if "://" not in txt:
        try:
            b = decode_b64_flex(txt)
            if "://" in b:
                decoded = b
                fmt = "base64"
        except Exception:
            pass

    nodes = []
    for idx, line in enumerate(decoded.splitlines(), start=1):
        n = parse_node_uri(line, sub_id, sub_tag, idx)
        if n:
            nodes.append(n)
    if not nodes:
        raise ValueError("未识别到有效节点（支持 Base64 / URI / Clash YAML / Sing-box JSON）")
    return _flt(nodes), fmt


def probe_single_node(node: dict, timeout_sec: float = 2.0) -> dict:
    host = node.get("address") or ""
    port = int(node.get("port") or 0)
    checked_at = now_iso()
    if not host or port <= 0:
        return {"id": node["id"], "latencyMs": 0, "alive": False, "checkedAt": checked_at, "error": "invalid address"}
    t0 = time.perf_counter()
    try:
        with socket.create_connection((host, port), timeout=timeout_sec):
            ms = max(1, int((time.perf_counter() - t0) * 1000))
            return {"id": node["id"], "latencyMs": ms, "alive": True, "checkedAt": checked_at}
    except Exception as exc:
        return {"id": node["id"], "latencyMs": 0, "alive": False, "checkedAt": checked_at, "error": str(exc)}


class AppContext:
    def __init__(self, data_dir: str, config_path: str):
        self.lock = threading.Lock()
        self.data_dir = data_dir
        self.state_path = os.path.join(data_dir, "state.json")
        self.config_path = config_path
        self.running = True
        self.started_at = time.time()
        self.tick = 0
        self.total_rx = 0
        self.total_tx = 0
        self.sessions = {}
        self.logs = [
            {"time": now_hms(), "level": "info", "message": "DaeStudio 已启动 (本地模拟预览守护引擎就绪)"},
            {"time": now_hms(), "level": "info", "message": "eBPF TC / TPROXY mock hooks initialized (wan=auto, lan=br-lan)"},
        ]
        self.state = self._load_or_init()

    def _load_or_init(self) -> dict:
        os.makedirs(self.data_dir, exist_ok=True)
        if os.path.exists(self.state_path):
            try:
                with open(self.state_path, "r", encoding="utf-8") as f:
                    st = json.load(f)
                    if st.get("version"):
                        return st
            except Exception:
                pass
        st = default_state()
        self._save_unlocked(st)
        self._write_cfg_unlocked(render_config_dae(st))
        return st

    def _save_unlocked(self, st: dict):
        st["updatedAt"] = now_iso()
        os.makedirs(os.path.dirname(self.state_path), exist_ok=True)
        with open(self.state_path, "w", encoding="utf-8") as f:
            json.dump(st, f, ensure_ascii=False, indent=2)

    def _write_cfg_unlocked(self, cfg: str):
        os.makedirs(os.path.dirname(self.config_path), exist_ok=True)
        with open(self.config_path, "w", encoding="utf-8") as f:
            f.write(cfg)
        try:
            os.chmod(self.config_path, 0o600)
        except Exception:
            pass

    def log(self, level: str, msg: str):
        self.logs.append({"time": now_hms(), "level": level, "message": msg})
        if len(self.logs) > 300:
            self.logs = self.logs[-300:]

    def stats(self) -> dict:
        with self.lock:
            self.tick += 1
            if self.running:
                t = float(self.tick)
                down = max(35_000, int(860_000 + 620_000 * math.sin(t * 0.45) + 270_000 * math.cos(t * 1.1)))
                up = max(14_000, int(195_000 + 105_000 * math.cos(t * 0.55)))
                self.total_rx += down
                self.total_tx += up
                uptime = int(time.time() - self.started_at)
                conns = 42 + (self.tick % 17)
            else:
                down, up, uptime, conns = 0, 0, 0, 0
            ep_traffic = {}
            if self.running:
                nodes = [n for n in (self.state.get("nodes") or []) if n.get("enabled", True) and n.get("address") and n.get("port")]
                if nodes:
                    first = nodes[0]
                    ep_key = f"{str(first['address']).strip('[]').lower()}:{first['port']}"
                    ep_traffic[ep_key] = {
                        "endpoint": ep_key,
                        "uploadBps": up,
                        "downloadBps": down,
                        "totalUpload": self.total_tx,
                        "totalDownload": self.total_rx,
                        "activeConns": 6,
                    }
            return {
                "running": self.running,
                "mockMode": platform.system().lower() != "linux" or shutil.which("dae") is None,
                "pid": os.getpid() if self.running else 0,
                "uptimeSeconds": uptime,
                "uploadBps": up,
                "downloadBps": down,
                "totalUpload": self.total_tx,
                "totalDownload": self.total_rx,
                "memoryMb": 22.4,
                "activeConns": conns,
                "configPath": self.config_path,
                "lastReloadedAt": now_iso(),
                "endpointTraffic": ep_traffic,
            }

    def doctor(self) -> dict:
        sys_os = platform.system().lower()
        arch = platform.machine().lower()
        kern = platform.release()
        return {
            "os": sys_os,
            "arch": arch,
            "kernel": kern,
            "mockMode": sys_os != "linux",
            "readyForDae": True,
            "daeBinaryFound": shutil.which("dae") is not None,
            "daeVersion": "dae v0.9.0 (Mock Preview Engine)",
            "checks": [
                {
                    "id": "os-mode",
                    "title": "运行环境与守护引擎",
                    "status": "info" if sys_os != "linux" else "pass",
                    "current": f"{sys_os}/{arch} ({'本地模拟预览引擎' if sys_os != 'linux' else 'Linux Native'})",
                    "requirement": "生产环境需 Linux >= 5.17 开启 eBPF & BTF",
                    "remediation": "当前支持全量可视化编排、多格式订阅转换、config.dae 双向编译与真实 TCP 节点延迟测速。",
                },
                {
                    "id": "btf",
                    "title": "内核 BTF 调试符号 (/sys/kernel/btf/vmlinux)",
                    "status": "pass",
                    "current": "CO-RE BTF Ready",
                    "requirement": "CONFIG_DEBUG_INFO_BTF=y",
                },
                {
                    "id": "bpffs",
                    "title": "BPF 文件系统 (/sys/fs/bpf)",
                    "status": "pass",
                    "current": "Mounted (bpffs)",
                    "requirement": "mount -t bpf bpf /sys/fs/bpf",
                },
                {
                    "id": "ipfwd",
                    "title": "IPv4 / IPv6 流量透明转发",
                    "status": "pass",
                    "current": "auto_config_kernel_parameter: true",
                    "requirement": "net.ipv4.ip_forward = 1",
                },
            ],
            "interfaces": [
                {"name": "en0", "ips": ["192.168.1.10/24"], "flags": "up|broadcast"},
                {"name": "br-lan", "ips": ["192.168.1.1/24"], "flags": "up|broadcast"},
                {"name": "eth0", "ips": ["10.0.0.2/24"], "flags": "up|broadcast"},
            ],
        }


class DaeStudioHandler(SimpleHTTPRequestHandler):
    ctx: AppContext = None

    def __init__(self, *args, **kwargs):
        super().__init__(*args, directory=WEB_DIR, **kwargs)

    def log_message(self, fmt, *args):
        return

    def _send_json(self, code: int, payload: dict):
        body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Cache-Control", "no-store")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _read_json(self) -> dict:
        length = int(self.headers.get("Content-Length") or 0)
        if length <= 0:
            return {}
        return json.loads(self.rfile.read(length).decode("utf-8"))

    def _check_auth(self) -> bool:
        with self.ctx.lock:
            enabled = bool((self.ctx.state.get("auth") or {}).get("enabled"))
            auth_hdr = self.headers.get("Authorization") or ""
            tok = auth_hdr.replace("Bearer ", "").strip()
            valid = tok in self.ctx.sessions
        if enabled and not valid:
            self._send_json(401, {"error": "需要登录认证", "authEnabled": True})
            return False
        return True

    def do_GET(self):
        path = urllib.parse.urlsplit(self.path).path
        if path == "/api/v1/bootstrap":
            if not self._check_auth():
                return
            with self.ctx.lock:
                st = json.loads(json.dumps(self.ctx.state))
                logs = list(self.ctx.logs)
            geo_cfg = st.get("geoData") or {
                "preset": "loyalsoldier",
                "autoUpdate": False,
                "scheduleMode": "daily",
                "scheduleTime": "04:00",
                "scheduleWeekday": 1,
                "intervalDays": 3,
            }
            ts_iso = now_iso()
            geo_status = {
                "dir": "/usr/share/dae (Mock)",
                "updating": False,
                "geoip": {"name": "geoip.dat", "path": "/usr/share/dae/geoip.dat", "exists": True, "sizeBytes": 23876117, "modified": ts_iso, "hasBackup": True},
                "geosite": {"name": "geosite.dat", "path": "/usr/share/dae/geosite.dat", "exists": True, "sizeBytes": 2415689, "modified": ts_iso, "hasBackup": True},
                "config": geo_cfg,
                "nextRunAt": "",
            }
            self._send_json(
                200,
                {
                    "state": st,
                    "panelVersion": "v1.0.3",
                    "updateRepo": st.get("updateRepo") or os.environ.get("DAED_NEXT_REPO", ""),
                    "geoDataStatus": geo_status,
                    "configDae": render_config_dae(st),
                    "stats": self.ctx.stats(),
                    "doctor": self.ctx.doctor(),
                    "logs": logs,
                },
            )
            return
        if path == "/api/v1/geodata":
            if not self._check_auth():
                return
            with self.ctx.lock:
                geo_cfg = dict(self.ctx.state.get("geoData") or {"preset": "loyalsoldier", "autoUpdate": False, "scheduleMode": "daily", "scheduleTime": "04:00", "scheduleWeekday": 1, "intervalDays": 3})
            ts_iso = now_iso()
            self._send_json(200, {
                "dir": "/usr/share/dae (Mock)",
                "updating": False,
                "geoip": {"name": "geoip.dat", "path": "/usr/share/dae/geoip.dat", "exists": True, "sizeBytes": 23876117, "modified": ts_iso, "hasBackup": True},
                "geosite": {"name": "geosite.dat", "path": "/usr/share/dae/geosite.dat", "exists": True, "sizeBytes": 2415689, "modified": ts_iso, "hasBackup": True},
                "config": geo_cfg,
            })
            return
        if path == "/api/v1/panel/update":
            if not self._check_auth():
                return
            with self.ctx.lock:
                repo = (self.ctx.state.get("updateRepo") or os.environ.get("DAED_NEXT_REPO") or "").strip()
            q = urllib.parse.parse_qs(urllib.parse.urlsplit(self.path).query)
            if q.get("repo"):
                repo = q["repo"][0].strip()
            if not repo:
                self._send_json(400, {"error": "请先填写面板 GitHub 仓库地址（格式：owner/repo）", "current": "v1.0.3", "needRepoConfig": True})
                return
            self._send_json(200, {"current": "v1.0.3", "latest": "v1.0.3", "repo": repo, "url": f"https://github.com/{repo}/releases", "available": False, "installable": False})
            return
        if path == "/api/v1/stats":
            if not self._check_auth():
                return
            with self.ctx.lock:
                logs = list(self.ctx.logs)
            self._send_json(200, {"stats": self.ctx.stats(), "logs": logs})
            return
        if path == "/api/v1/doctor":
            if not self._check_auth():
                return
            self._send_json(200, self.ctx.doctor())
            return
        return super().do_GET()

    def do_POST(self):
        path = urllib.parse.urlsplit(self.path).path
        req = self._read_json()

        if path == "/api/v1/auth/login":
            with self.ctx.lock:
                auth = self.ctx.state.get("auth") or {}
                if auth.get("enabled"):
                    pw_hash = hashlib.sha256((req.get("password") or "").encode()).hexdigest()
                    if req.get("username") != auth.get("username") or pw_hash != auth.get("passwordHash"):
                        self._send_json(401, {"ok": False, "error": "用户名或密码错误"})
                        return
                tok = secrets.token_hex(24)
                self.ctx.sessions[tok] = time.time()
            self._send_json(200, {"ok": True, "token": tok})
            return

        if not self._check_auth():
            return

        if path == "/api/v1/auth/update":
            with self.ctx.lock:
                auth = self.ctx.state.setdefault("auth", {})
                auth["enabled"] = bool(req.get("enabled"))
                if (req.get("username") or "").strip():
                    auth["username"] = req["username"].strip()
                if req.get("password"):
                    auth["passwordHash"] = hashlib.sha256(req["password"].encode()).hexdigest()
                self.ctx._save_unlocked(self.ctx.state)
                tok = secrets.token_hex(24)
                self.ctx.sessions[tok] = time.time()
                resp_auth = dict(auth)
            self._send_json(200, {"ok": True, "token": tok, "auth": resp_auth})
            return

        if path == "/api/v1/geodata/config":
            with self.ctx.lock:
                prev = dict(self.ctx.state.get("geoData") or {})
                prev.update({
                    "preset": req.get("preset") or "loyalsoldier",
                    "geoipUrl": req.get("geoipUrl") or "",
                    "geositeUrl": req.get("geositeUrl") or "",
                    "geoipSumUrl": req.get("geoipSumUrl") or "",
                    "geositeSumUrl": req.get("geositeSumUrl") or "",
                    "autoUpdate": bool(req.get("autoUpdate")),
                    "scheduleMode": req.get("scheduleMode") or "daily",
                    "scheduleTime": req.get("scheduleTime") or "04:00",
                    "scheduleWeekday": int(req.get("scheduleWeekday") or 0),
                    "intervalDays": int(req.get("intervalDays") or 3),
                })
                self.ctx.state["geoData"] = prev
                self.ctx._save_unlocked(self.ctx.state)
            ts_iso = now_iso()
            self._send_json(200, {
                "ok": True,
                "geoData": prev,
                "geoDataStatus": {
                    "dir": "/usr/share/dae (Mock)",
                    "updating": False,
                    "geoip": {"name": "geoip.dat", "path": "/usr/share/dae/geoip.dat", "exists": True, "sizeBytes": 23876117, "modified": ts_iso, "hasBackup": True},
                    "geosite": {"name": "geosite.dat", "path": "/usr/share/dae/geosite.dat", "exists": True, "sizeBytes": 2415689, "modified": ts_iso, "hasBackup": True},
                    "config": prev,
                },
            })
            return

        if path == "/api/v1/geodata/update":
            ts_iso = now_iso()
            with self.ctx.lock:
                prev = dict(self.ctx.state.get("geoData") or {"preset": "loyalsoldier", "scheduleMode": "daily", "scheduleTime": "04:00"})
                prev["lastUpdatedAt"] = ts_iso
                prev["lastStatus"] = "ok"
                prev["lastMessage"] = "已完成分流数据库更新 (预览模式)"
                self.ctx.state["geoData"] = prev
                self.ctx._save_unlocked(self.ctx.state)
                self.ctx.log("info", prev["lastMessage"])
            self._send_json(200, {
                "ok": True,
                "message": prev["lastMessage"],
                "geoDataStatus": {
                    "dir": "/usr/share/dae (Mock)",
                    "updating": False,
                    "geoip": {"name": "geoip.dat", "path": "/usr/share/dae/geoip.dat", "exists": True, "sizeBytes": 23876117, "modified": ts_iso, "hasBackup": True},
                    "geosite": {"name": "geosite.dat", "path": "/usr/share/dae/geosite.dat", "exists": True, "sizeBytes": 2415689, "modified": ts_iso, "hasBackup": True},
                    "config": prev,
                },
            })
            return

        if path == "/api/v1/state":
            new_st = req.get("state") or {}
            apply_reload = bool(req.get("apply"))
            with self.ctx.lock:
                old_hash = (self.ctx.state.get("auth") or {}).get("passwordHash", "")
                new_st.setdefault("auth", {})["passwordHash"] = old_hash
                self.ctx.state = new_st
                self.ctx._save_unlocked(self.ctx.state)
                cfg = render_config_dae(self.ctx.state)
                errs = validate_syntax(cfg)
                valid = len(errs) == 0
                if valid:
                    self.ctx._write_cfg_unlocked(cfg)
                    if apply_reload:
                        self.ctx.running = True
                        self.ctx.log("info", "配置已写入 config.dae 并完成 dae reload 零断流热重载")
                else:
                    self.ctx.log("error", "配置校验失败: " + "; ".join(errs))
                st_copy = json.loads(json.dumps(self.ctx.state))
            self._send_json(
                200,
                {
                    "ok": valid,
                    "valid": valid,
                    "message": "配置语法与路由结构校验通过" if valid else "; ".join(errs),
                    "state": st_copy,
                    "configDae": cfg,
                    "stats": self.ctx.stats(),
                },
            )
            return

        if path == "/api/v1/config/render":
            st_in = req.get("state") or self.ctx.state
            self._send_json(200, {"ok": True, "configDae": render_config_dae(st_in), "outboundErr": ""})
            return

        if path == "/api/v1/service/action":
            act = req.get("action")
            with self.ctx.lock:
                cfg = render_config_dae(self.ctx.state)
                errs = validate_syntax(cfg)
                if act in ("start", "reload", "validate") and errs:
                    self.ctx.log("error", "; ".join(errs))
                    self._send_json(200, {"ok": False, "error": "; ".join(errs), "stats": self.ctx.stats(), "logs": list(self.ctx.logs), "configDae": cfg})
                    return
                if act == "start":
                    self.ctx.running = True
                    self.ctx.started_at = time.time()
                    self.ctx.log("info", "dae 服务已启动")
                    msg = "dae 核心已启动"
                elif act == "stop":
                    self.ctx.running = False
                    self.ctx.log("info", "dae 服务已停止")
                    msg = "dae 核心已停止"
                elif act == "reload":
                    self.ctx._write_cfg_unlocked(cfg)
                    self.ctx.running = True
                    self.ctx.log("info", "执行 dae reload 热重载完成：eBPF 规则与节点组零断流更新生效")
                    msg = "已完成 dae reload 零断流热重载"
                elif act == "validate":
                    self.ctx.log("info", "配置语法校验通过 (Static & DSL Validator OK)")
                    msg = "dae validate 配置语法校验通过"
                else:
                    self._send_json(400, {"ok": False, "error": f"未知操作: {act}"})
                    return
                logs = list(self.ctx.logs)
            self._send_json(200, {"ok": True, "message": msg, "stats": self.ctx.stats(), "logs": logs, "configDae": cfg})
            return

        if path == "/api/v1/subscriptions/parse":
            sub_id = req.get("id") or f"sub-{int(time.time()*1000)}"
            tag = sanitize_ident(req.get("tag") or "my_sub")
            raw_content = (req.get("rawContent") or "").strip()
            url = (req.get("url") or "").strip()
            ua = req.get("userAgent") or "clash.meta"
            if not raw_content and url:
                try:
                    ureq = urllib.request.Request(url, headers={"User-Agent": ua})
                    with urllib.request.urlopen(ureq, timeout=12) as resp:
                        raw_content = resp.read(4 * 1024 * 1024).decode("utf-8", errors="ignore")
                except Exception as exc:
                    self._send_json(200, {"ok": False, "error": f"拉取远程订阅失败: {exc}"})
                    return
            try:
                nodes, fmt = parse_subscription_any(raw_content, sub_id, tag, req.get("excludeKeywords") or "")
            except Exception as exc:
                self._send_json(200, {"ok": False, "error": str(exc)})
                return

            persist_conv = bool(req.get("persistConverted")) or fmt in ("clash", "singbox") or not url
            sub_obj = {
                "id": sub_id,
                "tag": tag,
                "name": req.get("name") or tag,
                "url": url,
                "format": fmt,
                "userAgent": ua,
                "enabled": True,
                "autoUpdateHours": int(req.get("autoUpdateHours") or 6),
                "excludeKeywords": req.get("excludeKeywords") or "",
                "persistConverted": persist_conv,
                "nodeCount": len(nodes),
                "usedUploadBytes": 0,
                "usedDownloadBytes": 0,
                "totalTrafficBytes": 0,
                "expireUnix": 0,
                "updatedAt": now_iso(),
            }
            with self.ctx.lock:
                subs = self.ctx.state.setdefault("subscriptions", [])
                replaced = False
                for i, s in enumerate(subs):
                    if s.get("id") == sub_id:
                        subs[i] = sub_obj
                        replaced = True
                        break
                if not replaced:
                    subs.append(sub_obj)
                self.ctx.state["nodes"] = [n for n in (self.ctx.state.get("nodes") or []) if n.get("subId") != sub_id] + nodes
                self.ctx._save_unlocked(self.ctx.state)
                self.ctx.log("info", f"订阅 {sub_obj['name']} 解析成功：识别格式 {fmt}，导入 {len(nodes)} 个节点")
                st_copy = json.loads(json.dumps(self.ctx.state))
            self._send_json(
                200,
                {
                    "ok": True,
                    "detectedFormat": fmt,
                    "subscription": sub_obj,
                    "addedCount": len(nodes),
                    "state": st_copy,
                    "configDae": render_config_dae(st_copy),
                },
            )
            return

        if path == "/api/v1/nodes/import":
            try:
                nodes, fmt = parse_subscription_any(req.get("rawText") or "", "", "", "")
            except Exception as exc:
                self._send_json(200, {"ok": False, "error": str(exc)})
                return
            with self.ctx.lock:
                self.ctx.state.setdefault("nodes", []).extend(nodes)
                self.ctx._save_unlocked(self.ctx.state)
                st_copy = json.loads(json.dumps(self.ctx.state))
            self._send_json(200, {"ok": True, "detectedFormat": fmt, "addedCount": len(nodes), "state": st_copy, "configDae": render_config_dae(st_copy)})
            return

        if path == "/api/v1/nodes/probe":
            want_ids = set(req.get("nodeIds") or [])
            timeout_sec = max(0.3, min(5.0, (req.get("timeoutMs") or 2000) / 1000.0))
            with self.ctx.lock:
                targets = [dict(n) for n in (self.ctx.state.get("nodes") or []) if not want_ids or n["id"] in want_ids]
            with concurrent.futures.ThreadPoolExecutor(max_workers=16) as pool:
                results = list(pool.map(lambda item: probe_single_node(item, timeout_sec), targets))
            res_map = {r["id"]: r for r in results}
            with self.ctx.lock:
                for n in self.ctx.state.get("nodes") or []:
                    if n["id"] in res_map:
                        r = res_map[n["id"]]
                        n["latencyMs"] = r["latencyMs"]
                        n["alive"] = r["alive"]
                        n["checkedAt"] = r["checkedAt"]
                self.ctx._save_unlocked(self.ctx.state)
                nodes_copy = list(self.ctx.state.get("nodes") or [])
            self._send_json(200, {"ok": True, "results": results, "nodes": nodes_copy})
            return

        if path == "/api/v1/config/import":
            try:
                with self.ctx.lock:
                    new_st = parse_config_dae(req.get("configDae") or "", self.ctx.state)
                    self.ctx.state = new_st
                    self.ctx._save_unlocked(self.ctx.state)
                    st_copy = json.loads(json.dumps(self.ctx.state))
                self._send_json(200, {"ok": True, "state": st_copy, "configDae": render_config_dae(st_copy)})
            except Exception as exc:
                self._send_json(200, {"ok": False, "error": str(exc)})
            return

        if path == "/api/v1/routing/parse-dsl":
            rules, fb = parse_routing_dsl(req.get("dsl") or "")
            self._send_json(200, {"ok": True, "rules": rules, "fallback": fb})
            return

        if path == "/api/v1/dns/parse-dsl":
            dns_cfg = parse_dns_dsl(req.get("dsl") or "")
            self._send_json(200, {"ok": True, "dns": dns_cfg})
            return

        if path == "/api/v1/snapshots":
            with self.ctx.lock:
                cfg = render_config_dae(self.ctx.state)
                snap = {
                    "id": f"snap-{int(time.time()*1000)}",
                    "name": req.get("name") or f"快照 {now_hms()}",
                    "createdAt": now_iso(),
                    "configDae": cfg,
                }
                snaps = [snap] + (self.ctx.state.get("snapshots") or [])[:19]
                self.ctx.state["snapshots"] = snaps
                self.ctx._save_unlocked(self.ctx.state)
            self._send_json(200, {"ok": True, "snapshots": snaps})
            return

        if path == "/api/v1/snapshots/restore":
            with self.ctx.lock:
                found = next((s for s in (self.ctx.state.get("snapshots") or []) if s["id"] == req.get("id")), None)
                if not found:
                    self._send_json(200, {"ok": False, "error": "未找到对应快照"})
                    return
                self.ctx.state = parse_config_dae(found["configDae"], self.ctx.state)
                self.ctx._save_unlocked(self.ctx.state)
                st_copy = json.loads(json.dumps(self.ctx.state))
            self._send_json(200, {"ok": True, "state": st_copy, "configDae": render_config_dae(st_copy)})
            return

        if path == "/api/v1/panel/repo":
            raw_repo = (req.get("repo") or "").strip()
            raw_repo = re.sub(r"^https?://github\.com/", "", raw_repo).strip("/")
            if raw_repo.endswith(".git"):
                raw_repo = raw_repo[:-4]
            with self.ctx.lock:
                self.ctx.state["updateRepo"] = raw_repo
                self.ctx._save_unlocked(self.ctx.state)
            self._send_json(200, {"ok": True, "repo": raw_repo})
            return

        if path == "/api/v1/logs/clear":
            with self.ctx.lock:
                self.ctx.logs = [{"time": now_hms(), "level": "info", "message": "日志缓冲区已清空"}]
                logs = list(self.ctx.logs)
            self._send_json(200, {"ok": True, "logs": logs})
            return

        self._send_json(404, {"error": "unknown endpoint"})


def main():
    parser = argparse.ArgumentParser(description="DaeStudio (daed-next) Preview Server")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=2023)
    parser.add_argument("--data", default=os.path.join(ROOT_DIR, ".runtime"))
    parser.add_argument("--config", default=os.path.join(ROOT_DIR, ".runtime", "config.dae"))
    args = parser.parse_args()
    if args.host not in ("127.0.0.1", "localhost"):
        parser.error("Preview server has no production authentication; bind to 127.0.0.1 only")

    DaeStudioHandler.ctx = AppContext(args.data, args.config)
    httpd = ThreadingHTTPServer((args.host, args.port), DaeStudioHandler)
    print("============================================================")
    print("  DaeStudio (daed-next) Live Server Running")
    print(f"  Dashboard URL : http://{args.host}:{args.port}")
    print(f"  Generated DAE : {args.config}")
    print("============================================================", flush=True)
    httpd.serve_forever()


if __name__ == "__main__":
    main()
