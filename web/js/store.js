export const store = {
  activeTab: "overview",
  systemSubTab: "netdns", // netdns | configfile | logs
  dirty: false,
  applying: false,
  probing: false,
  checkingConn: false,
  connectivity: null,
  token: localStorage.getItem("daed_next_token") || "",
  theme: localStorage.getItem("daed_next_theme") || "dark",

  state: null,
  panelVersion: "v1.0.9",
  updateRepo: "",
  geoDataStatus: null,
  geoUpdating: false,
  configDae: "",
  stats: {
    running: false,
    mockMode: false,
    pid: 0,
    uptimeSeconds: 0,
    uploadBps: 0,
    downloadBps: 0,
    totalUpload: 0,
    totalDownload: 0,
    memoryMb: 0,
    activeConns: 0,
    configPath: "/etc/dae/config.dae",
  },
  doctor: null,
  logs: [],

  nodeFilter: {
    keyword: "",
    region: "ALL",
    protocol: "ALL",
    sortBy: "latency",
  },
  selectedGroupIdx: 0,
  logLevelFilter: "ALL",
  logSearch: "",
};

const IF_MATCH_ENDPOINTS = new Set([
  "/api/v1/state",
  "/api/v1/auth/update",
  "/api/v1/panel/repo",
  "/api/v1/geodata/config",
  "/api/v1/config/import",
  "/api/v1/snapshots",
  "/api/v1/snapshots/delete",
  "/api/v1/snapshots/restore",
  "/api/v1/subscriptions/parse",
  "/api/v1/nodes/import",
  "/api/v1/nodes/save",
  "/api/v1/nodes/probe",
]);

export async function apiFetch(path, options = {}) {
  const headers = {
    "Content-Type": "application/json",
    ...(options.headers || {}),
  };
  if (store.token) {
    headers["Authorization"] = `Bearer ${store.token}`;
  }
  const expectedRevision = store.state?.revision;
  if (options.method === "POST" && IF_MATCH_ENDPOINTS.has(path) && expectedRevision && !headers["If-Match"]) {
    headers["If-Match"] = expectedRevision;
  }
  const { timeoutMs = 15000, ...request } = options;
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  try {
    const resp = await fetch(path, { ...request, headers, signal: controller.signal });
    const rawText = await resp.text();
    let data = {};
    if (rawText) {
      try {
        data = JSON.parse(rawText);
      } catch (_) {
        throw new Error("服务返回非 JSON 响应 (HTTP " + resp.status + ")");
      }
    }
    if (resp.status === 401 && data.authEnabled) {
      const err = new Error("需要重新登录");
      err.unauthorized = true;
      throw err;
    }
    if (resp.status === 409) throw new Error(data.error || "配置版本冲突，请刷新后重试");
    const nextRevision = data.revision || data.state?.revision;
    if (resp.ok && data.ok && nextRevision && store.state) {
      store.state.revision = nextRevision;
    }
    if (!resp.ok && !data.error) throw new Error("服务返回 HTTP " + resp.status);
    return data;
  } catch (err) {
    if (controller.signal.aborted) throw new Error("等待响应超时，请刷新确认当前状态后再试");
    throw err;
  } finally {
    clearTimeout(timer);
  }
}

export function toast(message, type = "info") {
  const stack = document.getElementById("toast-stack");
  if (!stack) return;
  const el = document.createElement("div");
  el.className = `toast-msg ${type}`;
  el.textContent = message;
  el.addEventListener("click", () => el.remove());
  stack.appendChild(el);
  setTimeout(() => {
    el.style.opacity = "0";
    el.style.transform = "translateY(6px)";
    setTimeout(() => el.remove(), 220);
  }, type === "error" ? 9000 : 3400);
}

export function formatBytes(bytes) {
  const n = Number(bytes || 0);
  if (n <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  const idx = Math.min(units.length - 1, Math.floor(Math.log(n) / Math.log(1024)));
  return (n / Math.pow(1024, idx)).toFixed(idx === 0 ? 0 : 2) + " " + units[idx];
}

export function formatSpeed(bps) {
  return formatBytes(bps) + "/s";
}

export function formatUptime(sec) {
  const s = Math.max(0, Number(sec || 0));
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  const r = s % 60;
  if (d > 0) return `${d}d ${h}h ${m}m`;
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m ${r}s`;
  return `${r}s`;
}

export function regionLabel(region) {
  const map = {
    HK: "香港",
    TW: "台湾",
    JP: "日本",
    SG: "新加坡",
    US: "美国",
    KR: "韩国",
    DE: "德国",
    UK: "英国",
  };
  return map[(region || "").toUpperCase()] || "其他";
}

export function latencyTagHTML(ms) {
  if (ms === -1 || ms === undefined || ms === null) {
    return `<span title="仅测节点入口 TCP 建连，不代表代理实际延迟；TCP 可连接不代表代理可用" class="tag mono">—</span>`;
  }
  if (ms === 0) {
    return `<span title="仅测节点入口 TCP 建连，不代表代理实际延迟；TCP 可连接不代表代理可用" class="tag tag-err mono">超时</span>`;
  }
  if (ms < 120) {
    return `<span title="仅测节点入口 TCP 建连，不代表代理实际延迟；TCP 可连接不代表代理可用" class="tag tag-ok mono">${ms} ms</span>`;
  }
  if (ms < 260) {
    return `<span title="仅测节点入口 TCP 建连，不代表代理实际延迟；TCP 可连接不代表代理可用" class="tag tag-warn mono">${ms} ms</span>`;
  }
  return `<span title="仅测节点入口 TCP 建连，不代表代理实际延迟；TCP 可连接不代表代理可用" class="tag tag-err mono">${ms} ms</span>`;
}

export function escapeHTML(str) {
  return String(str ?? "")
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}
