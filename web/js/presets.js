export const REGION_OPTIONS = [
  { code: "HK", label: "香港", regex: "HK|Hong\\s*Kong|香港|港" },
  { code: "TW", label: "台湾", regex: "TW|Taiwan|台湾|台|新北|台北|彰化" },
  { code: "JP", label: "日本", regex: "JP|Japan|日本|东京|大阪|埼玉" },
  { code: "SG", label: "新加坡", regex: "SG|Singapore|新加坡|狮城" },
  { code: "US", label: "美国", regex: "US|United\\s*States|美国|美|硅谷|洛杉矶|西雅图|圣何塞" },
  { code: "KR", label: "韩国", regex: "KR|Korea|韩国|首尔|韩" },
  { code: "DE", label: "德国", regex: "DE|Germany|德国|法兰克福" },
  { code: "UK", label: "英国", regex: "UK|GB|Britain|英国|伦敦" },
];

export const ROUTING_FIELDS = [
  { value: "domain", label: "域名 (domain)" },
  { value: "dip", label: "目标 IP (dip)" },
  { value: "sip", label: "源 IP (sip)" },
  { value: "dport", label: "目标端口 (dport)" },
  { value: "sport", label: "源端口 (sport)" },
  { value: "l4proto", label: "协议 tcp/udp (l4proto)" },
  { value: "pname", label: "本机进程名 (pname)" },
  { value: "mac", label: "设备 MAC (mac)" },
];

export const ROUTING_PRESETS = [
  {
    id: "ai-suite",
    title: "AI 服务分流 (OpenAI / Claude / Gemini)",
    ensureGroup: {
      id: "grp-ai-auto",
      name: "ai_service",
      description: "AI 服务策略组（排除香港节点）",
      enabled: true,
      policy: "min_moving_avg",
      mode: "smart",
      filter: {
        subTags: [],
        regions: ["US", "JP", "SG", "TW"],
        includeKeywords: [],
        excludeKeywords: ["HK", "香港", "剩余流量"],
        includeRegex: "",
        excludeRegex: "",
        manualNodeTags: [],
      },
    },
    rules: [
      {
        name: "AI 大模型分流",
        category: "ai",
        enabled: true,
        conditions: [
          {
            negate: false,
            field: "domain",
            value: "geosite:openai, geosite:anthropic, geosite:google-gemini, suffix:claude.ai, suffix:cursor.sh",
          },
        ],
        outbound: "ai_service",
      },
    ],
  },
  {
    id: "media-unlock",
    title: "国际流媒体分流 (Netflix / YouTube / Disney+)",
    ensureGroup: {
      id: "grp-stream-auto",
      name: "streaming",
      description: "流媒体解锁策略组",
      enabled: true,
      policy: "min",
      mode: "smart",
      filter: {
        subTags: [],
        regions: ["HK", "TW", "JP", "SG"],
        includeKeywords: [],
        excludeKeywords: ["剩余流量", "到期"],
        includeRegex: "",
        excludeRegex: "",
        manualNodeTags: [],
      },
    },
    rules: [
      {
        name: "国际流媒体分流",
        category: "media",
        enabled: true,
        conditions: [
          {
            negate: false,
            field: "domain",
            value: "geosite:netflix, geosite:disney, geosite:youtube, geosite:spotify, geosite:bahamut",
          },
        ],
        outbound: "streaming",
      },
    ],
  },
  {
    id: "steam-direct",
    title: "Steam 下载国内直连",
    rules: [
      {
        name: "Steam 下载 CDN 直连",
        category: "direct",
        enabled: true,
        conditions: [{ negate: false, field: "domain", value: "geosite:steam@cn, suffix:steamcontent.com" }],
        outbound: "direct",
      },
    ],
  },
  {
    id: "block-quic",
    title: "阻断海外 UDP 443 (防 QUIC 限速)",
    rules: [
      {
        name: "阻断海外 QUIC",
        category: "block",
        enabled: true,
        conditions: [
          { negate: false, field: "l4proto", value: "udp" },
          { negate: false, field: "dport", value: "443" },
          { negate: true, field: "dip", value: "geoip:cn" },
        ],
        outbound: "block",
      },
    ],
  },
];

// Evaluates candidate nodes matching a Group's pool (ignoring pinnedNodeTag so all selectable candidates show in Overview)
export function matchNodesForGroup(group, allNodes) {
  const enabledNodes = (allNodes || []).filter((n) => n.enabled !== false);
  if (!group) return [];
  if (group.mode === "raw") {
    const raw = (group.rawFilterDsl || "").trim();
    // If raw filter is simple `filter: name(tag1, tag2)`, match those exact tags; otherwise return all enabled nodes
    const simpleMatch = raw.match(/^filter:\s*name\(([^:'"]+)\)$/);
    if (simpleMatch) {
      const tags = new Set(simpleMatch[1].split(",").map((s) => s.trim()).filter(Boolean));
      const subset = enabledNodes.filter((n) => tags.has(n.tag));
      return subset;
    }
    return enabledNodes;
  }

  const f = group.filter || {};
  const manualSet = new Set(f.manualNodeTags || []);
  const subSet = new Set(f.subTags || []);
  const subIds = new Set(f.subIds || []);
  const regions = new Set((f.regions || []).map((r) => r.toUpperCase()));
  const incKws = (f.includeKeywords || []).filter(Boolean).map((k) => k.toLowerCase());
  const excKws = (f.excludeKeywords || []).filter(Boolean).map((k) => k.toLowerCase());

  let incRe = null;
  let excRe = null;
  try { if (f.includeRegex) incRe = new RegExp(f.includeRegex, "i"); } catch (_) {}
  try { if (f.excludeRegex) excRe = new RegExp(f.excludeRegex, "i"); } catch (_) {}

  const hasDynamic =
    subIds.size > 0 || subSet.size > 0 || regions.size > 0 || incKws.length > 0 || excKws.length > 0 || Boolean(incRe) || Boolean(excRe);

  return enabledNodes.filter((n) => {
    if (manualSet.has(n.tag)) return true;
    if (!hasDynamic && manualSet.size > 0) return false;
    if (subIds.size > 0 && !subIds.has(n.subId)) return false;
    if (subSet.size > 0 && !subSet.has(n.subTag)) return false;
    if (regions.size > 0) {
      const nr = (n.region || "").toUpperCase();
      let ok = regions.has(nr);
      if (!ok) {
        for (const r of regions) {
          const opt = REGION_OPTIONS.find((o) => o.code === r);
          if (opt && new RegExp(opt.regex, "i").test(n.name)) { ok = true; break; }
        }
      }
      if (!ok) return false;
    }
    const low = (n.name || "").toLowerCase();
    for (const kw of incKws) if (!low.includes(kw)) return false;
    for (const kw of excKws) if (low.includes(kw)) return false;
    if (incRe && !incRe.test(n.name)) return false;
    if (excRe && excRe.test(n.name)) return false;
    return true;
  });
}

function computeNodeAliases(nodes = []) {
  const used = new Set();
  return nodes.map((node) => {
    const raw = String(node.tag || "").trim();
    let base = raw
      .split("")
      .map((ch, i) =>
        /[A-Za-z_]/.test(ch) || (i > 0 && /[0-9-]/.test(ch))
          ? ch
          : i === 0 && /[0-9]/.test(ch)
          ? "n_" + ch
          : "_"
      )
      .join("");
    if (!base) base = "node";
    let alias = base;
    for (let n = 2; used.has(alias); n++) {
      alias = `${base}_${n}`;
    }
    used.add(alias);
    return alias;
  });
}

export function buildGroupFilterPreview(group, activeNodes = [], subs = []) {
  if (!group) return "";
  const aliases = computeNodeAliases(activeNodes);
  if (group.policy === "fixed" && (group.pinnedNodeId || group.pinnedNodeTag)) {
    const idx = (activeNodes || []).findIndex((n) =>
      group.pinnedNodeId ? n.id === group.pinnedNodeId : n.tag === group.pinnedNodeTag
    );
    if (idx >= 0) {
      return `filter: name(${aliases[idx]})`;
    }
    if (group.pinnedNodeTag) {
      return `filter: name(${group.pinnedNodeTag})`;
    }
  }
  if (group.mode === "raw") return group.rawFilterDsl || "";
  const f = group.filter || {};
  const parts = [];
  if (f.subIds?.length) {
    const resolvedTags = f.subIds
      .map((id) => (subs || []).find((s) => s.id === id)?.tag || id)
      .filter(Boolean);
    if (resolvedTags.length) parts.push(`subtag(${resolvedTags.join(", ")})`);
  } else if (f.subTags?.length) {
    parts.push(`subtag(${f.subTags.join(", ")})`);
  }
  if (f.regions?.length) {
    const pats = f.regions.map((r) => REGION_OPTIONS.find((o) => o.code === r)?.regex || r);
    parts.push(`name(regex: '${pats.join("|")}')`);
  }
  for (const kw of f.includeKeywords || []) if (kw.trim()) parts.push(`name(keyword: '${kw.trim()}')`);
  if (f.includeRegex?.trim()) parts.push(`name(regex: '${f.includeRegex.trim()}')`);
  for (const kw of f.excludeKeywords || []) if (kw.trim()) parts.push(`!name(keyword: '${kw.trim()}')`);
  if (f.excludeRegex?.trim()) parts.push(`!name(regex: '${f.excludeRegex.trim()}')`);
  const lines = [];
  if (parts.length) lines.push(`filter: ${parts.join(" && ")}`);
  if (f.manualNodeTags?.length) lines.push(`filter: name(${f.manualNodeTags.join(", ")})`);
  return lines.join("\n") || "# 默认匹配全部可用节点";
}

export function renderRuleExprText(rule) {
  if (rule.rawExpr && (!rule.conditions || rule.conditions.length === 0)) {
    return rule.rawExpr;
  }
  return (rule.conditions || [])
    .filter((c) => c.field && c.value)
    .map((c) => `${c.negate ? "!" : ""}${c.field}(${c.value})`)
    .join(" && ");
}

export function renderRoutingDSLFromRules(rules, fallback) {
  const lines = [];
  for (const r of rules || []) {
    const expr = renderRuleExprText(r);
    if (!expr) continue;
    if (r.name) lines.push(`# ${r.name}`);
    lines.push(r.enabled !== false ? `${expr} -> ${r.outbound || "direct"}` : `# [Disabled] ${expr} -> ${r.outbound || "direct"}`);
  }
  lines.push("");
  lines.push(`fallback: ${fallback || "direct"}`);
  return lines.join("\n");
}

export function countRoutingRules(routing) {
  if (!routing) return 0;
  if (routing.mode === "dsl" && routing.rawDsl?.trim()) {
    let count = 0;
    for (const line of routing.rawDsl.split("\n")) {
      const trimmed = line.trim();
      if (!trimmed) continue;
      if (trimmed.startsWith("#") && !/^#\s*\[Disabled\]/i.test(trimmed)) continue;
      if (/^(#\s*\[Disabled\]\s*)?(.+?)\s*->\s*([A-Za-z0-9_-]+)\s*(?:#.*)?$/.test(trimmed)) {
        count++;
      }
    }
    return count;
  }
  return (routing.rules || []).length;
}

export function renderDNSDSLFromVisual(dns) {
  if (!dns) return "";
  const escSq = (s) => String(s || "").replace(/'/g, "\\'");
  const lines = [];
  if (dns.bind?.trim()) {
    lines.push(`bind: '${escSq(dns.bind.trim())}'`);
  }
  if (dns.ipVersionPrefer === 4 || dns.ipVersionPrefer === 6) {
    lines.push(`ipversion_prefer: ${dns.ipVersionPrefer}`);
  }
  if (lines.length > 0) lines.push("");

  const upstreams = dns.upstreams || [];
  lines.push("upstream {");
  if (upstreams.length === 0) {
    lines.push("  alidns: 'udp://223.5.5.5:53'");
  } else {
    for (const u of upstreams) {
      if (!u.tag?.trim() || !u.url?.trim()) continue;
      if (u.note?.trim()) lines.push(`  # ${u.note.trim()}`);
      lines.push(`  ${u.tag.trim()}: '${escSq(u.url.trim())}'`);
    }
  }
  lines.push("}");
  lines.push("");
  lines.push("routing {");
  lines.push("  request {");
  for (const r of dns.requestRules || []) {
    if (!r.expr?.trim() || !r.target?.trim()) continue;
    if (r.comment?.trim()) lines.push(`    # ${r.comment.trim()}`);
    lines.push(
      r.enabled !== false
        ? `    ${r.expr.trim()} -> ${r.target.trim()}`
        : `    # [Disabled] ${r.expr.trim()} -> ${r.target.trim()}`
    );
  }
  const reqFb = dns.requestFallback?.trim() || upstreams[0]?.tag || "alidns";
  lines.push(`    fallback: ${reqFb}`);
  lines.push("  }");

  const respRules = dns.responseRules || [];
  const respFb = dns.responseFallback?.trim() || "accept";
  if (respRules.length > 0 || respFb !== "accept") {
    lines.push("  response {");
    for (const r of respRules) {
      if (!r.expr?.trim() || !r.target?.trim()) continue;
      if (r.comment?.trim()) lines.push(`    # ${r.comment.trim()}`);
      lines.push(
        r.enabled !== false
          ? `    ${r.expr.trim()} -> ${r.target.trim()}`
          : `    # [Disabled] ${r.expr.trim()} -> ${r.target.trim()}`
      );
    }
    lines.push(`    fallback: ${respFb}`);
    lines.push("  }");
  }
  lines.push("}");
  return lines.join("\n");
}
