import {
  store,
  apiFetch,
  toast,
  formatBytes,
  formatSpeed,
  formatUptime,
  regionLabel,
  latencyTagHTML,
  escapeHTML,
} from "./store.js";
import {
  REGION_OPTIONS,
  ROUTING_FIELDS,
  ROUTING_PRESETS,
  matchNodesForGroup,
  buildGroupFilterPreview,
  renderRuleExprText,
  renderRoutingDSLFromRules,
  countRoutingRules,
  renderDNSDSLFromVisual,
} from "./presets.js";
import { TrafficChart } from "./charts.js";

const chart = new TrafficChart(36);
let statsTimer = null;
let statsPending = false;
let modalLocked = false;

// ============================================================================
// Lifecycle & Global Bindings
// ============================================================================

function syncDirtyUI(dirty) {
  store.dirty = Boolean(dirty);
  document.getElementById("unsaved-indicator")?.classList.toggle("hidden", !store.dirty);
  document.getElementById("btn-save-apply")?.classList.toggle("is-dirty", store.dirty);
  syncNavCounts();
}

async function initApp() {
  applyTheme(store.theme);
  bindGlobalEvents();
  await loadBootstrap();
  checkConnectivitySilent();
  startTelemetryLoop();
}

function applyTheme(theme) {
  store.theme = theme === "light" ? "light" : "dark";
  document.documentElement.setAttribute("data-theme", store.theme);
  localStorage.setItem("daed_next_theme", store.theme);
  const btn = document.getElementById("btn-theme");
  if (btn) btn.textContent = store.theme === "dark" ? "☀ 浅色" : "☾ 深色";
}

async function loadBootstrap() {
  try {
    const data = await apiFetch("/api/v1/bootstrap");
    store.state = normalizeState(data.state);
    store.panelVersion = data.panelVersion || store.panelVersion || "v1.0.9";
    store.updateRepo = data.updateRepo ?? store.state?.updateRepo ?? "";
    store.geoDataStatus = data.geoDataStatus || store.geoDataStatus;
    store.configDae = data.configDae || "";
    store.stats = data.stats || store.stats;
    store.doctor = data.doctor || null;
    store.logs = data.logs || [];
    syncDirtyUI(Boolean(data.pendingApply));
    chart.push(store.stats.uploadBps, store.stats.downloadBps);

    setText("side-panel-ver", store.panelVersion);
    const verLine = (store.doctor?.daeVersion || "dae").split("\n")[0].replace("dae version ", "");
    setText("side-dae-ver", verLine);

    syncHeader();
    renderPage();
  } catch (err) {
    if (err.unauthorized) {
      openLoginModal();
      return;
    }
    document.getElementById("view-root").textContent = "加载状态失败：" + err.message;
    toast("加载状态失败: " + err.message, "error");
  }
}

async function checkConnectivitySilent(manual = false) {
  if (store.checkingConn) return;
  store.checkingConn = true;
  if (store.activeTab === "overview") renderConnectivityBarDOM();
  try {
    const res = await apiFetch("/api/v1/connectivity");
    if (res.targets) {
      store.connectivity = res.targets;
      if (manual) toast("已完成软路由分流连通性实测", "success");
    }
  } catch (_) {}
  store.checkingConn = false;
  if (store.activeTab === "overview") renderConnectivityBarDOM();
}

function startTelemetryLoop() {
  if (statsTimer) clearInterval(statsTimer);
  statsTimer = setInterval(async () => {
    if (!store.state || statsPending || document.hidden || store.applying) return;
    statsPending = true;
    try {
      const showLogs = store.activeTab === "system" && store.systemSubTab === "logs";
      const data = await apiFetch("/api/v1/stats?logs=" + (showLogs ? "1" : "0"));
      const modalOpen = !document.getElementById("modal-overlay")?.classList.contains("hidden");
      if (
        data.revision &&
        store.state?.revision &&
        data.revision !== store.state.revision &&
        !store.dirty &&
        !store.applying &&
        !store.probing &&
        !modalOpen
      ) {
        await loadBootstrap();
        return;
      }
      if (data.stats) {
        store.stats = data.stats;
        chart.push(data.stats.uploadBps, data.stats.downloadBps);
      }
      if (data.logs) store.logs = data.logs;
      syncHeader();
      if (store.activeTab === "overview") {
        updateOverviewLiveMetrics();
      } else if (store.activeTab === "system" && store.systemSubTab === "logs") {
        renderLogsListDOM();
      }
    } catch (_) {}
    finally { statsPending = false; }
  }, 2500);
}

function mergePoolFromServerState(serverState, nextConfigDae = "") {
  if (!serverState) return;
  const normalized = normalizeState(serverState);
  if (!store.state || !store.dirty) {
    store.state = normalized;
    if (nextConfigDae) store.configDae = nextConfigDae;
  } else {
    store.state.subscriptions = normalized.subscriptions;
    store.state.nodes = normalized.nodes;
    store.state.revision = normalized.revision;
    store.state.updatedAt = normalized.updatedAt;
  }
  reconcileGroupsWithActivePool();
}

function markDirty() {
  syncDirtyUI(true);
}

function activeNodes(state = store.state) {
  const sources = new Map((state.subscriptions || []).filter((s) => s.enabled).map((s) => [s.id, s.tag]));
  return (state.nodes || []).filter((n) => n.enabled !== false && n.uri && (!n.subId || sources.has(n.subId)))
    .map((n) => n.subId ? { ...n, subTag: sources.get(n.subId) } : n);
}

function normalizeState(state) {
  for (const key of ["subscriptions", "nodes", "groups", "snapshots"]) state[key] ??= [];
  return state;
}

function setApplyStatus(message, phase = "") {
  const status = document.getElementById("apply-status");
  if (!status) return;
  status.textContent = message;
  status.dataset.phase = phase;
  status.classList.toggle("hidden", !message);
}

function syncApplyControls() {
  document.getElementById("view-root")?.toggleAttribute("inert", store.applying);
  const btn = document.getElementById("btn-save-apply");
  btn.disabled = store.applying;
  btn.textContent = store.applying ? "应用中…" : "保存并应用";
}

async function saveState(applyReload = true, candidate = store.state, outbound = "") {
  if (!candidate || store.applying) return false;
  store.applying = true;
  syncApplyControls();
  setApplyStatus(outbound ? "正在切换到「" + outbound + "」，等待 dae 确认…" : "正在校验并应用配置…", "pending");
  try {
    const res = await apiFetch("/api/v1/state", {
      method: "POST",
      timeoutMs: 145000,
      body: JSON.stringify({ state: candidate, apply: applyReload, switchOutbound: Boolean(outbound) }),
    });
    if (!res.ok) throw new Error(res.error || res.reloadErr || res.message || "配置应用失败");
    store.state = normalizeState(res.state || candidate);
    if (res.configDae) store.configDae = res.configDae;
    if (res.stats) store.stats = res.stats;
    syncDirtyUI(false);
    const message = outbound ? "已切换到「" + outbound + "」" : applyReload ? "配置已应用" : "配置已保存";
    setApplyStatus(message, "success");
    toast(message, "success");
    syncHeader();
    renderPage();
    if (applyReload) checkConnectivitySilent();
    return true;
  } catch (err) {
    const message = (outbound ? "切换失败" : "应用失败") + "：" + err.message;
    setApplyStatus(message, "error");
    toast(message, "error");
    if (err.unauthorized) openLoginModal();
    return false;
  } finally {
    store.applying = false;
    syncApplyControls();
  }
}

function switchGroup(index, label, edit) {
  if (store.applying) return;
  const candidate = structuredClone(store.state);
  edit(candidate.groups[index], candidate);
  return saveState(true, candidate, label);
}

function clearPin(group) {
  group.pinnedNodeId = "";
  group.pinnedNodeTag = "";
  group.fixedIndex = 0;
}

function reconcileGroupsWithActivePool() {
  if (!store.state) return;
  const active = activeNodes(store.state);
  const activeIds = new Set(active.map((n) => n.id));
  const activeTags = new Set(active.map((n) => n.tag));
  const allNodeTags = new Set((store.state.nodes || []).map((n) => n.tag));
  const existingSubIds = new Set((store.state.subscriptions || []).map((s) => s.id));
  const existingSubTags = new Set((store.state.subscriptions || []).map((s) => s.tag));
  const autoReverted = [];

  for (const grp of store.state.groups || []) {
    if (grp.filter) {
      if (Array.isArray(grp.filter.subIds) && grp.filter.subIds.length) {
        grp.filter.subIds = grp.filter.subIds.filter((id) => existingSubIds.has(id));
      }
      if (Array.isArray(grp.filter.subTags) && grp.filter.subTags.length) {
        grp.filter.subTags = grp.filter.subTags.filter((tag) => existingSubTags.has(tag));
      }
      if (Array.isArray(grp.filter.manualNodeTags) && grp.filter.manualNodeTags.length) {
        grp.filter.manualNodeTags = grp.filter.manualNodeTags.filter((tag) => allNodeTags.has(tag));
      }
    }
    const hasPinned = Boolean(grp.pinnedNodeId || grp.pinnedNodeTag);
    const pinStillActive = grp.pinnedNodeId
      ? activeIds.has(grp.pinnedNodeId)
      : grp.pinnedNodeTag
      ? activeTags.has(grp.pinnedNodeTag)
      : true;
    if (hasPinned && !pinStillActive) {
      clearPin(grp);
      if (grp.policy === "fixed") {
        grp.policy = "min_moving_avg";
      }
      autoReverted.push(grp.name);
    }
  }
  if (autoReverted.length > 0) {
    toast(`固定节点已移除或停用，策略组「${autoReverted.join("、")}」已自动回退为智能优选`, "info");
  }
}

async function runServiceAction(action) {
  if (store.applying) return;
  try {
    if (store.dirty && (action === "start" || action === "reload")) {
      await saveState(true);
      return;
    }
    const res = await apiFetch("/api/v1/service/action", {
      method: "POST",
      timeoutMs: 145000,
      body: JSON.stringify({ action }),
    });
    if (res.stats) store.stats = res.stats;
    if (res.logs) store.logs = res.logs;
    if (res.configDae) store.configDae = res.configDae;
    if (res.ok) {
      toast(res.message || "操作成功", "success");
      if (action === "start" || action === "reload") {
        setTimeout(() => checkConnectivitySilent(false), 1500);
      }
    } else {
      toast(res.error || "操作失败", "error");
    }
    syncHeader();
    renderPage();
  } catch (err) {
    toast("请求失败: " + err.message, "error");
  }
}

function syncHeader() {
  const pill = document.getElementById("service-pill");
  const txt = document.getElementById("service-status-text");
  if (pill && txt) {
    if (store.stats.running) {
      pill.className = "state-badge online";
      txt.textContent = store.stats.mockMode ? "预览模式" : "运行中";
    } else {
      pill.className = "state-badge offline";
      txt.textContent = "已停止";
    }
  }
  setText("top-down-speed", formatSpeed(store.stats.downloadBps));
  setText("top-up-speed", formatSpeed(store.stats.uploadBps));
  syncNavCounts();
}

function syncNavCounts() {
  if (!store.state) return;
  setText("badge-nodes", store.state.nodes?.length || 0);
  setText("badge-groups", store.state.groups?.length || 0);
  setText("badge-rules", countRoutingRules(store.state.routing));
}

function setText(id, val) {
  const el = document.getElementById(id);
  if (el) el.textContent = String(val);
}

function switchTab(tab) {
  store.activeTab = tab;
  document.querySelectorAll(".nav-btn").forEach((b) => {
    b.classList.toggle("active", b.dataset.tab === tab);
  });
  document.getElementById("sidebar")?.classList.remove("open");
  document.getElementById("sidebar-backdrop")?.classList.add("hidden");
  renderPage();
}

function bindGlobalEvents() {
  document.getElementById("nav-list")?.addEventListener("click", (e) => {
    const btn = e.target.closest(".nav-btn");
    if (btn?.dataset.tab) switchTab(btn.dataset.tab);
  });

  document.getElementById("btn-mobile-menu")?.addEventListener("click", () => {
    document.getElementById("sidebar")?.classList.toggle("open");
    document.getElementById("sidebar-backdrop")?.classList.toggle("hidden");
  });
  document.getElementById("sidebar-backdrop")?.addEventListener("click", () => {
    document.getElementById("sidebar")?.classList.remove("open");
    document.getElementById("sidebar-backdrop")?.classList.add("hidden");
  });

  document.getElementById("btn-theme")?.addEventListener("click", () => {
    applyTheme(store.theme === "dark" ? "light" : "dark");
  });

  document.getElementById("side-panel-ver")?.addEventListener("click", () => {
    store.systemSubTab = "logs";
    switchTab("system");
  });

  document.getElementById("btn-save-apply")?.addEventListener("click", () => saveState(true));
  document.getElementById("modal-close")?.addEventListener("click", closeModal);
  document.getElementById("modal-overlay")?.addEventListener("click", (e) => {
    if (e.target.id === "modal-overlay") closeModal();
  });

  window.addEventListener("keydown", (e) => {
    if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "s") {
      e.preventDefault();
      saveState(true);
    } else if (e.key === "Escape") {
      closeModal();
    }
  });
}

function renderPage() {
  const root = document.getElementById("view-root");
  if (!root || !store.state) return;
  switch (store.activeTab) {
    case "overview":
      renderOverview(root);
      break;
    case "proxies":
      renderProxiesAndSubs(root);
      break;
    case "groups":
      renderGroups(root);
      break;
    case "rules":
      renderRules(root);
      break;
    case "system":
      renderSystem(root);
      break;
    default:
      renderOverview(root);
  }
}

// ============================================================================
// 1. Overview & Instant Node Switcher
// ============================================================================

function nodeEndpointKey(node) {
  if (!node || !node.address || !node.port) return "";
  const host = String(node.address).trim().replace(/^\[|\]$/g, "").toLowerCase();
  return host ? `${host}:${node.port}` : "";
}

function getPinnedEndpointsOfOtherGroups(groups, selfIdx, enabledNodes) {
  const pinnedKeys = new Set();
  (groups || []).forEach((grp, idx) => {
    if (idx === selfIdx || !grp?.enabled || grp.policy !== "fixed") return;
    const matched = matchNodesForGroup(grp, enabledNodes);
    const pinned = enabledNodes.find((n) =>
      grp.pinnedNodeId ? n.id === grp.pinnedNodeId : grp.pinnedNodeTag && n.tag === grp.pinnedNodeTag
    );
    const candidates = pinned && !matched.some((n) => n.id === pinned.id) ? [pinned, ...matched] : matched;
    const activeNode = pinned || candidates[grp.fixedIndex || 0];
    const ep = nodeEndpointKey(activeNode);
    if (ep) pinnedKeys.add(ep);
  });
  return pinnedKeys;
}

function computeGroupTraffic(grp, gIdx, candidates, pinned, isAuto, enabledNodes) {
  const epMap = store.stats?.endpointTraffic || {};
  if (!isAuto) {
    const activeNode = pinned || candidates[grp.fixedIndex || 0];
    const ep = nodeEndpointKey(activeNode);
    const item = (ep && epMap[ep]) || null;
    return {
      downloadBps: item?.downloadBps || 0,
      uploadBps: item?.uploadBps || 0,
      totalDownload: item?.totalDownload || 0,
      totalUpload: item?.totalUpload || 0,
      activeConns: item?.activeConns || 0,
      topNode: activeNode || null,
    };
  }

  const otherPinned = candidates.length > 1 ? getPinnedEndpointsOfOtherGroups(store.state?.groups, gIdx, enabledNodes) : new Set();
  const seenEndpoints = new Set();
  let downloadBps = 0;
  let uploadBps = 0;
  let totalDownload = 0;
  let totalUpload = 0;
  let activeConns = 0;
  let topNode = null;
  let topRate = 0;

  for (const n of candidates) {
    const ep = nodeEndpointKey(n);
    if (!ep || seenEndpoints.has(ep) || otherPinned.has(ep)) continue;
    seenEndpoints.add(ep);
    const item = epMap[ep];
    if (!item) continue;
    downloadBps += item.downloadBps || 0;
    uploadBps += item.uploadBps || 0;
    totalDownload += item.totalDownload || 0;
    totalUpload += item.totalUpload || 0;
    activeConns += item.activeConns || 0;
    const rate = (item.downloadBps || 0) + (item.uploadBps || 0);
    if (rate > topRate) {
      topRate = rate;
      topNode = n;
    }
  }
  return { downloadBps, uploadBps, totalDownload, totalUpload, activeConns, topNode };
}

function renderGroupRateHTML(traffic, isAuto) {
  const down = traffic?.downloadBps || 0;
  const up = traffic?.uploadBps || 0;
  const totDown = traffic?.totalDownload || 0;
  const totUp = traffic?.totalUpload || 0;
  const conns = traffic?.activeConns || 0;
  const active = down > 0 || up > 0;
  const hasHistory = totDown > 0 || totUp > 0;

  let subText = "暂无流量经过此出口";
  if (isAuto && active && traffic?.topNode) {
    subText = `当前走 ${escapeHTML(traffic.topNode.name)} · 累计 ↓ ${formatBytes(totDown)}`;
  } else if (hasHistory || conns > 0) {
    subText = `累计 ↓ ${formatBytes(totDown)} · ↑ ${formatBytes(totUp)}${conns > 0 ? ` · ${conns} 连接` : ""}`;
  }

  return `
    <div class="outbound-rate-main mono ${active ? "is-active" : ""}">
      <span class="status-dot ${active ? "ok" : ""}"></span>
      <span class="rate-down">↓ ${formatSpeed(down)}</span>
      <span class="rate-up">↑ ${formatSpeed(up)}</span>
    </div>
    <div class="outbound-rate-sub mono">${subText}</div>
  `;
}

function renderOverview(root) {
  const st = store.state;
  const s = store.stats;
  const enabledNodes = activeNodes(st);
  const usableSubs = (st.subscriptions || []).filter((sub) => sub.enabled && enabledNodes.some((node) => node.subId === sub.id));

  root.innerHTML = `
    <div class="section-top">
      <div>
        <h1 class="section-title">运行概览</h1>
        <p class="section-sub">查看透明代理连通状态、实时速率，或直接点击下方节点切换出口</p>
      </div>
      <div class="toolbar">
        <button class="btn btn-sm" id="ov-btn-conn">检测连通性</button>
        <button class="btn btn-sm" id="ov-btn-probe" ${store.probing ? "disabled" : ""}>${store.probing ? "测速中..." : "全节点测速"}</button>
        <button class="btn btn-sm" id="ov-btn-reload">热重载</button>
        ${
          s.running
            ? `<button class="btn btn-sm btn-danger-text" id="ov-btn-stop">停止服务</button>`
            : `<button class="btn btn-sm btn-primary" id="ov-btn-start">启动服务</button>`
        }
      </div>
    </div>

    <!-- 4-Site Real Connectivity Bar -->
    <div id="ov-conn-bar" class="conn-bar"></div>

    <!-- Strategy Groups Instant Node Selector -->
    ${st.groups
      .map((grp, gIdx) => {
        if (!grp.enabled) return "";
        const matched = matchNodesForGroup(grp, enabledNodes);
        const pinned = enabledNodes.find((n) => grp.pinnedNodeId ? n.id === grp.pinnedNodeId : grp.pinnedNodeTag && n.tag === grp.pinnedNodeTag);
        const candidates = pinned && !matched.some((n) => n.id === pinned.id) ? [pinned, ...matched] : matched;
        const isAuto = grp.policy !== "fixed";
        const bestAuto = [...candidates]
          .filter((n) => n.latencyMs > 0)
          .sort((a, b) => a.latencyMs - b.latencyMs)[0];
        const grpTraffic = computeGroupTraffic(grp, gIdx, candidates, pinned, isAuto, enabledNodes);
        const epMap = store.stats?.endpointTraffic || {};

        return `
          <div class="panel outbound-panel">
            <div class="panel-head outbound-head">
              <div class="outbound-title-wrap">
                <div class="outbound-heading">
                  <h2 class="panel-title">${escapeHTML(grp.name)}</h2>
                  <span class="panel-desc">${candidates.length} 个候选 · ${isAuto ? "自动选择" : "固定节点"}</span>
                </div>
                <div class="outbound-live-rate ${(grpTraffic?.downloadBps || 0) + (grpTraffic?.uploadBps || 0) > 0 ? "is-flowing" : ""}" data-grp-rate="${gIdx}">
                  ${renderGroupRateHTML(grpTraffic, isAuto)}
                </div>
              </div>
              <div class="outbound-controls">
                <label class="source-control">订阅源
                  <select class="select select-sm" aria-label="订阅源" data-grp-source="${gIdx}">
                    <option value="">选择订阅源…</option>
                    ${usableSubs.map((sub) => `<option value="${escapeHTML(sub.id)}" ${grp.filter?.subIds?.[0] === sub.id ? "selected" : ""}>${escapeHTML(sub.name || sub.tag)}</option>`).join("")}
                  </select>
                </label>
                <button class="btn btn-sm" data-grp-auto="${gIdx}" ${isAuto || !matched.length ? "disabled" : ""}>自动选择</button>
              </div>
            </div>
            <div class="outbound-current">${isAuto
              ? (bestAuto ? "测速参考 · " + escapeHTML(bestAuto.name) : "由 dae 根据策略选择出口")
              : (pinned || candidates[grp.fixedIndex || 0] ? "固定出口 · " + escapeHTML((pinned || candidates[grp.fixedIndex || 0]).name) : "固定节点不可用，请选择新节点")}</div>
            ${
              candidates.length === 0
                ? `<div class="empty-box">该策略组暂无匹配节点，请前往「节点与订阅」添加节点或在「策略组管理」调整筛选规则。</div>`
                : `${candidates.length > 10 ? '<div class="outbound-search"><input class="input input-sm" type="search" aria-label="筛选节点" placeholder="搜索节点名称或协议" data-outbound-search></div>' : ""}
                  <div class="selector-grid">
                    ${candidates
                      .map((n) => {
                        const selected = !isAuto
                          ? (pinned ? pinned.id === n.id : !grp.pinnedNodeId && !grp.pinnedNodeTag && candidates[grp.fixedIndex || 0]?.id === n.id)
                          : false;
                        const nEp = nodeEndpointKey(n);
                        const nStat = (nEp && epMap[nEp]) || null;
                        const nActive = (nStat?.downloadBps || 0) + (nStat?.uploadBps || 0) >= 512;
                        const searchMeta = `${n.name || ""} ${n.protocol || ""} ${regionLabel(n.region)}`;
                        return `
                          <button type="button" class="selector-item ${selected ? "selected" : ""}" data-pick-grp="${gIdx}" data-pick-id="${escapeHTML(n.id)}" data-search-text="${escapeHTML(searchMeta.toLowerCase())}" aria-pressed="${selected}">
                            <div style="min-width:0;">
                              <div class="selector-item-title">${escapeHTML(n.name)}</div>
                              <div class="selector-item-sub mono">
                                <span>${escapeHTML((n.protocol || "").toUpperCase())} · ${regionLabel(n.region)}</span>
                                <span class="node-live-rate ${nActive ? "" : "hidden"}" data-node-rate="${escapeHTML(n.id)}">${nActive ? ` · ↓ ${formatSpeed(nStat.downloadBps)}` : ""}</span>
                              </div>
                            </div>
                            ${latencyTagHTML(n.latencyMs)}
                          </button>
                        `;
                      })
                      .join("")}
                  </div>`
            }
          </div>
        `;
      })
      .join("")}

    <!-- Runtime metrics -->
    <div class="metrics-row">
      <div class="metric-box">
        <div class="metric-label">
          <span>实时吞吐速率</span>
          <span class="mono text-muted" id="ov-total-bytes">总计 ↓ ${formatBytes(s.totalDownload)} · ↑ ${formatBytes(s.totalUpload)}</span>
        </div>
        <div style="display:flex;align-items:baseline;gap:16px;">
          <div class="metric-val mono" id="ov-down-bps">↓ ${formatSpeed(s.downloadBps)}</div>
          <div class="text-sec mono" id="ov-up-bps">↑ ${formatSpeed(s.uploadBps)}</div>
        </div>
        <div id="ov-chart">${chart.renderSVG()}</div>
      </div>

      <div class="metric-box">
        <div class="metric-label">
          <span>dae 内核引擎</span>
          <span class="tag ${s.running ? "tag-ok" : "tag-err"}">${s.running ? "运行中" : "未运行"}</span>
        </div>
        <div class="metric-val mono" id="ov-uptime">${s.running ? formatUptime(s.uptimeSeconds) : "已停止"}</div>
        <div class="metric-meta mono" id="ov-proc-meta">
          <span class="meta-chip">PID <strong>${s.pid || "-"}</strong></span>
          <span class="meta-chip">内存 <strong>${s.memoryMb} MB</strong></span>
          <span class="meta-chip">连接 <strong>${s.activeConns}</strong></span>
        </div>
      </div>

      <div class="metric-box">
        <div class="metric-label">
          <span>分流工作模式</span>
          <span class="mono tag tag-primary">${escapeHTML(st.global.dialMode)}</span>
        </div>
        <div class="metric-val">${enabledNodes.length} <span style="font-size: 15px;font-weight:500;color:var(--text-sec);">个可用节点</span></div>
        <div class="metric-meta">
          <span class="meta-chip">LAN <strong class="mono">${escapeHTML((st.global.lanInterfaces || []).join(",") || "无")}</strong></span>
          <span class="meta-chip">WAN <strong class="mono">${escapeHTML(st.global.wanInterface || "auto")}</strong></span>
          <span class="meta-chip">兜底 <strong class="mono">${escapeHTML(st.routing.fallback)}</strong></span>
        </div>
      </div>
    </div>
  `;

  renderConnectivityBarDOM();

  root.querySelector("#ov-btn-conn")?.addEventListener("click", () => checkConnectivitySilent(true));
  root.querySelector("#ov-btn-probe")?.addEventListener("click", () => probeNodes());
  root.querySelector("#ov-btn-reload")?.addEventListener("click", () => runServiceAction("reload"));
  root.querySelector("#ov-btn-start")?.addEventListener("click", () => runServiceAction("start"));
  root.querySelector("#ov-btn-stop")?.addEventListener("click", () => runServiceAction("stop"));

  root.querySelectorAll("[data-grp-auto]").forEach((btn) => {
    btn.addEventListener("click", () => switchGroup(Number(btn.dataset.grpAuto), "自动选择", (group) => {
      group.policy = "min_moving_avg";
      clearPin(group);
    }));
  });

  root.querySelectorAll("[data-grp-source]").forEach((select) => {
    select.addEventListener("change", async () => {
      const sub = usableSubs.find((item) => item.id === select.value);
      if (!sub) return;
      await switchGroup(Number(select.dataset.grpSource), sub.name || sub.tag, (group) => {
        group.mode = "smart";
        group.filter = {
          ...(group.filter || {}),
          subIds: [sub.id],
          subTags: [],
          manualNodeTags: [],
        };
        group.policy = "min_moving_avg";
        clearPin(group);
      });
      if (select.isConnected) select.value = st.groups[Number(select.dataset.grpSource)].filter?.subIds?.[0] || "";
    });
  });

  root.querySelectorAll("[data-outbound-search]").forEach((input) => {
    input.addEventListener("input", () => {
      const query = input.value.trim().toLowerCase();
      for (const card of input.closest(".outbound-panel").querySelectorAll("[data-pick-grp]")) {
        const hay = card.dataset.searchText || card.textContent.toLowerCase();
        card.classList.toggle("hidden", !hay.includes(query));
      }
    });
  });

  root.querySelectorAll("[data-pick-grp]").forEach((card) => {
    card.addEventListener("click", () => {
      const node = enabledNodes.find((n) => n.id === card.dataset.pickId);
      if (!node || card.getAttribute("aria-pressed") === "true") return;
      return switchGroup(Number(card.dataset.pickGrp), node.name, (group) => {
        clearPin(group);
        group.policy = "fixed";
        group.pinnedNodeId = node.id;
        group.pinnedNodeTag = node.tag;
      });
    });
  });
}

function renderConnectivityBarDOM() {
  const bar = document.getElementById("ov-conn-bar");
  if (!bar) return;
  const items = store.connectivity || [
    { name: "国内直连 · 百度", ok: null, latencyMs: 0 },
    { name: "海外连通 · Cloudflare", ok: null, latencyMs: 0 },
    { name: "Google", ok: null, latencyMs: 0 },
    { name: "GitHub", ok: null, latencyMs: 0 },
  ];
  bar.innerHTML = items
    .map((it) => {
      const dotCls = store.checkingConn || it.ok === null ? "" : it.ok ? "ok" : "fail";
      const cardCls = store.checkingConn || it.ok === null ? "" : it.ok ? "is-ok" : "is-fail";
      const rightHTML =
        store.checkingConn || it.ok === null
          ? `<span class="tag mono">检测中…</span>`
          : it.ok
          ? `<span class="tag tag-ok mono">${it.latencyMs} ms</span>`
          : `<span class="tag tag-err">不通</span>`;
      return `
        <div class="conn-item ${cardCls}">
          <div class="conn-name">
            <span class="status-dot ${dotCls}"></span>
            <span>${escapeHTML(it.name)}</span>
          </div>
          ${rightHTML}
        </div>
      `;
    })
    .join("");
}

function updateOverviewLiveMetrics() {
  const s = store.stats;
  setText("ov-down-bps", `↓ ${formatSpeed(s.downloadBps)}`);
  setText("ov-up-bps", `↑ ${formatSpeed(s.uploadBps)}`);
  setText("ov-total-bytes", `总计 ↓ ${formatBytes(s.totalDownload)} · ↑ ${formatBytes(s.totalUpload)}`);
  setText("ov-uptime", s.running ? formatUptime(s.uptimeSeconds) : "已停止");
  const meta = document.getElementById("ov-proc-meta");
  if (meta) {
    meta.innerHTML = `<span class="meta-chip">PID <strong>${s.pid || "-"}</strong></span><span class="meta-chip">内存 <strong>${s.memoryMb} MB</strong></span><span class="meta-chip">连接 <strong>${s.activeConns}</strong></span>`;
  }
  const chartBox = document.getElementById("ov-chart");
  if (chartBox) chartBox.innerHTML = chart.renderSVG();

  if (store.state) {
    const enabledNodes = activeNodes(store.state);
    const epMap = s.endpointTraffic || {};
    const nodeById = new Map(enabledNodes.map((n) => [n.id, n]));

    (store.state.groups || []).forEach((grp, gIdx) => {
      if (!grp.enabled) return;
      const box = document.querySelector(`[data-grp-rate="${gIdx}"]`);
      if (!box) return;
      const matched = matchNodesForGroup(grp, enabledNodes);
      const pinned = enabledNodes.find((n) =>
        grp.pinnedNodeId ? n.id === grp.pinnedNodeId : grp.pinnedNodeTag && n.tag === grp.pinnedNodeTag
      );
      const candidates = pinned && !matched.some((n) => n.id === pinned.id) ? [pinned, ...matched] : matched;
      const isAuto = grp.policy !== "fixed";
      const traffic = computeGroupTraffic(grp, gIdx, candidates, pinned, isAuto, enabledNodes);
      box.classList.toggle("is-flowing", (traffic?.downloadBps || 0) + (traffic?.uploadBps || 0) > 0);
      box.innerHTML = renderGroupRateHTML(traffic, isAuto);
    });

    document.querySelectorAll("[data-node-rate]").forEach((el) => {
      const n = nodeById.get(el.dataset.nodeRate);
      const ep = nodeEndpointKey(n);
      const stItem = (ep && epMap[ep]) || null;
      const active = (stItem?.downloadBps || 0) + (stItem?.uploadBps || 0) >= 512;
      el.classList.toggle("hidden", !active);
      el.textContent = active ? ` · ↓ ${formatSpeed(stItem.downloadBps)}` : "";
    });
  }
}

// ============================================================================
// 2. Proxies & Subscriptions (Clean Unified Table View)
// ============================================================================

async function probeNodes(nodeIds = []) {
  if (store.probing) return;
  store.probing = true;
  renderPage();
  try {
    const res = await apiFetch("/api/v1/nodes/probe", {
      method: "POST",
      body: JSON.stringify({ nodeIds, timeoutMs: 2500 }),
    });
    if (res.nodes) {
      store.state.nodes = res.nodes;
      const okCount = (res.results || []).filter((r) => r.alive).length;
      toast(`测速完成：${okCount} / ${(res.results || []).length} 个节点响应正常`, "success");
    }
  } catch (err) {
    toast("测速失败: " + err.message, "error");
  } finally {
    store.probing = false;
    renderPage();
  }
}

function renderProxiesAndSubs(root) {
  const subs = store.state.subscriptions || [];
  const allNodes = store.state.nodes || [];
  const nf = store.nodeFilter;
  const protocols = ["ALL", ...new Set(allNodes.map((n) => (n.protocol || "").toLowerCase()).filter(Boolean))];

  const filtered = allNodes
    .filter((n) => {
      if (nf.region !== "ALL" && (n.region || "OTHER") !== nf.region) return false;
      if (nf.protocol !== "ALL" && (n.protocol || "").toLowerCase() !== nf.protocol) return false;
      if (nf.keyword) {
        const q = nf.keyword.toLowerCase();
        if (!`${n.name} ${n.address} ${n.tag} ${n.subTag}`.toLowerCase().includes(q)) return false;
      }
      return true;
    })
    .sort((a, b) => {
      if (nf.sortBy === "name") return (a.name || "").localeCompare(b.name || "");
      const la = a.latencyMs > 0 ? a.latencyMs : a.latencyMs === -1 ? 99998 : 99999;
      const lb = b.latencyMs > 0 ? b.latencyMs : b.latencyMs === -1 ? 99998 : 99999;
      return la - lb;
    });

  root.innerHTML = `
    <div class="section-top">
      <div>
        <h1 class="section-title">节点与订阅</h1>
        <p class="section-sub">支持多格式远程订阅自动转换（Clash / Sing-box / Base64 / V2Ray / Shadowrocket）与手动单节点管理</p>
      </div>
      <div class="toolbar">
        <button class="btn btn-sm" id="px-btn-add-sub">添加订阅源</button>
        <button class="btn btn-sm" id="px-btn-add-node">添加 / 批量导入节点</button>
        <button class="btn btn-sm btn-primary" id="px-btn-probe-all" ${store.probing ? "disabled" : ""}>
          ${store.probing ? "正在测速..." : "测试全部延迟"}
        </button>
      </div>
    </div>

    <!-- Subscriptions Table -->
    <div class="panel">
      <div class="panel-head">
        <span class="panel-title">订阅源 (${subs.length})</span>
        <span class="panel-desc">开启自动更新后，后台将按设定小时数自动拉取并热重载</span>
      </div>
      ${
        subs.length === 0
          ? `<div class="empty-box" style="padding:22px;">暂无远程订阅源，可点击右上角「添加订阅源」导入节点订阅。</div>`
          : `<div class="table-wrap">
              <table class="data-table">
                <thead>
                  <tr>
                    <th style="width:60px;">启用</th>
                    <th>订阅名称</th>
                    <th>格式</th>
                    <th>节点数</th>
                    <th>已用流量</th>
                    <th>自动更新</th>
                    <th style="text-align:right;">操作</th>
                  </tr>
                </thead>
                <tbody>
                  ${subs
                    .map((s, idx) => {
                      const used = (s.usedUploadBytes || 0) + (s.usedDownloadBytes || 0);
                      const total = s.totalTrafficBytes || 0;
                      const pct = total > 0 ? Math.min(100, Math.round((used / total) * 100)) : 0;
                      return `
                        <tr class="${s.enabled ? "" : "row-disabled"}">
                          <td>
                            <label class="toggle">
                              <input type="checkbox" data-sub-tog="${idx}" ${s.enabled ? "checked" : ""}/>
                              <span class="toggle-slider"></span>
                            </label>
                          </td>
                          <td>
                            <div style="font-weight:500;">${escapeHTML(s.name || s.tag)}</div>
                            <div class="text-muted mono" style="font-size: 14px;max-width:340px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;">${escapeHTML(s.url ? "远程订阅" : "离线导入")}</div>
                          </td>
                          <td><span class="tag mono">${escapeHTML((s.format || "auto").toUpperCase())}</span></td>
                          <td class="mono">${s.nodeCount || 0}</td>
                          <td>
                            ${
                              total > 0
                                ? `<div class="mono" style="font-size: 13.5px;">${formatBytes(used)} / ${formatBytes(total)} <span class="text-muted">(${pct}%)</span></div>
                                   <div class="quota-bar"><div class="quota-fill" style="width:${pct}%"></div></div>`
                                : `<span class="text-muted">—</span>`
                            }
                          </td>
                          <td class="mono">${s.autoUpdateHours > 0 ? `每 ${s.autoUpdateHours} 小时` : "手动"}</td>
                          <td style="text-align:right;white-space:nowrap;">
                            ${s.url ? `<button class="btn btn-sm btn-subtle" data-sub-sync="${idx}">更新</button>` : ""}
                            <button class="btn btn-sm btn-subtle" data-sub-edit="${idx}">编辑</button>
                            <button class="btn btn-sm btn-danger-text" data-sub-del="${idx}">删除</button>
                          </td>
                        </tr>
                      `;
                    })
                    .join("")}
                </tbody>
              </table>
            </div>`
      }
    </div>

    <!-- Nodes Table -->
    <div class="panel">
      <div class="panel-head">
        <div class="toolbar">
          <span class="panel-title">节点列表 (${filtered.length}/${allNodes.length})</span>
          <input id="nd-search" class="input input-sm" style="width:210px;" placeholder="搜索名称 / 地址 / 标签..." value="${escapeHTML(nf.keyword)}" />
          <select id="nd-region" class="select select-sm" style="width:115px;">
            <option value="ALL" ${nf.region === "ALL" ? "selected" : ""}>全部地区</option>
            ${REGION_OPTIONS.map((r) => `<option value="${r.code}" ${nf.region === r.code ? "selected" : ""}>${r.label}</option>`).join("")}
            <option value="OTHER" ${nf.region === "OTHER" ? "selected" : ""}>其他地区</option>
          </select>
          <select id="nd-proto" class="select select-sm" style="width:120px;">
            ${protocols.map((p) => `<option value="${p}" ${nf.protocol === p ? "selected" : ""}>${p === "ALL" ? "全部协议" : p.toUpperCase()}</option>`).join("")}
          </select>
        </div>
        <select id="nd-sort" class="select select-sm" style="width:140px;">
          <option value="latency" ${nf.sortBy === "latency" ? "selected" : ""}>按延迟升序</option>
          <option value="name" ${nf.sortBy === "name" ? "selected" : ""}>按名称排序</option>
        </select>
      </div>

      ${
        filtered.length === 0
          ? `<div class="empty-box">未找到匹配的节点</div>`
          : `<div class="table-wrap">
              <table class="data-table">
                <thead>
                  <tr>
                    <th style="width:60px;">启用</th>
                    <th>节点名称</th>
                    <th>来源</th>
                    <th>协议</th>
                    <th>服务器地址</th>
                    <th>延迟</th>
                    <th style="text-align:right;">操作</th>
                  </tr>
                </thead>
                <tbody>
                  ${filtered
                    .map(
                      (n) => `
                      <tr class="${n.enabled !== false ? "" : "row-disabled"}">
                        <td>
                          <label class="toggle">
                            <input type="checkbox" data-nd-tog="${escapeHTML(n.id)}" ${n.enabled !== false ? "checked" : ""}/>
                            <span class="toggle-slider"></span>
                          </label>
                        </td>
                        <td>
                          <div style="font-weight:600;">${escapeHTML(n.name)}</div>
                          <div class="text-muted mono" style="font-size: 12.5px;">tag: ${escapeHTML(n.tag)}</div>
                        </td>
                        <td><span class="tag">${escapeHTML(n.subTag || "手动添加")}</span></td>
                        <td><span class="tag tag-primary proto-${escapeHTML((n.protocol || "").toLowerCase())} mono">${escapeHTML((n.protocol || "").toUpperCase())}</span></td>
                        <td class="mono text-sec" style="font-size: 13.5px;">${escapeHTML(n.address)}:${n.port}</td>
                        <td>${latencyTagHTML(n.latencyMs)}</td>
                        <td style="text-align:right;white-space:nowrap;">
                          <button class="btn btn-sm btn-subtle" data-nd-probe="${escapeHTML(n.id)}">测速</button>
                          <button class="btn btn-sm btn-subtle" data-nd-edit="${escapeHTML(n.id)}">编辑</button>
                          <button class="btn btn-sm btn-subtle" data-nd-copy="${escapeHTML(n.id)}">复制</button>
                          <button class="btn btn-sm btn-danger-text" data-nd-del="${escapeHTML(n.id)}">删除</button>
                        </td>
                      </tr>
                    `
                    )
                    .join("")}
                </tbody>
              </table>
            </div>`
      }
    </div>
  `;

  root.querySelector("#px-btn-add-sub")?.addEventListener("click", () => openSubModal(null));
  root.querySelector("#px-btn-add-node")?.addEventListener("click", () => openNodeModal(null));
  root.querySelector("#px-btn-probe-all")?.addEventListener("click", () => probeNodes());

  // Subscription events
  root.querySelectorAll("[data-sub-tog]").forEach((chk) => {
    chk.addEventListener("change", () => {
      subs[Number(chk.dataset.subTog)].enabled = chk.checked;
      reconcileGroupsWithActivePool();
      markDirty();
      renderProxiesAndSubs(root);
    });
  });
  root.querySelectorAll("[data-sub-edit]").forEach((btn) => {
    btn.addEventListener("click", () => openSubModal(subs[Number(btn.dataset.subEdit)]));
  });
  root.querySelectorAll("[data-sub-sync]").forEach((btn) => {
    btn.addEventListener("click", async () => {
      const sub = subs[Number(btn.dataset.subSync)];
      btn.disabled = true;
      btn.textContent = "更新中";
      try {
        const res = await apiFetch("/api/v1/subscriptions/parse", { timeoutMs: 25000, method: "POST", body: JSON.stringify(sub) });
        if (!res.ok) toast(res.error || "更新失败", "error");
        else {
          mergePoolFromServerState(res.state, res.configDae);
          markDirty();
          toast(`已同步 ${res.addedCount} 个节点`, "success");
          renderProxiesAndSubs(root);
        }
      } catch (err) {
        toast("更新失败：" + err.message, "error");
      } finally {
        btn.disabled = false;
        if (btn.isConnected) btn.textContent = "更新";
      }
    });
  });
  root.querySelectorAll("[data-sub-del]").forEach((btn) => {
    btn.addEventListener("click", () => {
      const removed = subs.splice(Number(btn.dataset.subDel), 1)[0];
      if (removed) store.state.nodes = store.state.nodes.filter((n) => n.subId !== removed.id);
      reconcileGroupsWithActivePool();
      markDirty();
      renderProxiesAndSubs(root);
    });
  });

  // Filter events
  root.querySelector("#nd-search")?.addEventListener("input", (e) => {
    nf.keyword = e.target.value;
    renderProxiesAndSubs(root);
    const inp = root.querySelector("#nd-search");
    inp?.focus();
    inp?.setSelectionRange(inp.value.length, inp.value.length);
  });
  root.querySelector("#nd-region")?.addEventListener("change", (e) => {
    nf.region = e.target.value;
    renderProxiesAndSubs(root);
  });
  root.querySelector("#nd-proto")?.addEventListener("change", (e) => {
    nf.protocol = e.target.value;
    renderProxiesAndSubs(root);
  });
  root.querySelector("#nd-sort")?.addEventListener("change", (e) => {
    nf.sortBy = e.target.value;
    renderProxiesAndSubs(root);
  });

  // Node row actions
  root.querySelectorAll("[data-nd-tog]").forEach((chk) => {
    chk.addEventListener("change", () => {
      const n = store.state.nodes.find((x) => x.id === chk.dataset.ndTog);
      if (n) {
        n.enabled = chk.checked;
        reconcileGroupsWithActivePool();
        markDirty();
        renderProxiesAndSubs(root);
      }
    });
  });
  root.querySelectorAll("[data-nd-probe]").forEach((btn) => {
    btn.addEventListener("click", () => probeNodes([btn.dataset.ndProbe]));
  });
  root.querySelectorAll("[data-nd-edit]").forEach((btn) => {
    const n = store.state.nodes.find((x) => x.id === btn.dataset.ndEdit);
    if (n) btn.addEventListener("click", () => openNodeModal(n));
  });
  root.querySelectorAll("[data-nd-copy]").forEach((btn) => {
    btn.addEventListener("click", () => {
      const n = store.state.nodes.find((x) => x.id === btn.dataset.ndCopy);
      if (n?.uri) {
        navigator.clipboard?.writeText(n.uri);
        toast("节点链接已复制", "success");
      }
    });
  });
  root.querySelectorAll("[data-nd-del]").forEach((btn) => {
    btn.addEventListener("click", () => {
      store.state.nodes = store.state.nodes.filter((x) => x.id !== btn.dataset.ndDel);
      reconcileGroupsWithActivePool();
      markDirty();
      renderProxiesAndSubs(root);
    });
  });
}

function openSubModal(existing) {
  const s = existing || {
    id: "",
    name: "",
    tag: "sub_main",
    url: "",
    userAgent: "clash.meta",
    autoUpdateHours: 6,
    excludeKeywords: "剩余流量|到期|官网",
    persistConverted: false,
  };
  openModal({
    title: existing ? "编辑订阅源" : "添加订阅源",
    bodyHTML: `
      <div class="form-grid">
        <div class="field">
          <label class="field-label">订阅名称</label>
          <input id="m-sub-name" class="input" value="${escapeHTML(s.name)}" placeholder="主力订阅" />
        </div>
        <div class="field">
          <label class="field-label">内部标识 Tag</label>
          <input id="m-sub-tag" class="input mono" value="${escapeHTML(s.tag)}" placeholder="sub_main" />
        </div>
        <div class="field full">
          <label class="field-label">远程订阅链接 URL</label>
          <input id="m-sub-url" class="input mono" value="${escapeHTML(s.url)}" placeholder="https://..." />
        </div>
        <div class="field">
          <label class="field-label">请求 User-Agent</label>
          <select id="m-sub-ua" class="select">
            ${["clash.meta", "sing-box", "v2rayN/6.45", "Shadowrocket/2024", "dae"].map(
              (u) => `<option value="${u}" ${s.userAgent === u ? "selected" : ""}>${u}</option>`
            ).join("")}
          </select>
        </div>
        <div class="field">
          <label class="field-label">自动更新间隔 (小时, 0 为不自动更新)</label>
          <input id="m-sub-hrs" type="number" min="0" class="input mono" value="${s.autoUpdateHours ?? 6}" />
        </div>
        <div class="field full">
          <label class="field-label">排除节点名称正则</label>
          <input id="m-sub-exc" class="input" value="${escapeHTML(s.excludeKeywords || "")}" placeholder="剩余流量|套餐到期|官网" />
        </div>
        <div class="field full">
          <label class="field-label">离线订阅内容 (若上方未填 URL，可直接在此粘贴 Clash YAML / Sing-box / Base64)</label>
          <textarea id="m-sub-raw" class="textarea mono" rows="5" placeholder="可选..."></textarea>
        </div>
      </div>
    `,
    footerHTML: `
      <button class="btn" id="m-cancel">取消</button>
      <button class="btn btn-primary" id="m-ok">保存并解析</button>
    `,
    onMount(box) {
      box.querySelector("#m-cancel")?.addEventListener("click", closeModal);
      box.querySelector("#m-ok")?.addEventListener("click", async () => {
        const okBtn = box.querySelector("#m-ok");
        okBtn.disabled = true;
        okBtn.textContent = "拉取解析中...";
        try {
          const payload = {
            id: s.id,
            name: box.querySelector("#m-sub-name").value.trim() || "订阅源",
            tag: box.querySelector("#m-sub-tag").value.trim() || "sub_main",
            url: box.querySelector("#m-sub-url").value.trim(),
            userAgent: box.querySelector("#m-sub-ua").value,
            autoUpdateHours: Number(box.querySelector("#m-sub-hrs").value) || 0,
            excludeKeywords: box.querySelector("#m-sub-exc").value.trim(),
            rawContent: box.querySelector("#m-sub-raw").value.trim(),
            persistConverted: s.persistConverted,
          };
          if (existing && !payload.rawContent && payload.url === existing.url) {
            Object.assign(existing, payload);
            markDirty();
            closeModal();
            renderPage();
            return;
          }
          const res = await apiFetch("/api/v1/subscriptions/parse", { timeoutMs: 25000, method: "POST", body: JSON.stringify(payload) });
          if (!res.ok) {
            toast(res.error || "订阅解析失败", "error");
            return;
          }
          mergePoolFromServerState(res.state, res.configDae);
          markDirty();
          closeModal();
          toast(`成功解析 ${res.addedCount} 个节点`, "success");
          renderPage();
        } finally {
          okBtn.disabled = false;
          okBtn.textContent = "保存并解析";
        }
      });
    },
  });
}

function openNodeModal(existing) {
  if (existing) {
    openModal({
      title: "编辑节点",
      bodyHTML: `
        <div class="form-grid">
          <div class="field">
            <label class="field-label">节点名称</label>
            <input id="m-nd-name" class="input" value="${escapeHTML(existing.name)}" />
          </div>
          <div class="field">
            <label class="field-label">节点标识 Tag</label>
            <input id="m-nd-tag" class="input mono" value="${escapeHTML(existing.tag)}" />
          </div>
          <div class="field full">
            <label class="field-label">节点标准分享链接 URI</label>
            <textarea id="m-nd-uri" class="textarea mono" rows="4">${escapeHTML(existing.uri)}</textarea>
          </div>
        </div>
      `,
      footerHTML: `<button class="btn" id="m-c">取消</button><button class="btn btn-primary" id="m-s">保存节点</button>`,
      onMount(box) {
        box.querySelector("#m-c")?.addEventListener("click", closeModal);
        const saveBtn = box.querySelector("#m-s");
        saveBtn?.addEventListener("click", async () => {
          saveBtn.disabled = true;
          try {
            const res = await apiFetch("/api/v1/nodes/save", {
              method: "POST",
              body: JSON.stringify({
                id: existing.id,
                name: box.querySelector("#m-nd-name").value.trim(),
                tag: box.querySelector("#m-nd-tag").value.trim(),
                uri: box.querySelector("#m-nd-uri").value.trim(),
              }),
            });
            if (!res.ok) {
              toast(res.error || "保存失败", "error");
              return;
            }
            mergePoolFromServerState(res.state, res.configDae);
            markDirty();
            closeModal();
            renderPage();
          } catch (err) {
            toast("保存失败：" + err.message, "error");
          } finally {
            saveBtn.disabled = false;
          }
        });
      },
    });
    return;
  }

  openModal({
    title: "添加或批量导入节点",
    bodyHTML: `
      <div class="field full">
        <label class="field-label">粘贴单条或多条节点链接（支持 vless://, hysteria2://, trojan://, vmess://, ss://, tuic:// 或 Shadowrocket / Base64）</label>
        <textarea id="m-batch-uri" class="textarea mono" rows="7" placeholder="vless://uuid@[ipv6]:443?security=reality&flow=xtls-rprx-vision&sni=...#NodeName"></textarea>
      </div>
    `,
    footerHTML: `<button class="btn" id="m-c">取消</button><button class="btn btn-primary" id="m-s">解析导入</button>`,
    onMount(box) {
      box.querySelector("#m-c")?.addEventListener("click", closeModal);
      const saveBtn = box.querySelector("#m-s");
      saveBtn?.addEventListener("click", async () => {
        const rawText = box.querySelector("#m-batch-uri").value.trim();
        if (!rawText) return;
        saveBtn.disabled = true;
        try {
          const res = await apiFetch("/api/v1/nodes/import", { method: "POST", body: JSON.stringify({ rawText }) });
          if (!res.ok) {
            toast(res.error || "导入失败", "error");
            return;
          }
          mergePoolFromServerState(res.state, res.configDae);
          markDirty();
          closeModal();
          toast(`已添加 ${res.addedCount} 个节点，请点击右上角「保存并应用」生效`, "success");
          renderPage();
        } catch (err) {
          toast("导入失败：" + err.message, "error");
        } finally {
          saveBtn.disabled = false;
        }
      });
    },
  });
}

// ============================================================================
// 3. Strategy Groups (Clean Master-Detail Split)
// ============================================================================

function renderGroups(root) {
  const groups = store.state.groups || [];
  if (store.selectedGroupIdx >= groups.length) store.selectedGroupIdx = 0;
  const g = groups[store.selectedGroupIdx];
  const pool = activeNodes();
  const matched = g ? matchNodesForGroup(g, pool) : [];
  const dslPreview = g ? buildGroupFilterPreview(g, pool, store.state.subscriptions) : "";

  root.innerHTML = `
    <div class="section-top">
      <div>
        <h1 class="section-title">策略组管理</h1>
        <p class="section-sub">定义不同的节点池（如默认代理、AI 专用组、流媒体组）供分流规则调用</p>
      </div>
      <button class="btn btn-sm btn-primary" id="grp-add">新建策略组</button>
    </div>

    <div class="group-layout">
      <div class="panel">
        ${groups
          .map(
            (item, idx) => `
            <button class="group-nav-item ${idx === store.selectedGroupIdx ? "active" : ""}" data-g-nav="${idx}">
              <span class="mono">${escapeHTML(item.name)}</span>
              <span class="tag mono">${matchNodesForGroup(item, activeNodes()).length}</span>
            </button>
          `
          )
          .join("")}
      </div>

      ${
        !g
          ? `<div class="panel empty-box">请新建策略组</div>`
          : `
        <div class="panel panel-pad" style="display:flex;flex-direction:column;gap:16px;">
          <div style="display:flex;justify-content:space-between;align-items:center;gap:12px;flex-wrap:wrap;">
            <div class="seg-tabs">
              <button class="seg-tab ${g.mode !== "raw" ? "active" : ""}" id="g-mode-smart">按条件筛选节点</button>
              <button class="seg-tab ${g.mode === "raw" ? "active" : ""}" id="g-mode-raw">原生 Filter DSL</button>
            </div>
            ${groups.length > 1 ? `<button class="btn btn-sm btn-danger-text" id="g-del">删除该组</button>` : ""}
          </div>

          <div class="form-grid">
            <div class="field">
              <label class="field-label">策略组名称 (英文标识)</label>
              <input id="g-name" class="input mono" value="${escapeHTML(g.name)}" />
            </div>
            <div class="field">
              <label class="field-label">负载均衡与选路方式</label>
              <select id="g-pol" class="select">
                <option value="min_moving_avg" ${g.policy === "min_moving_avg" ? "selected" : ""}>移动平均最低延迟 (min_moving_avg · 推荐)</option>
                <option value="min" ${g.policy === "min" ? "selected" : ""}>单次最低延迟 (min)</option>
                <option value="min_avg10" ${g.policy === "min_avg10" ? "selected" : ""}>近 10 次均值最低 (min_avg10)</option>
                <option value="random" ${g.policy === "random" ? "selected" : ""}>随机负载均衡 (random)</option>
                <option value="fixed" ${g.policy === "fixed" ? "selected" : ""}>手动固定节点 (fixed)</option>
              </select>
            </div>
            <div class="field full">
              <label class="field-label">备注说明</label>
              <input id="g-desc" class="input" value="${escapeHTML(g.description || "")}" placeholder="可选备注" />
            </div>
          </div>

          ${
            g.mode === "raw"
              ? `
            <div class="field full">
              <label class="field-label">原生 filter 表达式</label>
              <textarea id="g-raw" class="code-area" style="min-height:140px;">${escapeHTML(g.rawFilterDsl || dslPreview)}</textarea>
            </div>`
              : `
            <div class="field full">
              <label class="field-label">限定地区 (不选默认包含全部地区)</label>
              <div class="chips">
                ${REGION_OPTIONS.map((r) => {
                  const on = (g.filter?.regions || []).includes(r.code);
                  return `<button type="button" class="chip-btn ${on ? "active" : ""}" data-g-reg="${r.code}">${r.label}</button>`;
                }).join("")}
              </div>
            </div>

            <div class="form-grid">
              <div class="field">
                <label class="field-label">包含关键词 (逗号分隔)</label>
                <input id="g-inc" class="input" value="${escapeHTML((g.filter?.includeKeywords || []).join(", "))}" placeholder="如: IEPL, 专线" />
              </div>
              <div class="field">
                <label class="field-label">排除关键词 (逗号分隔)</label>
                <input id="g-exc" class="input" value="${escapeHTML((g.filter?.excludeKeywords || []).join(", "))}" placeholder="如: HK, 剩余流量" />
              </div>
            </div>

            <div class="field full">
              <label class="field-label">手动指定包含的节点 (点击多选)</label>
              <div class="chips">
                ${store.state.nodes
                  .map((n) => {
                    const on = (g.filter?.manualNodeTags || []).includes(n.tag);
                    return `<button type="button" class="chip-btn ${on ? "active" : ""}" data-g-man="${escapeHTML(n.tag)}">${escapeHTML(n.name)}</button>`;
                  })
                  .join("") || `<span class="field-hint">暂无节点</span>`}
              </div>
            </div>`
          }

          <div class="field full">
            <label class="field-label">生成的配置语法预览 (当前命中 ${matched.length} 个节点)</label>
            <pre class="code-area mono" style="min-height:64px;padding:10px 12px;">${escapeHTML(dslPreview)}</pre>
          </div>
        </div>
      `
      }
    </div>
  `;

  root.querySelectorAll("[data-g-nav]").forEach((b) => {
    b.addEventListener("click", () => {
      store.selectedGroupIdx = Number(b.dataset.gNav);
      renderGroups(root);
    });
  });

  root.querySelector("#grp-add")?.addEventListener("click", () => {
    groups.push({
      id: `grp-${Date.now()}`,
      name: `group_${groups.length + 1}`,
      description: "",
      enabled: true,
      policy: "min_moving_avg",
      mode: "smart",
      filter: { subTags: [], regions: [], includeKeywords: [], excludeKeywords: [], manualNodeTags: [] },
    });
    store.selectedGroupIdx = groups.length - 1;
    markDirty();
    renderGroups(root);
  });

  if (!g) return;
  g.filter = g.filter || {};

  root.querySelector("#g-del")?.addEventListener("click", () => {
    openDeleteGroupModal(store.selectedGroupIdx, root);
  });
  root.querySelector("#g-mode-smart")?.addEventListener("click", () => { g.mode = "smart"; markDirty(); renderGroups(root); });
  root.querySelector("#g-mode-raw")?.addEventListener("click", () => {
    if (!g.rawFilterDsl) g.rawFilterDsl = buildGroupFilterPreview(g, pool, store.state.subscriptions);
    g.mode = "raw";
    markDirty();
    renderGroups(root);
  });
  root.querySelector("#g-name")?.addEventListener("change", (e) => {
    const nextName = e.target.value.trim();
    if (!nextName || nextName === g.name) {
      e.target.value = g.name;
      return;
    }
    if (["direct", "must_direct", "block"].includes(nextName)) {
      toast(`「${nextName}」为系统保留出站名，请使用其他名称`, "error");
      e.target.value = g.name;
      return;
    }
    if (groups.some((other, idx) => idx !== store.selectedGroupIdx && other.name === nextName)) {
      toast(`已存在同名策略组「${nextName}」`, "error");
      e.target.value = g.name;
      return;
    }
    const prevName = g.name;
    g.name = nextName;
    reassignGroupReferences(store.state, prevName, nextName);
    markDirty();
    renderGroups(root);
  });
  root.querySelector("#g-pol")?.addEventListener("change", (e) => {
    g.policy = e.target.value;
    if (g.policy !== "fixed") clearPin(g);
    markDirty();
    renderGroups(root);
  });
  root.querySelector("#g-desc")?.addEventListener("change", (e) => { g.description = e.target.value; markDirty(); });
  root.querySelector("#g-raw")?.addEventListener("input", (e) => { g.rawFilterDsl = e.target.value; markDirty(); });

  root.querySelectorAll("[data-g-reg]").forEach((b) => {
    b.addEventListener("click", () => {
      g.filter.regions = toggleItem(g.filter.regions || [], b.dataset.gReg);
      markDirty();
      renderGroups(root);
    });
  });
  root.querySelectorAll("[data-g-man]").forEach((b) => {
    b.addEventListener("click", () => {
      g.filter.manualNodeTags = toggleItem(g.filter.manualNodeTags || [], b.dataset.gMan);
      markDirty();
      renderGroups(root);
    });
  });
  root.querySelector("#g-inc")?.addEventListener("change", (e) => { g.filter.includeKeywords = splitCSV(e.target.value); markDirty(); renderGroups(root); });
  root.querySelector("#g-exc")?.addEventListener("change", (e) => { g.filter.excludeKeywords = splitCSV(e.target.value); markDirty(); renderGroups(root); });
}

function collectGroupRuleReferences(state, groupIdx) {
  const groups = state?.groups || [];
  const targetGroup = groups[groupIdx];
  if (!targetGroup) return { matchedRules: [], fallbackAffected: false, defaultTarget: "direct" };

  const groupName = (targetGroup.name || "").trim();
  const remainingGroups = groups.filter((grp, idx) => idx !== groupIdx && grp.enabled !== false);
  const validTargets = new Set(["direct", "must_direct", "block", ...remainingGroups.map((grp) => grp.name)]);

  const rt = state?.routing || {};
  let currentFallback = (rt.fallback || "").trim();
  const matchedRules = [];
  let fallbackAffected = false;

  if (rt.mode === "dsl" && rt.rawDsl?.trim()) {
    let pendingComment = "";
    let seq = 0;
    for (const line of rt.rawDsl.split("\n")) {
      const trimmed = line.trim();
      if (!trimmed) {
        pendingComment = "";
        continue;
      }
      if (trimmed.startsWith("#") && !/^#\s*\[Disabled\]/i.test(trimmed)) {
        pendingComment = trimmed.replace(/^#+\s*/, "").trim();
        continue;
      }
      const fbMatch = trimmed.match(/^fallback\s*:\s*([A-Za-z0-9_-]+)\s*(?:#.*)?$/);
      if (fbMatch) {
        currentFallback = fbMatch[1];
        if (fbMatch[1] === groupName) fallbackAffected = true;
        pendingComment = "";
        continue;
      }
      const ruleMatch = trimmed.match(/^(#\s*\[Disabled\]\s*)?(.+?)\s*->\s*([A-Za-z0-9_-]+)\s*(?:#.*)?$/);
      if (ruleMatch) {
        seq++;
        if (ruleMatch[3] === groupName) {
          matchedRules.push({
            index: seq,
            name: pendingComment,
            expr: ruleMatch[2].trim(),
            enabled: !ruleMatch[1],
          });
        }
        pendingComment = "";
      }
    }
  } else {
    fallbackAffected = currentFallback === groupName;
    (rt.rules || []).forEach((r, idx) => {
      if ((r.outbound || "").trim() === groupName) {
        matchedRules.push({
          index: idx + 1,
          name: r.name || "",
          expr: renderRuleExprText(r),
          enabled: r.enabled !== false,
        });
      }
    });
  }

  const defaultTarget =
    currentFallback && currentFallback !== groupName && validTargets.has(currentFallback)
      ? currentFallback
      : remainingGroups[0]?.name || "direct";

  return { matchedRules, fallbackAffected, defaultTarget };
}

function reassignGroupReferences(state, fromName, toName) {
  if (!state?.routing || !fromName || !toName || fromName === toName) return 0;
  let moved = 0;
  const rt = state.routing;

  for (const r of rt.rules || []) {
    if ((r.outbound || "").trim() === fromName) {
      r.outbound = toName;
      moved++;
    }
  }
  if ((rt.fallback || "").trim() === fromName) {
    rt.fallback = toName;
    moved++;
  }
  if (rt.rawDsl) {
    rt.rawDsl = rt.rawDsl
      .split("\n")
      .map((line) => {
        const trimmed = line.trim();
        if (trimmed.startsWith("#") && !/^#\s*\[Disabled\]/i.test(trimmed)) {
          return line;
        }
        const fbMatch = line.match(/^(\s*fallback\s*:\s*)([A-Za-z0-9_-]+)(\s*(?:#.*)?)$/);
        if (fbMatch && fbMatch[2] === fromName) {
          return `${fbMatch[1]}${toName}${fbMatch[3] || ""}`;
        }
        const ruleMatch = line.match(/^(\s*(?:#\s*\[Disabled\]\s*)?.+?->\s*)([A-Za-z0-9_-]+)(\s*(?:#.*)?)$/);
        if (ruleMatch && ruleMatch[2] === fromName) {
          return `${ruleMatch[1]}${toName}${ruleMatch[3] || ""}`;
        }
        return line;
      })
      .join("\n");
  }
  return moved;
}

function openDeleteGroupModal(groupIdx, root) {
  const groups = store.state?.groups || [];
  const g = groups[groupIdx];
  if (!g || groups.length <= 1) return;

  const { matchedRules, fallbackAffected, defaultTarget } = collectGroupRuleReferences(store.state, groupIdx);
  const totalCount = matchedRules.length + (fallbackAffected ? 1 : 0);

  const listItemsHTML = [
    fallbackAffected
      ? `<div style="padding:10px 14px;border-bottom:1px solid var(--border);display:flex;justify-content:space-between;align-items:center;gap:10px;">
           <div>
             <span class="tag tag-warn">默认兜底出口</span>
             <span class="mono" style="margin-left:6px;font-weight:500;">fallback</span>
           </div>
           <span class="mono text-sec" style="font-size:14px;flex-shrink:0;">${escapeHTML(g.name)} → <strong>${escapeHTML(defaultTarget)}</strong></span>
         </div>`
      : "",
    ...matchedRules.map(
      (r) => `
        <div style="padding:10px 14px;border-bottom:1px solid var(--border);display:flex;flex-direction:column;gap:4px;">
          <div style="display:flex;justify-content:space-between;align-items:center;gap:10px;">
            <div style="font-weight:500;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;">
              <span class="mono text-muted">#${r.index}</span>
              <span style="margin-left:6px;">${escapeHTML(r.name || "未备注分流规则")}</span>
              ${r.enabled ? "" : `<span class="tag" style="margin-left:6px;">已停用</span>`}
            </div>
            <span class="mono text-sec" style="font-size:14px;flex-shrink:0;">${escapeHTML(g.name)} → <strong>${escapeHTML(defaultTarget)}</strong></span>
          </div>
          <div class="mono text-muted" style="font-size:13px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;" title="${escapeHTML(r.expr)}">${escapeHTML(r.expr)}</div>
        </div>`
    ),
  ]
    .filter(Boolean)
    .join("");

  openModal({
    title: `删除策略组「${g.name}」`,
    bodyHTML:
      totalCount === 0
        ? `<div style="font-size:15px;line-height:1.65;color:var(--text-sec);">
             该策略组当前没有关联任何分流规则，确认删除策略组 <code class="mono">${escapeHTML(g.name)}</code> 吗？
           </div>`
        : `<div style="background:var(--warn-bg);color:var(--text);border:1px solid var(--border);border-radius:8px;padding:11px 14px;font-size:15px;line-height:1.65;">
             检测到 <strong>${totalCount}</strong> 项关联规则正在使用策略组 <code class="mono">${escapeHTML(g.name)}</code>。确认删除后，这些规则将自动迁移至默认出口 <code class="mono">${escapeHTML(defaultTarget)}</code>：
           </div>
           <div style="max-height:260px;overflow-y:auto;border:1px solid var(--border);border-radius:8px;background:var(--bg-subtle);">
             ${listItemsHTML}
           </div>`,
    footerHTML: `
      <button class="btn" id="m-del-cancel">取消</button>
      <button class="btn btn-primary" id="m-del-ok">${
        totalCount > 0 ? `确认删除并转到「${escapeHTML(defaultTarget)}」` : "确认删除"
      }</button>
    `,
    onMount(box) {
      box.querySelector("#m-del-cancel")?.addEventListener("click", closeModal);
      box.querySelector("#m-del-ok")?.addEventListener("click", () => {
        const deletedName = g.name;
        if (totalCount > 0) {
          reassignGroupReferences(store.state, deletedName, defaultTarget);
        }
        store.state.groups.splice(groupIdx, 1);
        store.selectedGroupIdx = Math.max(0, Math.min(store.selectedGroupIdx, store.state.groups.length - 1));
        if (document.getElementById("apply-status")?.dataset.phase === "error") {
          setApplyStatus("");
        }
        markDirty();
        closeModal();
        renderGroups(root);
        if (totalCount > 0) {
          toast(`已删除「${deletedName}」，${totalCount} 项关联规则已转到默认出口「${defaultTarget}」，请点击保存并应用生效`, "success");
        } else {
          toast(`已删除策略组「${deletedName}」，请点击保存并应用生效`, "success");
        }
      });
    },
  });
}

// ============================================================================
// 4. Routing Rules (Clean Ordered Table + Bidirectional DSL Editor)
// ============================================================================

function renderOutboundOptions(outbounds, selectedValue) {
  const cur = (selectedValue || "").trim();
  const missingOpt =
    cur && !outbounds.includes(cur)
      ? `<option value="${escapeHTML(cur)}" selected>${escapeHTML(cur)} (策略组不存在)</option>`
      : "";
  return (
    missingOpt +
    outbounds
      .map((ob) => `<option value="${escapeHTML(ob)}" ${cur === ob ? "selected" : ""}>${escapeHTML(ob)}</option>`)
      .join("")
  );
}

function renderRules(root) {
  const rt = store.state.routing;
  const outbounds = ["direct", "must_direct", "block", ...store.state.groups.map((g) => g.name)];

  root.innerHTML = `
    <div class="section-top">
      <div>
        <h1 class="section-title">分流规则</h1>
        <p class="section-sub">自上而下顺序匹配流量，未命中任何规则的流量将走默认兜底出口</p>
      </div>
      <div class="toolbar">
        <div class="seg-tabs">
          <button class="seg-tab ${rt.mode !== "dsl" ? "active" : ""}" id="rt-vis">表格视图</button>
          <button class="seg-tab ${rt.mode === "dsl" ? "active" : ""}" id="rt-dsl">DSL 源码</button>
        </div>
        <button class="btn btn-sm" id="rt-btn-geodata" title="更新 geoip.dat / geosite.dat 分流数据库">Geo 数据库</button>
        ${rt.mode !== "dsl" ? `<button class="btn btn-sm btn-primary" id="rt-add">新增规则</button>` : ""}
      </div>
    </div>

    <div class="panel">
      <div class="panel-head">
        <div class="toolbar">
          <span class="text-sec" style="font-size: 15px;">快速添加常用规则组:</span>
          <select id="rt-preset-sel" class="select select-sm" style="width:270px;">
            ${ROUTING_PRESETS.map((p) => `<option value="${p.id}">${escapeHTML(p.title)}</option>`).join("")}
          </select>
          <button class="btn btn-sm" id="rt-preset-btn">插入规则</button>
        </div>
        <div class="toolbar">
          <span class="text-sec" style="font-size: 15px;">默认兜底出口 (fallback):</span>
          <select id="rt-fb" class="select select-sm" style="width:155px;">
            ${renderOutboundOptions(outbounds, rt.fallback)}
          </select>
        </div>
      </div>

      ${
        rt.mode === "dsl"
          ? `
          <div class="panel-pad" style="display:flex;flex-direction:column;gap:10px;">
            <div style="display:flex;justify-content:space-between;align-items:center;">
              <span class="field-hint">直接编辑 routing { ... } 内部规则语句，切换回表格视图将自动解析同步</span>
              <button class="btn btn-sm" id="rt-parse-back">解析回表格视图</button>
            </div>
            <textarea id="rt-dsl-area" class="code-area mono">${escapeHTML(
              rt.rawDsl || renderRoutingDSLFromRules(rt.rules, rt.fallback)
            )}</textarea>
          </div>`
          : `
          <div class="table-wrap">
            <table class="data-table">
              <thead>
                <tr>
                  <th style="width:52px;">优先级</th>
                  <th style="width:58px;">启用</th>
                  <th>匹配表达式</th>
                  <th style="width:160px;">出站策略</th>
                  <th>备注</th>
                  <th style="text-align:right;width:170px;">操作</th>
                </tr>
              </thead>
              <tbody>
                ${rt.rules
                  .map(
                    (r, idx) => `
                    <tr class="${r.enabled !== false ? "" : "row-disabled"}">
                      <td class="mono text-muted">${idx + 1}</td>
                      <td>
                        <label class="toggle">
                          <input type="checkbox" data-r-tog="${idx}" ${r.enabled !== false ? "checked" : ""}/>
                          <span class="toggle-slider"></span>
                        </label>
                      </td>
                      <td class="mono" style="font-size: 15px;">${escapeHTML(renderRuleExprText(r))}</td>
                      <td>
                        <select class="select select-sm mono" data-r-ob="${idx}">
                          ${renderOutboundOptions(outbounds, r.outbound)}
                        </select>
                      </td>
                      <td class="text-sec" style="font-size: 15px;">${escapeHTML(r.name || "—")}</td>
                      <td style="text-align:right;white-space:nowrap;">
                        <button class="btn btn-sm btn-subtle" data-r-up="${idx}" ${idx === 0 ? "disabled" : ""}>↑</button>
                        <button class="btn btn-sm btn-subtle" data-r-dn="${idx}" ${idx === rt.rules.length - 1 ? "disabled" : ""}>↓</button>
                        <button class="btn btn-sm btn-subtle" data-r-ed="${idx}">编辑</button>
                        <button class="btn btn-sm btn-danger-text" data-r-rm="${idx}">删除</button>
                      </td>
                    </tr>
                  `
                  )
                  .join("")}
              </tbody>
            </table>
          </div>`
      }
    </div>
  `;

  root.querySelector("#rt-preset-btn")?.addEventListener("click", () => {
    const id = root.querySelector("#rt-preset-sel").value;
    const preset = ROUTING_PRESETS.find((p) => p.id === id);
    if (!preset) return;
    if (preset.ensureGroup && !store.state.groups.some((g) => g.name === preset.ensureGroup.name)) {
      store.state.groups.push(JSON.parse(JSON.stringify(preset.ensureGroup)));
    }
    let pos = Math.min(4, rt.rules.length);
    for (const r of preset.rules) {
      rt.rules.splice(pos++, 0, { id: `rt-${Date.now()}-${pos}`, ...JSON.parse(JSON.stringify(r)) });
    }
    if (rt.mode === "dsl") rt.rawDsl = renderRoutingDSLFromRules(rt.rules, rt.fallback);
    markDirty();
    toast(`已插入「${preset.title}」`, "success");
    renderRules(root);
  });

  root.querySelector("#rt-fb")?.addEventListener("change", (e) => { rt.fallback = e.target.value; markDirty(); });

  root.querySelector("#rt-vis")?.addEventListener("click", async () => {
    if (rt.mode === "dsl" && rt.rawDsl) {
      try {
        const res = await apiFetch("/api/v1/routing/parse-dsl", { method: "POST", body: JSON.stringify({ dsl: rt.rawDsl }) });
        if (res.ok) { rt.rules = res.rules || []; rt.fallback = res.fallback || rt.fallback; }
      } catch (err) {
        toast("解析分流 DSL 失败：" + err.message, "error");
        return;
      }
    }
    rt.mode = "visual";
    renderRules(root);
  });
  root.querySelector("#rt-dsl")?.addEventListener("click", () => {
    rt.rawDsl = renderRoutingDSLFromRules(rt.rules, rt.fallback);
    rt.mode = "dsl";
    renderRules(root);
  });
  root.querySelector("#rt-dsl-area")?.addEventListener("input", (e) => { rt.rawDsl = e.target.value; markDirty(); });
  root.querySelector("#rt-parse-back")?.addEventListener("click", async () => {
    try {
      const dsl = root.querySelector("#rt-dsl-area").value;
      const res = await apiFetch("/api/v1/routing/parse-dsl", { method: "POST", body: JSON.stringify({ dsl }) });
      if (res.ok) {
        rt.rules = res.rules || [];
        rt.fallback = res.fallback || rt.fallback;
        rt.mode = "visual";
        markDirty();
        renderRules(root);
      } else {
        toast(res.error || "解析分流 DSL 失败", "error");
      }
    } catch (err) {
      toast("解析分流 DSL 失败：" + err.message, "error");
    }
  });

  root.querySelector("#rt-add")?.addEventListener("click", () => openRuleModal(null, -1));
  root.querySelector("#rt-btn-geodata")?.addEventListener("click", () => {
    store.systemSubTab = "logs";
    switchTab("system");
    setTimeout(() => document.getElementById("geodata-panel")?.scrollIntoView({ behavior: "smooth", block: "center" }), 60);
  });
  root.querySelectorAll("[data-r-tog]").forEach((c) => {
    c.addEventListener("change", () => { rt.rules[Number(c.dataset.rTog)].enabled = c.checked; markDirty(); renderRules(root); });
  });
  root.querySelectorAll("[data-r-ob]").forEach((s) => {
    s.addEventListener("change", () => { rt.rules[Number(s.dataset.rOb)].outbound = s.value; markDirty(); });
  });
  root.querySelectorAll("[data-r-up]").forEach((b) => {
    b.addEventListener("click", () => {
      const i = Number(b.dataset.rUp);
      if (i > 0) { [rt.rules[i - 1], rt.rules[i]] = [rt.rules[i], rt.rules[i - 1]]; markDirty(); renderRules(root); }
    });
  });
  root.querySelectorAll("[data-r-dn]").forEach((b) => {
    b.addEventListener("click", () => {
      const i = Number(b.dataset.rDn);
      if (i < rt.rules.length - 1) { [rt.rules[i + 1], rt.rules[i]] = [rt.rules[i], rt.rules[i + 1]]; markDirty(); renderRules(root); }
    });
  });
  root.querySelectorAll("[data-r-ed]").forEach((b) => {
    const i = Number(b.dataset.rEd);
    b.addEventListener("click", () => openRuleModal(rt.rules[i], i));
  });
  root.querySelectorAll("[data-r-rm]").forEach((b) => {
    b.addEventListener("click", () => { rt.rules.splice(Number(b.dataset.rRm), 1); markDirty(); renderRules(root); });
  });
}

function openRuleModal(existing, idx) {
  const outbounds = ["direct", "must_direct", "block", ...store.state.groups.map((g) => g.name)];
  const rule = existing
    ? JSON.parse(JSON.stringify(existing))
    : {
        id: `rt-${Date.now()}`,
        enabled: true,
        name: "",
        category: "custom",
        conditions: [{ negate: false, field: "domain", value: "suffix:example.com" }],
        outbound: store.state.groups[0]?.name || "direct",
      };
  if (!rule.conditions?.length) {
    rule.conditions = [{ negate: false, field: "domain", value: rule.rawExpr || "" }];
  }

  const renderConds = () =>
    rule.conditions
      .map(
        (c, i) => `
        <div style="display:flex;gap:8px;margin-bottom:8px;">
          <select class="select" style="width:90px;" data-c-neg="${i}">
            <option value="false" ${!c.negate ? "selected" : ""}>匹配</option>
            <option value="true" ${c.negate ? "selected" : ""}>非 (!)</option>
          </select>
          <select class="select" style="width:170px;" data-c-f="${i}">
            ${ROUTING_FIELDS.map((f) => `<option value="${f.value}" ${c.field === f.value ? "selected" : ""}>${f.label}</option>`).join("")}
          </select>
          <input class="input mono" style="flex:1;" data-c-v="${i}" value="${escapeHTML(c.value)}" placeholder="geosite:cn 或 1.1.1.1/32" />
          ${rule.conditions.length > 1 ? `<button type="button" class="btn btn-danger-text" data-c-rm="${i}">✕</button>` : ""}
        </div>`
      )
      .join("");

  openModal({
    title: idx >= 0 ? "编辑分流规则" : "新增分流规则",
    bodyHTML: `
      <div class="form-grid">
        <div class="field">
          <label class="field-label">出站目标</label>
          <select id="m-r-ob" class="select mono">
            ${renderOutboundOptions(outbounds, rule.outbound)}
          </select>
        </div>
        <div class="field">
          <label class="field-label">备注说明 (可选)</label>
          <input id="m-r-name" class="input" value="${escapeHTML(rule.name || "")}" placeholder="如：国内直连" />
        </div>
        <div class="field full">
          <label class="field-label">
            <span>匹配条件 (多条件同时满足)</span>
            <button type="button" class="btn btn-sm" id="m-r-addc">+ 添加条件</button>
          </label>
          <div id="m-r-conds">${renderConds()}</div>
        </div>
      </div>
    `,
    footerHTML: `<button class="btn" id="m-c">取消</button><button class="btn btn-primary" id="m-s">确定</button>`,
    onMount(box) {
      const bind = () => {
        box.querySelectorAll("[data-c-neg]").forEach((el) => el.addEventListener("change", () => (rule.conditions[Number(el.dataset.cNeg)].negate = el.value === "true")));
        box.querySelectorAll("[data-c-f]").forEach((el) => el.addEventListener("change", () => (rule.conditions[Number(el.dataset.cF)].field = el.value)));
        box.querySelectorAll("[data-c-v]").forEach((el) => el.addEventListener("input", () => (rule.conditions[Number(el.dataset.cV)].value = el.value)));
        box.querySelectorAll("[data-c-rm]").forEach((b) =>
          b.addEventListener("click", () => {
            rule.conditions.splice(Number(b.dataset.cRm), 1);
            box.querySelector("#m-r-conds").innerHTML = renderConds();
            bind();
          })
        );
      };
      bind();
      box.querySelector("#m-r-addc")?.addEventListener("click", () => {
        rule.conditions.push({ negate: false, field: "dport", value: "443" });
        box.querySelector("#m-r-conds").innerHTML = renderConds();
        bind();
      });
      box.querySelector("#m-c")?.addEventListener("click", closeModal);
      box.querySelector("#m-s")?.addEventListener("click", () => {
        rule.outbound = box.querySelector("#m-r-ob").value;
        rule.name = box.querySelector("#m-r-name").value.trim();
        rule.rawExpr = "";
        if (idx >= 0) store.state.routing.rules[idx] = rule;
        else store.state.routing.rules.unshift(rule);
        markDirty();
        closeModal();
        renderPage();
      });
    },
  });
}

// ============================================================================
// 5. System, Network/DNS, Raw config.dae Studio & Live Logs
// ============================================================================

function renderSystem(root) {
  const sub = store.systemSubTab;

  root.innerHTML = `
    <div class="section-top">
      <div>
        <h1 class="section-title">系统与日志</h1>
        <p class="section-sub">管理软路由网卡接口、DNS 防污染上游、原生 config.dae 源码与内核运行日志</p>
      </div>
      <div class="seg-tabs">
        <button class="seg-tab ${sub === "netdns" ? "active" : ""}" data-sys-tab="netdns">网络接口与 DNS</button>
        <button class="seg-tab ${sub === "configfile" ? "active" : ""}" data-sys-tab="configfile">配置文件 (config.dae)</button>
        <button class="seg-tab ${sub === "logs" ? "active" : ""}" data-sys-tab="logs">运行日志与环境体检</button>
      </div>
    </div>
    <div id="sys-body" style="display:flex;flex-direction:column;gap:16px;"></div>
  `;

  root.querySelectorAll("[data-sys-tab]").forEach((b) => {
    b.addEventListener("click", () => {
      store.systemSubTab = b.dataset.sysTab;
      renderSystem(root);
    });
  });

  const body = root.querySelector("#sys-body");
  if (sub === "netdns") renderSysNetDNS(body);
  else if (sub === "configfile") renderSysConfigFile(body);
  else renderSysLogs(body);
}

function renderSysNetDNS(container) {
  const g = store.state.global;
  const dns = store.state.dns;
  const ifaces = (store.doctor?.interfaces || [])
    .map((i) => i.name)
    .filter((n) => n && !n.startsWith("ip6") && !n.startsWith("gre") && !n.startsWith("erspan") && !n.startsWith("sit") && n !== "teql0" && n !== "tunl0" && n !== "dummy0");

  container.innerHTML = `
    <div class="split-2 split-2-top">
      <!-- Left: Network & Global -->
      <div class="panel panel-pad" style="display:flex;flex-direction:column;gap:14px;">
        <div class="panel-title">接口绑定与核心参数</div>

        <div class="field">
          <label class="field-label">LAN 局域网代理接口 (点击选择软路由接口)</label>
          <div class="chips">
            ${(ifaces.length ? ifaces : ["br-lan", "eth0"])
              .map((name) => {
                const on = (g.lanInterfaces || []).includes(name);
                return `<button type="button" class="chip-btn mono ${on ? "active" : ""}" data-lan-pick="${escapeHTML(name)}">${escapeHTML(name)}</button>`;
              })
              .join("")}
          </div>
          <span class="field-hint">软路由作主路由/旁路由时勾选局域网桥接接口（通常为 br-lan）</span>
        </div>

        <div class="form-grid">
          <div class="field">
            <label class="field-label">WAN 出口接口</label>
            <input id="s-wan" class="input mono" value="${escapeHTML(g.wanInterface)}" placeholder="auto" />
            <span class="field-hint">默认填 auto 自动绑定拨号出口 (如 pppoe-wan)</span>
          </div>
          <div class="field">
            <label class="field-label">域名嗅探模式 (dial_mode)</label>
            <select id="s-dial" class="select">
              <option value="domain+" ${g.dialMode === "domain+" ? "selected" : ""}>domain+ (嗅探域名直连代理解析 · 推荐)</option>
              <option value="domain++" ${g.dialMode === "domain++" ? "selected" : ""}>domain++ (强制嗅探域名重路由)</option>
              <option value="domain" ${g.dialMode === "domain" ? "selected" : ""}>domain (标准嗅探)</option>
              <option value="ip" ${g.dialMode === "ip" ? "selected" : ""}>ip (纯目标 IP 拨号)</option>
            </select>
          </div>
          <div class="field">
            <label class="field-label">TLS 指纹伪装库</label>
            <select id="s-tls" class="select">
              <option value="tls" ${g.tlsImplementation === "tls" ? "selected" : ""}>tls (标准库)</option>
              <option value="utls" ${g.tlsImplementation === "utls" ? "selected" : ""}>utls (模拟 Chrome 指纹)</option>
            </select>
          </div>
          <div class="field">
            <label class="field-label">日志等级</label>
            <select id="s-loglvl" class="select">
              ${["error", "warn", "info", "debug"].map((l) => `<option value="${l}" ${g.logLevel === l ? "selected" : ""}>${l}</option>`).join("")}
            </select>
          </div>
          <div class="field">
            <label class="field-label">节点健康检测周期</label>
            <input id="s-chkint" class="input mono" value="${escapeHTML(g.checkInterval)}" />
          </div>
          <div class="field">
            <label class="field-label">TProxy 端口</label>
            <input id="s-tport" type="number" class="input mono" value="${g.tproxyPort || 12345}" />
          </div>
        </div>

        <div style="display:flex;gap:20px;flex-wrap:wrap;padding-top:4px;">
          <label class="toggle">
            <input type="checkbox" id="s-autokern" ${g.autoConfigKernelParameter ? "checked" : ""}/>
            <span class="toggle-slider"></span>
            <span>自动开启内核转发参数</span>
          </label>
          <label class="toggle">
            <input type="checkbox" id="s-nowait" ${g.disableWaitingNetwork ? "checked" : ""}/>
            <span class="toggle-slider"></span>
            <span>启动不等待网络就绪</span>
          </label>
        </div>

        <div style="border-top:1px solid var(--border);padding-top:14px;margin-top:4px;">
          <div class="panel-title" style="margin-bottom:10px;">面板访问密码保护</div>
          <div class="form-grid">
            <div class="field">
              <label class="field-label">管理员账号</label>
              <input id="s-auth-u" class="input" value="${escapeHTML(store.state.auth?.username || "admin")}" />
            </div>
            <div class="field">
              <label class="field-label">登录密码</label>
              <input id="s-auth-p" type="password" class="input" placeholder="设置新密码" />
            </div>
          </div>
          <div style="display:flex;justify-content:space-between;align-items:center;margin-top:10px;">
            <label class="toggle">
              <input type="checkbox" id="s-auth-on" ${store.state.auth?.enabled ? "checked" : ""}/>
              <span class="toggle-slider"></span>
              <span>启用面板登录鉴权</span>
            </label>
            <button class="btn btn-sm" id="s-auth-save">保存鉴权设置</button>
          </div>
        </div>
      </div>

      <!-- Right: Clean DNS Config -->
      <div class="panel panel-pad" style="display:flex;flex-direction:column;gap:14px;">
        <div style="display:flex;justify-content:space-between;align-items:center;">
          <span class="panel-title">DNS 上游与防污染分流</span>
          <div class="seg-tabs">
            <button class="seg-tab ${dns.mode !== "dsl" ? "active" : ""}" id="dns-m-vis">表单模式</button>
            <button class="seg-tab ${dns.mode === "dsl" ? "active" : ""}" id="dns-m-dsl">DSL 源码</button>
          </div>
        </div>

        ${
          dns.mode === "dsl"
            ? `<div style="display:flex;justify-content:space-between;align-items:center;">
                 <span class="field-hint">直接编辑 dns { ... } 内部语句，切换回表单模式将自动解析同步</span>
                 <button type="button" class="btn btn-sm" id="dns-parse-back">解析回表单模式</button>
               </div>
               <textarea id="dns-raw-txt" class="code-area mono" style="min-height:300px;">${escapeHTML(
                 dns.rawDsl || renderDNSDSLFromVisual(dns)
               )}</textarea>`
            : `
            <div class="form-grid">
              <div class="field">
                <label class="field-label">本地监听地址 (bind, 可选)</label>
                <input id="dns-bind" class="input mono" value="${escapeHTML(dns.bind || "")}" placeholder="127.0.0.1:15353" />
              </div>
              <div class="field">
                <label class="field-label">默认兜底上游 (海外防污染)</label>
                <select id="dns-fb" class="select mono">
                  ${
                    dns.requestFallback && !(dns.upstreams || []).some((u) => u.tag === dns.requestFallback)
                      ? `<option value="${escapeHTML(dns.requestFallback)}" selected>${escapeHTML(dns.requestFallback)} (上游不存在)</option>`
                      : ""
                  }
                  ${(dns.upstreams || []).map((u) => `<option value="${escapeHTML(u.tag)}" ${dns.requestFallback === u.tag ? "selected" : ""}>${escapeHTML(u.tag)} (${escapeHTML(u.url)})</option>`).join("")}
                </select>
              </div>
            </div>

            <div class="field">
              <label class="field-label">
                <span>上游 DNS 服务器</span>
                <button type="button" class="btn btn-sm" id="dns-add-up">+ 添加上游</button>
              </label>
              <div class="table-wrap" style="border:1px solid var(--border);border-radius:7px;">
                <table class="data-table">
                  <thead><tr><th>标识</th><th>地址 (推荐国内 https:// 或 udp://, 海外 https://)</th><th></th></tr></thead>
                  <tbody>
                    ${(dns.upstreams || [])
                      .map(
                        (u, idx) => `
                        <tr>
                          <td class="mono">${escapeHTML(u.tag)}</td>
                          <td><input class="input input-sm mono" data-up-url="${idx}" value="${escapeHTML(u.url)}" /></td>
                          <td style="text-align:right;"><button class="btn btn-sm btn-danger-text" data-up-rm="${idx}" ${(dns.upstreams || []).length <= 1 ? "disabled" : ""}>删除</button></td>
                        </tr>`
                      )
                      .join("")}
                  </tbody>
                </table>
              </div>
            </div>

            <div class="field">
              <label class="field-label">DNS 请求分流规则</label>
              <div class="table-wrap" style="border:1px solid var(--border);border-radius:7px;">
                <table class="data-table">
                  <thead><tr><th style="width:52px;">启用</th><th>匹配条件</th><th>目标上游</th></tr></thead>
                  <tbody>
                    ${(dns.requestRules || [])
                      .map(
                        (r, idx) => `
                        <tr class="${r.enabled !== false ? "" : "row-disabled"}">
                          <td>
                            <label class="toggle">
                              <input type="checkbox" data-dreq-tog="${idx}" ${r.enabled !== false ? "checked" : ""}/>
                              <span class="toggle-slider"></span>
                            </label>
                          </td>
                          <td class="mono" style="font-size: 15px;">${escapeHTML(r.expr)}</td>
                          <td class="mono">${escapeHTML(r.target)}</td>
                        </tr>`
                      )
                      .join("")}
                  </tbody>
                </table>
              </div>
            </div>

            ${
              (dns.responseRules || []).length > 0
                ? `<div class="field">
                     <label class="field-label">DNS 响应过滤规则 (response)</label>
                     <div class="table-wrap" style="border:1px solid var(--border);border-radius:7px;">
                       <table class="data-table">
                         <thead><tr><th style="width:52px;">启用</th><th>匹配条件</th><th>处理动作</th></tr></thead>
                         <tbody>
                           ${(dns.responseRules || [])
                             .map(
                               (r, idx) => `
                               <tr class="${r.enabled !== false ? "" : "row-disabled"}">
                                 <td>
                                   <label class="toggle">
                                     <input type="checkbox" data-dresp-tog="${idx}" ${r.enabled !== false ? "checked" : ""}/>
                                     <span class="toggle-slider"></span>
                                   </label>
                                 </td>
                                 <td class="mono" style="font-size: 15px;">${escapeHTML(r.expr)}</td>
                                 <td class="mono">${escapeHTML(r.target)}</td>
                               </tr>`
                             )
                             .join("")}
                         </tbody>
                       </table>
                     </div>
                   </div>`
                : ""
            }`
        }
      </div>
    </div>
  `;

  container.querySelectorAll("[data-lan-pick]").forEach((b) => {
    b.addEventListener("click", () => {
      g.lanInterfaces = toggleItem(g.lanInterfaces || [], b.dataset.lanPick);
      markDirty();
      renderSysNetDNS(container);
    });
  });

  const bindG = (sel, fn) => container.querySelector(sel)?.addEventListener("change", (e) => { fn(e.target); markDirty(); });
  bindG("#s-wan", (el) => (g.wanInterface = el.value.trim()));
  bindG("#s-dial", (el) => (g.dialMode = el.value));
  bindG("#s-tls", (el) => (g.tlsImplementation = el.value));
  bindG("#s-loglvl", (el) => (g.logLevel = el.value));
  bindG("#s-chkint", (el) => (g.checkInterval = el.value.trim()));
  bindG("#s-tport", (el) => (g.tproxyPort = Number(el.value) || 12345));
  bindG("#s-autokern", (el) => (g.autoConfigKernelParameter = el.checked));
  bindG("#s-nowait", (el) => (g.disableWaitingNetwork = el.checked));

  container.querySelector("#s-auth-save")?.addEventListener("click", async () => {
    const authBtn = container.querySelector("#s-auth-save");
    const enabled = container.querySelector("#s-auth-on").checked;
    const username = container.querySelector("#s-auth-u").value.trim() || "admin";
    const passInput = container.querySelector("#s-auth-p");
    const password = passInput.value;
    if (enabled && !password && !store.state.auth?.enabled) {
      toast("启用密码保护请先填写密码", "error");
      return;
    }
    authBtn.disabled = true;
    try {
      const res = await apiFetch("/api/v1/auth/update", { method: "POST", body: JSON.stringify({ enabled, username, password }) });
      if (res.ok) {
        store.token = res.token || store.token;
        localStorage.setItem("daed_next_token", store.token);
        store.state.auth = res.auth;
        passInput.value = "";
        toast("密码保护设置已更新", "success");
      } else {
        toast(res.error || "保存鉴权设置失败", "error");
      }
    } catch (err) {
      toast("保存鉴权设置失败：" + err.message, "error");
    } finally {
      authBtn.disabled = false;
    }
  });

  const syncDNSFromDSL = async (dslText) => {
    const raw = String(dslText ?? dns.rawDsl ?? "").trim();
    if (!raw) {
      dns.mode = "visual";
      renderSysNetDNS(container);
      return;
    }
    try {
      const res = await apiFetch("/api/v1/dns/parse-dsl", {
        method: "POST",
        body: JSON.stringify({ dsl: raw }),
      });
      if (res.ok && res.dns) {
        if ((res.dns.upstreams || []).length === 0) {
          toast("当前 DNS DSL 未检测到标准 upstream 块，已保留上次表单字段", "info");
        } else {
          dns.bind = res.dns.bind || "";
          dns.ipVersionPrefer = res.dns.ipVersionPrefer || 0;
          dns.upstreams = res.dns.upstreams;
          dns.requestRules = res.dns.requestRules || [];
          dns.requestFallback = res.dns.requestFallback || res.dns.upstreams[0].tag;
          dns.responseRules = res.dns.responseRules || [];
          dns.responseFallback = res.dns.responseFallback || "accept";
        }
      }
      dns.mode = "visual";
      markDirty();
      renderSysNetDNS(container);
    } catch (err) {
      toast("解析 DNS DSL 失败：" + err.message, "error");
    }
  };

  container.querySelector("#dns-m-vis")?.addEventListener("click", () => {
    if (dns.mode === "dsl") {
      syncDNSFromDSL(container.querySelector("#dns-raw-txt")?.value ?? dns.rawDsl);
    }
  });
  container.querySelector("#dns-parse-back")?.addEventListener("click", () => {
    syncDNSFromDSL(container.querySelector("#dns-raw-txt")?.value ?? dns.rawDsl);
  });
  container.querySelector("#dns-m-dsl")?.addEventListener("click", () => {
    if (dns.mode !== "dsl") {
      dns.rawDsl = renderDNSDSLFromVisual(dns);
      dns.mode = "dsl";
      markDirty();
      renderSysNetDNS(container);
    }
  });
  container.querySelector("#dns-raw-txt")?.addEventListener("input", (e) => { dns.rawDsl = e.target.value; markDirty(); });
  bindG("#dns-bind", (el) => (dns.bind = el.value.trim()));
  bindG("#dns-fb", (el) => (dns.requestFallback = el.value));

  container.querySelectorAll("[data-up-url]").forEach((inp) => {
    inp.addEventListener("change", () => { dns.upstreams[Number(inp.dataset.upUrl)].url = inp.value.trim(); markDirty(); });
  });
  container.querySelectorAll("[data-up-rm]").forEach((b) => {
    b.addEventListener("click", () => openDeleteDnsUpstreamModal(Number(b.dataset.upRm), container));
  });
  container.querySelectorAll("[data-dreq-tog]").forEach((c) => {
    c.addEventListener("change", () => { dns.requestRules[Number(c.dataset.dreqTog)].enabled = c.checked; markDirty(); renderSysNetDNS(container); });
  });
  container.querySelectorAll("[data-dresp-tog]").forEach((c) => {
    c.addEventListener("change", () => { dns.responseRules[Number(c.dataset.drespTog)].enabled = c.checked; markDirty(); renderSysNetDNS(container); });
  });
  container.querySelector("#dns-add-up")?.addEventListener("click", () => {
    dns.upstreams.push({ tag: `dns_${dns.upstreams.length + 1}`, url: "https://1.1.1.1/dns-query", note: "" });
    markDirty();
    renderSysNetDNS(container);
  });
}

function openDeleteDnsUpstreamModal(upIdx, container) {
  const dns = store.state?.dns;
  const upstreams = dns?.upstreams || [];
  const target = upstreams[upIdx];
  if (!target) return;
  if (upstreams.length <= 1) {
    toast("至少需要保留一个上游 DNS 服务器", "error");
    return;
  }
  const tag = (target.tag || "").trim();
  const remaining = upstreams.filter((_, idx) => idx !== upIdx);
  const remainingTags = new Set(remaining.map((u) => u.tag));
  const defaultTarget =
    dns.requestFallback && dns.requestFallback !== tag && remainingTags.has(dns.requestFallback)
      ? dns.requestFallback
      : remaining[0].tag;

  const fallbackAffected = (dns.requestFallback || "").trim() === tag;
  const affectedReqRules = (dns.requestRules || []).filter((r) => (r.target || "").trim() === tag);
  const upstreamCallRe = new RegExp(`\\bupstream\\(\\s*${tag.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}\\s*\\)`);
  const affectedRespRules = (dns.responseRules || []).filter(
    (r) => (r.target || "").trim() === tag || upstreamCallRe.test(r.expr || "")
  );
  const totalRefs = (fallbackAffected ? 1 : 0) + affectedReqRules.length + affectedRespRules.length;

  openModal({
    title: `删除 DNS 上游「${tag}」`,
    bodyHTML:
      totalRefs === 0
        ? `<div style="font-size:15px;line-height:1.65;color:var(--text-sec);">
             该 DNS 上游当前未被任何请求或响应规则引用，确认删除 <code class="mono">${escapeHTML(tag)}</code> 吗？
           </div>`
        : `<div style="background:var(--warn-bg);color:var(--text);border:1px solid var(--border);border-radius:8px;padding:11px 14px;font-size:15px;line-height:1.65;">
             检测到 <strong>${totalRefs}</strong> 处 DNS 规则或兜底配置引用了上游 <code class="mono">${escapeHTML(tag)}</code>。确认删除后，这些引用将自动迁移至 <code class="mono">${escapeHTML(defaultTarget)}</code>。
           </div>`,
    footerHTML: `
      <button class="btn" id="m-dup-cancel">取消</button>
      <button class="btn btn-primary" id="m-dup-ok">${
        totalRefs > 0 ? `确认删除并迁移至「${escapeHTML(defaultTarget)}」` : "确认删除"
      }</button>
    `,
    onMount(box) {
      box.querySelector("#m-dup-cancel")?.addEventListener("click", closeModal);
      box.querySelector("#m-dup-ok")?.addEventListener("click", () => {
        if (fallbackAffected) dns.requestFallback = defaultTarget;
        for (const r of dns.requestRules || []) {
          if ((r.target || "").trim() === tag) r.target = defaultTarget;
        }
        for (const r of dns.responseRules || []) {
          if ((r.target || "").trim() === tag) r.target = defaultTarget;
          if (upstreamCallRe.test(r.expr || "")) {
            r.expr = r.expr.replace(upstreamCallRe, `upstream(${defaultTarget})`);
          }
        }
        upstreams.splice(upIdx, 1);
        dns.rawDsl = renderDNSDSLFromVisual(dns);
        markDirty();
        closeModal();
        renderSysNetDNS(container);
        toast(
          totalRefs > 0
            ? `已删除上游「${tag}」，${totalRefs} 处引用已迁移至「${defaultTarget}」`
            : `已删除上游「${tag}」`,
          "success"
        );
      });
    },
  });
}

async function renderSysConfigFile(container) {
  // Pure read-only render preview — never mutates state.json or /etc/dae/config.dae on tab view
  try {
    const syncRes = await apiFetch("/api/v1/config/render", {
      method: "POST",
      body: JSON.stringify({ state: store.state }),
    });
    if (syncRes.configDae) store.configDae = syncRes.configDae;
  } catch (_) {}

  const snaps = store.state.snapshots || [];
  const isZeroNodeDirect = Boolean(store.state.directWithoutNodes && activeNodes(store.state).length === 0);

  container.innerHTML = `
    <div class="panel panel-pad" style="display:flex;flex-direction:column;gap:12px;">
      <div style="display:flex;justify-content:space-between;align-items:center;flex-wrap:wrap;gap:10px;">
        <div>
          <div class="panel-title">原生 /etc/dae/config.dae 在线编辑</div>
          <div class="panel-desc">可直接修改下方完整配置文件并点击「解析并热重载」，所有更改将双向同步回可视化表单</div>
        </div>
        <div class="toolbar">
          <button class="btn btn-sm btn-danger-text" id="cfg-btn-reset">恢复默认设置</button>
          <button class="btn btn-sm" id="cfg-btn-snap">保存为备份快照</button>
          <button class="btn btn-sm" id="cfg-btn-copy">复制</button>
          <button class="btn btn-sm" id="cfg-btn-dl">下载 .dae</button>
          <button class="btn btn-sm btn-primary" id="cfg-btn-apply-raw">解析并热重载</button>
        </div>
      </div>
      ${
        isZeroNodeDirect
          ? `<div class="info-banner">当前尚无可用代理节点，运行中的 <code class="mono">config.dae</code> 已自动切换为安全直连回退配置；您预设的完整策略组与分流规则模板已安全保留，导入节点并点击「保存并应用」后将自动恢复完整分流。</div>`
          : ""
      }
      <textarea id="cfg-raw-editor" class="code-area mono" style="min-height:440px;">${escapeHTML(store.configDae)}</textarea>
    </div>

    ${
      snaps.length > 0
        ? `<div class="panel">
            <div class="panel-head"><span class="panel-title">配置备份快照 (${snaps.length})</span></div>
            <div class="table-wrap">
              <table class="data-table">
                <thead><tr><th>快照名称</th><th>创建时间</th><th style="text-align:right;">操作</th></tr></thead>
                <tbody>
                  ${snaps
                    .map(
                      (sn) => `
                      <tr>
                        <td style="font-weight:500;">${escapeHTML(sn.name)}</td>
                        <td class="mono text-sec">${new Date(sn.createdAt).toLocaleString("zh-CN")}</td>
                        <td style="text-align:right;">
                          <button class="btn btn-sm" data-snap-rev="${escapeHTML(sn.id)}">恢复此版本</button>
 <button class="btn btn-sm btn-danger-text" data-snap-del="${escapeHTML(sn.id)}">删除</button>
                        </td>
                      </tr>`
                    )
                    .join("")}
                </tbody>
              </table>
            </div>
          </div>`
        : ""
    }
  `;

  container.querySelector("#cfg-btn-copy")?.addEventListener("click", () => {
    navigator.clipboard?.writeText(container.querySelector("#cfg-raw-editor").value);
    toast("已复制配置文件内容", "success");
  });
  container.querySelector("#cfg-btn-dl")?.addEventListener("click", () => {
    const blob = new Blob([container.querySelector("#cfg-raw-editor").value], { type: "text/plain;charset=utf-8" });
    const a = document.createElement("a");
    a.href = URL.createObjectURL(blob);
    a.download = "config.dae";
    a.click();
  });
  container.querySelector("#cfg-btn-reset")?.addEventListener("click", () => {
    openResetDefaultsModal();
  });
  container.querySelector("#cfg-btn-snap")?.addEventListener("click", async () => {
    const snapBtn = container.querySelector("#cfg-btn-snap");
    snapBtn.disabled = true;
    try {
      const res = await apiFetch("/api/v1/snapshots", { method: "POST", body: JSON.stringify({}) });
      if (res.ok) {
        store.state.snapshots = res.snapshots;
        toast("已保存配置备份快照", "success");
        renderSysConfigFile(container);
      } else {
        toast(res.error || "保存快照失败", "error");
      }
    } catch (err) {
      toast("保存快照失败：" + err.message, "error");
    } finally {
      snapBtn.disabled = false;
    }
  });
  container.querySelector("#cfg-btn-apply-raw")?.addEventListener("click", async () => {
    if (store.applying) return;
    const configDae = container.querySelector("#cfg-raw-editor").value;
    store.applying = true;
    syncApplyControls();
    setApplyStatus("正在解析并热重载 config.dae…", "pending");
    try {
      const imp = await apiFetch("/api/v1/config/import", {
        method: "POST",
        timeoutMs: 145000,
        body: JSON.stringify({ configDae, apply: true }),
      });
      if (!imp.ok) {
        throw new Error(imp.error || "语法解析或内核校验未通过");
      }
      store.state = normalizeState(imp.state);
      store.configDae = imp.configDae;
      if (imp.stats) store.stats = imp.stats;
      if (imp.logs) store.logs = imp.logs;
      syncDirtyUI(false);
      setApplyStatus("config.dae 已解析并热重载生效", "success");
      toast("config.dae 已解析并热重载生效", "success");
      syncHeader();
      renderPage();
      checkConnectivitySilent();
    } catch (err) {
      setApplyStatus("应用失败：" + err.message, "error");
      toast("应用失败：" + err.message, "error");
    } finally {
      store.applying = false;
      syncApplyControls();
    }
  });
  container.querySelectorAll("[data-snap-del]").forEach((b) => {
    b.addEventListener("click", () => {
      const snapshot = store.state.snapshots.find((sn) => sn.id === b.dataset.snapDel);
      if (snapshot) openDeleteSnapshotModal(snapshot, container);
    });
  });
  container.querySelectorAll("[data-snap-rev]").forEach((b) => {
    b.addEventListener("click", () => {
      const snapshot = store.state.snapshots.find((sn) => sn.id === b.dataset.snapRev);
      if (snapshot) openRestoreSnapshotModal(snapshot);
    });
  });
}

function openResetDefaultsModal() {
  openModal({
    title: "恢复默认设置",
    bodyHTML: `
      <div style="background:var(--warn-bg);color:var(--text);border:1px solid var(--border);border-radius:8px;padding:11px 14px;font-size:15px;line-height:1.65;">
        恢复后将清空订阅和节点，恢复通用 DNS、国内外分流及自动选路策略。无节点时直连，导入节点并保存应用后启用代理。登录账号、网卡绑定和 DNS 监听地址会保留。系统会自动生成恢复备份快照，正在运行的代理将重新加载。
      </div>
      <div class="field full">
        <label class="field-label">请输入 <code class="mono">RESET</code> 确认执行</label>
        <input id="m-reset-confirm" class="input mono" placeholder="RESET" autocomplete="off" />
      </div>
    `,
    footerHTML: `
      <button class="btn" id="m-reset-cancel">取消</button>
      <button class="btn btn-primary" id="m-reset-ok" disabled>确认恢复默认设置</button>
    `,
    onMount(box) {
      const input = box.querySelector("#m-reset-confirm");
      const okBtn = box.querySelector("#m-reset-ok");
      input?.addEventListener("input", () => {
        okBtn.disabled = input.value.trim() !== "RESET";
      });
      box.querySelector("#m-reset-cancel")?.addEventListener("click", closeModal);
      okBtn?.addEventListener("click", async () => {
        if (input.value.trim() !== "RESET" || store.applying) return;
        okBtn.disabled = true;
        okBtn.textContent = "正在恢复…";
        store.applying = true;
        syncApplyControls();
        try {
          const result = await apiFetch("/api/v1/settings/reset", {
            method: "POST",
            timeoutMs: 145000,
            body: JSON.stringify({ confirm: "RESET" }),
          });
          if (!result.ok) throw new Error(result.error || "恢复失败");
          store.state = normalizeState(result.state);
          store.configDae = result.configDae || "";
          store.stats = result.stats || store.stats;
          store.selectedGroupIdx = 0;
          syncDirtyUI(false);
          setApplyStatus("已恢复通用默认配置", "success");
          closeModal();
          syncHeader();
          renderPage();
          toast("已恢复通用默认配置，原配置可在备份快照中恢复", "success");
        } catch (err) {
          toast("恢复默认设置失败：" + err.message, "error");
          okBtn.disabled = false;
          okBtn.textContent = "确认恢复默认设置";
        } finally {
          store.applying = false;
          syncApplyControls();
        }
      });
      input?.focus();
    },
  });
}

function openDeleteSnapshotModal(snapshot, container) {
  openModal({
    title: "删除备份快照",
    bodyHTML: `
      <div style="font-size:15px;line-height:1.65;color:var(--text-sec);">
        确定删除备份快照「<strong>${escapeHTML(snapshot.name)}</strong>」吗？删除后无法再从该快照恢复，当前运行配置不受影响。
      </div>
    `,
    footerHTML: `
      <button class="btn" id="m-sdel-cancel">取消</button>
      <button class="btn btn-primary" id="m-sdel-ok">确认删除</button>
    `,
    onMount(box) {
      box.querySelector("#m-sdel-cancel")?.addEventListener("click", closeModal);
      const okBtn = box.querySelector("#m-sdel-ok");
      okBtn?.addEventListener("click", async () => {
        okBtn.disabled = true;
        try {
          const res = await apiFetch("/api/v1/snapshots/delete", {
            method: "POST",
            body: JSON.stringify({ id: snapshot.id }),
          });
          if (!res.ok) throw new Error(res.error || "删除快照失败");
          store.state.snapshots = res.snapshots || [];
          closeModal();
          toast("备份快照已删除", "success");
          renderSysConfigFile(container);
        } catch (err) {
          toast(err.message, "error");
          okBtn.disabled = false;
        }
      });
    },
  });
}

function openRestoreSnapshotModal(snapshot) {
  const willReload = Boolean(store.stats?.running);
  openModal({
    title: `恢复备份快照「${snapshot.name}」`,
    bodyHTML: `
      <div style="font-size:15px;line-height:1.65;color:var(--text-sec);">
        恢复后将使用快照「<strong>${escapeHTML(snapshot.name)}</strong>」覆盖当前配置与 <code class="mono">/etc/dae/config.dae</code>${
          willReload ? "，并立即通知 dae 热重载生效" : ""
        }。${store.dirty ? "<br><span style='color:var(--warn);'>注意：当前页面未保存的修改将被覆盖。</span>" : ""}
      </div>
    `,
    footerHTML: `
      <button class="btn" id="m-srev-cancel">取消</button>
      <button class="btn btn-primary" id="m-srev-ok">${willReload ? "确认恢复并热重载" : "确认恢复快照"}</button>
    `,
    onMount(box) {
      box.querySelector("#m-srev-cancel")?.addEventListener("click", closeModal);
      const okBtn = box.querySelector("#m-srev-ok");
      okBtn?.addEventListener("click", async () => {
        if (store.applying) return;
        okBtn.disabled = true;
        okBtn.textContent = "正在恢复…";
        store.applying = true;
        syncApplyControls();
        setApplyStatus(`正在恢复快照「${snapshot.name}」…`, "pending");
        try {
          const res = await apiFetch("/api/v1/snapshots/restore", {
            method: "POST",
            timeoutMs: 145000,
            body: JSON.stringify({ id: snapshot.id, apply: willReload }),
          });
          if (!res.ok) throw new Error(res.error || "快照恢复失败");
          store.state = normalizeState(res.state);
          store.configDae = res.configDae || "";
          if (res.stats) store.stats = res.stats;
          if (res.logs) store.logs = res.logs;
          syncDirtyUI(false);
          const doneMsg = willReload ? `已恢复快照「${snapshot.name}」并热重载生效` : `已恢复快照「${snapshot.name}」`;
          setApplyStatus(doneMsg, "success");
          closeModal();
          syncHeader();
          renderPage();
          toast(doneMsg, "success");
          if (willReload) checkConnectivitySilent();
        } catch (err) {
          setApplyStatus("恢复快照失败：" + err.message, "error");
          toast("恢复快照失败：" + err.message, "error");
          okBtn.disabled = false;
          okBtn.textContent = willReload ? "确认恢复并热重载" : "确认恢复快照";
        } finally {
          store.applying = false;
          syncApplyControls();
        }
      });
    },
  });
}

function bindCoreUpdate(container) {
  const check = container.querySelector("#core-check");
  const install = container.querySelector("#core-install");
  const status = container.querySelector("#core-update-status");
  const releaseLink = container.querySelector("#core-release-link");
  let version = "";
  check.addEventListener("click", async () => {
    check.disabled = true;
    install.classList.add("hidden");
    releaseLink.classList.add("hidden");
    status.textContent = "正在查询官方稳定版…";
    try {
      const release = await apiFetch("/api/v1/core/update", { timeoutMs: 25000 });
      if (release.error) throw new Error(release.error);
      version = release.latest;
      status.textContent = "当前 " + release.current + " · 最新稳定版 " + version;
      releaseLink.href = release.url;
      releaseLink.classList.remove("hidden");
      if (release.installable && store.state.auth?.enabled) {
        install.classList.remove("hidden");
      } else if (release.installable) {
        status.textContent += " · 启用面板登录鉴权后可安装";
      } else if (release.available) {
        status.textContent += " · 当前环境请按官方说明更新";
      }
    } catch (err) {
      status.textContent = "检查失败：" + err.message;
    } finally {
      check.disabled = false;
    }
  });
  install.addEventListener("click", () => {
    if (store.dirty) {
      toast("请先保存面板中的配置改动", "error");
      return;
    }
    openModal({
      title: `安装 dae ${version}`,
      bodyHTML: `<div style="font-size:15px;line-height:1.65;color:var(--text-sec);">确认下载并安装官方稳定版 <code class="mono">dae ${escapeHTML(version)}</code> 吗？代理服务重启期间连接可能短暂中断。</div>`,
      footerHTML: `<button class="btn" id="m-core-cancel">取消</button><button class="btn btn-primary" id="m-core-ok">确认下载并安装</button>`,
      onMount(box) {
        box.querySelector("#m-core-cancel")?.addEventListener("click", closeModal);
        box.querySelector("#m-core-ok")?.addEventListener("click", async () => {
          closeModal();
          install.disabled = true;
          check.disabled = true;
          status.textContent = "正在下载、校验并更新内核…";
          try {
            const result = await apiFetch("/api/v1/core/update", {
              method: "POST",
              timeoutMs: 210000,
              body: JSON.stringify({ version }),
            });
            if (!result.ok) throw new Error(result.error || "安装未完成");
            install.classList.add("hidden");
            status.textContent = "已安装 dae " + version;
            store.doctor = await apiFetch("/api/v1/doctor");
            setText("side-dae-ver", (store.doctor.daeVersion || version).split("\n")[0].replace("dae version ", ""));
          } catch (err) {
            status.textContent = "更新结果：" + err.message;
            install.classList.add("hidden");
          } finally {
            install.disabled = false;
            check.disabled = false;
          }
        });
      },
    });
  });
}

function openUpdateRepoModal(container) {
  const currentCustom = store.state?.updateRepo || "";
  openModal({
    title: "设置面板更新源仓库",
    bodyHTML: `
      <div class="field full">
        <label class="field-label">GitHub 仓库地址 (格式：owner/repo 或完整 GitHub 链接，留空使用内置默认源)</label>
        <input id="m-up-repo" class="input mono" value="${escapeHTML(currentCustom)}" placeholder="${escapeHTML(store.updateRepo || "skylink9119/DaeStudio")}" />
        <span class="field-hint">当前生效更新源：<code class="mono">${escapeHTML(store.updateRepo || "未指定")}</code></span>
      </div>
    `,
    footerHTML: `
      <button class="btn" id="m-repo-cancel">取消</button>
      <button class="btn btn-primary" id="m-repo-save">保存更新源</button>
    `,
    onMount(box) {
      box.querySelector("#m-repo-cancel")?.addEventListener("click", closeModal);
      const saveBtn = box.querySelector("#m-repo-save");
      const input = box.querySelector("#m-up-repo");
      saveBtn?.addEventListener("click", async () => {
        saveBtn.disabled = true;
        try {
          const raw = input.value.trim();
          const res = await apiFetch("/api/v1/panel/repo", {
            method: "POST",
            body: JSON.stringify({ repo: raw }),
          });
          if (!res.ok) {
            toast(res.error || "保存更新源失败", "error");
            return;
          }
          if (store.state) store.state.updateRepo = raw ? res.repo : "";
          store.updateRepo = res.repo || "";
          closeModal();
          toast("面板更新源已更新", "success");
          renderSysLogs(container);
        } catch (err) {
          toast("保存更新源失败：" + err.message, "error");
        } finally {
          saveBtn.disabled = false;
        }
      });
      input?.focus();
    },
  });
}

function openPanelUpdateAuth(onReady) {
  openModal({
    title: "设置登录后继续升级",
    bodyHTML: `
      <p class="field-hint">在线升级需要管理员登录。保存后将自动重新检测更新。</p>
      <div class="field"><label class="field-label" for="panel-auth-user">管理员账号</label>
        <input id="panel-auth-user" class="input" autocomplete="username" value="${escapeHTML(store.state.auth?.username || "admin")}" /></div>
      <div class="field"><label class="field-label" for="panel-auth-pass">登录密码</label>
        <input id="panel-auth-pass" class="input" type="password" autocomplete="new-password" /></div>
      <div class="field"><label class="field-label" for="panel-auth-confirm">确认密码</label>
        <input id="panel-auth-confirm" class="input" type="password" autocomplete="new-password" /></div>
      <p id="panel-auth-error" class="field-hint" role="alert"></p>`,
    footerHTML: `<button class="btn" id="panel-auth-cancel">取消</button><button class="btn btn-primary" id="panel-auth-save">启用登录并继续</button>`,
    onMount(box) {
      box.querySelector("#panel-auth-cancel").addEventListener("click", closeModal);
      box.querySelector("#panel-auth-save").addEventListener("click", async () => {
        const btn = box.querySelector("#panel-auth-save");
        const error = box.querySelector("#panel-auth-error");
        const username = box.querySelector("#panel-auth-user").value.trim();
        const password = box.querySelector("#panel-auth-pass").value;
        if (!username || !password) { error.textContent = "请填写账号和密码"; return; }
        if (password !== box.querySelector("#panel-auth-confirm").value) { error.textContent = "两次密码不一致"; return; }
        btn.disabled = true;
        try {
          const res = await apiFetch("/api/v1/auth/update", { method: "POST", body: JSON.stringify({ enabled: true, username, password }) });
          if (!res.ok) throw new Error(res.error || "设置登录失败");
          store.token = res.token || store.token;
          localStorage.setItem("daed_next_token", store.token);
          store.state.auth = res.auth;
          closeModal();
          toast("登录保护已启用，请确认升级", "success");
          onReady();
        } catch (err) { error.textContent = err.message; }
        finally { btn.disabled = false; }
      });
      box.querySelector("#panel-auth-pass").focus();
    },
  });
}

function bindPanelUpdate(container) {
  const checkBtn = container.querySelector("#panel-check");
  const installBtn = container.querySelector("#panel-install");
  const authBtn = container.querySelector("#panel-auth-setup");
  const repoBtn = container.querySelector("#panel-repo-btn");
  const statusEl = container.querySelector("#panel-update-status");
  const releaseLink = container.querySelector("#panel-release-link");
  let latestVersion = "";
  let checkedRepo = "";
  authBtn?.addEventListener("click", () => openPanelUpdateAuth(() => checkBtn.click()));

  repoBtn?.addEventListener("click", () => openUpdateRepoModal(container));

  checkBtn?.addEventListener("click", async () => {
    checkBtn.disabled = true;
    installBtn.classList.add("hidden");
    authBtn?.classList.add("hidden");
    releaseLink.classList.add("hidden");
    try {
      statusEl.textContent = "正在查询 DaeStudio 最新版本…";
      const release = await apiFetch("/api/v1/panel/update", { timeoutMs: 25000 });
      if (release.needRepoConfig) {
        statusEl.textContent = "未配置默认更新仓库，请点击下方「更新源」设置 GitHub 仓库";
        openUpdateRepoModal(container);
        return;
      }
      if (release.error) throw new Error(release.error);
      latestVersion = release.latest;
      checkedRepo = release.repo;
      releaseLink.href = release.url;
      releaseLink.classList.remove("hidden");
      if (release.available) {
        statusEl.textContent = `当前 ${release.current} · 发现新版本 ${latestVersion}`;
        if (release.installable && store.state.auth?.enabled) {
          installBtn.classList.remove("hidden");
        } else if (release.installable) {
          statusEl.textContent += " · 请先设置管理员登录，再继续升级";
          authBtn?.classList.remove("hidden");
        } else {
          statusEl.textContent += " · " + (release.installReason || "当前环境请下载安装包手动升级");
        }
      } else {
        statusEl.textContent = `当前 ${release.current} · 已是最新正式版`;
      }
    } catch (err) {
      statusEl.textContent = "检测提示：" + err.message;
    } finally {
      checkBtn.disabled = false;
    }
  });

  installBtn?.addEventListener("click", () => {
    if (store.dirty) {
      toast("请先保存并应用面板中的未保存改动", "error");
      return;
    }
    openModal({
      title: `升级 DaeStudio 至 ${latestVersion}`,
      bodyHTML: `
        <div style="font-size:15px;line-height:1.65;color:var(--text-sec);">
          系统将下载对应架构二进制，核对 GitHub Release 摘要及 <code class="mono">SHA256SUMS</code> 并完成可执行文件自检后自动平滑重启面板服务。升级仅支持由独立服务管理的 dae。
        </div>
      `,
      footerHTML: `
        <button class="btn" id="m-p-cancel">取消</button>
        <button class="btn btn-primary" id="m-p-ok">确认下载并升级</button>
      `,
      onMount(box) {
        box.querySelector("#m-p-cancel")?.addEventListener("click", closeModal);
        box.querySelector("#m-p-ok")?.addEventListener("click", async () => {
          closeModal();
          installBtn.disabled = true;
          checkBtn.disabled = true;
          statusEl.textContent = "正在下载新版面板并校验 SHA-256…";
          try {
            const res = await apiFetch("/api/v1/panel/update", {
              method: "POST",
              timeoutMs: 180000,
              body: JSON.stringify({ version: latestVersion, repo: checkedRepo }),
            });
            if (!res.ok) throw new Error(res.error || "面板升级未完成");
            installBtn.classList.add("hidden");
            statusEl.textContent = `已写入 ${latestVersion}，正在平滑重启面板服务…`;
            for (let attempt = 0; attempt < 18; attempt++) {
              await new Promise((r) => setTimeout(r, 1500));
              try {
                const boot = await apiFetch("/api/v1/panel/version", { timeoutMs: 2500, cache: "no-store" });
                if (boot?.version === latestVersion) {
                  window.location.reload();
                  return;
                }
              } catch (_) {}
            }
            statusEl.textContent = `已替换二进制，但尚未检测到 ${latestVersion} 上线，请检查面板服务日志`;
          } catch (err) {
            statusEl.textContent = "升级结果：" + err.message;
          } finally {
            installBtn.disabled = false;
            checkBtn.disabled = false;
          }
        });
      },
    });
  });
}

const WEEKDAY_NAMES = ["周日", "周一", "周二", "周三", "周四", "周五", "周六"];

function formatGeoPresetName(preset) {
  const p = String(preset || "loyalsoldier").toLowerCase();
  if (p === "metacubex") return "MetaCubeX 规则库";
  if (p === "v2fly") return "v2fly 标准源";
  if (p === "custom") return "自定义规则源";
  return "Loyalsoldier 增强版";
}

function formatGeoScheduleBadge(cfg) {
  if (!cfg?.autoUpdate) return "手动更新";
  const t = cfg.scheduleTime || "04:00";
  if (cfg.scheduleMode === "weekly") {
    return `定时 · 每${WEEKDAY_NAMES[cfg.scheduleWeekday ?? 1] || "周一"} ${t}`;
  }
  if (cfg.scheduleMode === "interval") {
    return `定时 · 每 ${cfg.intervalDays || 3} 天 ${t}`;
  }
  return `定时 · 每天 ${t}`;
}

function formatDateTimeShort(iso) {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit" });
}

function openGeoDataConfigModal(container) {
  const cur = store.geoDataStatus?.config || store.state?.geoData || {
    preset: "loyalsoldier",
    autoUpdate: false,
    scheduleMode: "daily",
    scheduleTime: "04:00",
    scheduleWeekday: 1,
    intervalDays: 3,
  };

  openModal({
    title: "GeoIP / GeoSite 分流数据库更新与定时设置",
    bodyHTML: `
      <div class="form-grid">
        <div class="field full">
          <label class="field-label">规则数据库上游源</label>
          <select id="m-geo-preset" class="select">
            <option value="loyalsoldier" ${cur.preset !== "metacubex" && cur.preset !== "v2fly" && cur.preset !== "custom" ? "selected" : ""}>Loyalsoldier/v2ray-rules-dat 增强版（推荐 · 含 SHA-256 校验）</option>
            <option value="metacubex" ${cur.preset === "metacubex" ? "selected" : ""}>MetaCubeX/meta-rules-dat 规则库（含 SHA-256 校验）</option>
            <option value="v2fly" ${cur.preset === "v2fly" ? "selected" : ""}>v2fly 官方标准源（含 SHA-256 校验）</option>
            <option value="custom" ${cur.preset === "custom" ? "selected" : ""}>自定义 HTTPS 下载地址 / 加速镜像</option>
          </select>
        </div>

        <div id="m-geo-custom-box" class="field full ${cur.preset === "custom" ? "" : "hidden"}" style="display:flex;flex-direction:column;gap:10px;padding:12px 14px;border-radius:10px;background:var(--bg-subtle);border:1px solid var(--border);">
          <div class="field">
            <label class="field-label">geoip.dat 下载地址 (仅支持 HTTPS)</label>
            <input id="m-geo-ip-url" class="input mono" value="${escapeHTML(cur.geoipUrl || "")}" placeholder="https://.../geoip.dat" />
          </div>
          <div class="field">
            <label class="field-label">geosite.dat 下载地址 (仅支持 HTTPS)</label>
            <input id="m-geo-site-url" class="input mono" value="${escapeHTML(cur.geositeUrl || "")}" placeholder="https://.../geosite.dat" />
          </div>
          <div class="form-grid">
            <div class="field">
              <label class="field-label">geoip SHA-256 地址 (可选)</label>
              <input id="m-geo-ip-sum" class="input mono" value="${escapeHTML(cur.geoipSumUrl || "")}" placeholder="https://.../geoip.dat.sha256sum" />
            </div>
            <div class="field">
              <label class="field-label">geosite SHA-256 地址 (可选)</label>
              <input id="m-geo-site-sum" class="input mono" value="${escapeHTML(cur.geositeSumUrl || "")}" placeholder="https://.../geosite.dat.sha256sum" />
            </div>
          </div>
        </div>

        <div class="field full" style="padding-top:4px;border-top:1px solid var(--border);">
          <label class="toggle">
            <input type="checkbox" id="m-geo-auto" ${cur.autoUpdate ? "checked" : ""}/>
            <span class="toggle-slider"></span>
            <span style="font-weight:600;">启用定时自动更新（校验并替换后自动平滑热重载 dae）</span>
          </label>
          <span class="field-hint">若上游文件 SHA-256 与本机一致，将自动跳过重复下载与闪存写入</span>
        </div>

        <div class="field">
          <label class="field-label">自动更新频率</label>
          <select id="m-geo-mode" class="select">
            <option value="daily" ${cur.scheduleMode !== "weekly" && cur.scheduleMode !== "interval" ? "selected" : ""}>每天固定时间</option>
            <option value="weekly" ${cur.scheduleMode === "weekly" ? "selected" : ""}>每周固定时间</option>
            <option value="interval" ${cur.scheduleMode === "interval" ? "selected" : ""}>按间隔天数</option>
          </select>
        </div>

        <div class="field">
          <label class="field-label">执行时间 (软路由本地时间)</label>
          <input id="m-geo-time" type="time" class="input mono" value="${escapeHTML(cur.scheduleTime || "04:00")}" />
        </div>

        <div id="m-geo-week-wrap" class="field full ${cur.scheduleMode === "weekly" ? "" : "hidden"}">
          <label class="field-label">每周执行日</label>
          <select id="m-geo-weekday" class="select">
            ${WEEKDAY_NAMES.map((w, idx) => `<option value="${idx}" ${Number(cur.scheduleWeekday ?? 1) === idx ? "selected" : ""}>每${w}</option>`).join("")}
          </select>
        </div>

        <div id="m-geo-int-wrap" class="field full ${cur.scheduleMode === "interval" ? "" : "hidden"}">
          <label class="field-label">间隔天数 (1 ~ 30 天)</label>
          <input id="m-geo-days" type="number" min="1" max="30" class="input mono" value="${cur.intervalDays || 3}" />
        </div>
      </div>
    `,
    footerHTML: `
      <button class="btn" id="m-geo-cancel">取消</button>
      <button class="btn btn-primary" id="m-geo-save">保存设置</button>
    `,
    onMount(box) {
      const presetSel = box.querySelector("#m-geo-preset");
      const customBox = box.querySelector("#m-geo-custom-box");
      const modeSel = box.querySelector("#m-geo-mode");
      const weekWrap = box.querySelector("#m-geo-week-wrap");
      const intWrap = box.querySelector("#m-geo-int-wrap");
      const saveBtn = box.querySelector("#m-geo-save");

      presetSel?.addEventListener("change", () => {
        customBox?.classList.toggle("hidden", presetSel.value !== "custom");
      });
      modeSel?.addEventListener("change", () => {
        weekWrap?.classList.toggle("hidden", modeSel.value !== "weekly");
        intWrap?.classList.toggle("hidden", modeSel.value !== "interval");
      });
      box.querySelector("#m-geo-cancel")?.addEventListener("click", closeModal);
      saveBtn?.addEventListener("click", async () => {
        saveBtn.disabled = true;
        try {
          const payload = {
            preset: presetSel.value,
            geoipUrl: box.querySelector("#m-geo-ip-url")?.value.trim() || "",
            geositeUrl: box.querySelector("#m-geo-site-url")?.value.trim() || "",
            geoipSumUrl: box.querySelector("#m-geo-ip-sum")?.value.trim() || "",
            geositeSumUrl: box.querySelector("#m-geo-site-sum")?.value.trim() || "",
            autoUpdate: box.querySelector("#m-geo-auto").checked,
            scheduleMode: modeSel.value,
            scheduleTime: box.querySelector("#m-geo-time").value || "04:00",
            scheduleWeekday: Number(box.querySelector("#m-geo-weekday")?.value ?? 1),
            intervalDays: Math.max(1, Math.min(30, Number(box.querySelector("#m-geo-days")?.value || 3))),
          };
          const res = await apiFetch("/api/v1/geodata/config", {
            method: "POST",
            body: JSON.stringify(payload),
          });
          if (!res.ok) {
            toast(res.error || "保存设置失败", "error");
            return;
          }
          if (res.geoDataStatus) store.geoDataStatus = res.geoDataStatus;
          if (store.state && res.geoData) store.state.geoData = res.geoData;
          closeModal();
          toast(payload.autoUpdate ? "已开启分流数据库定时自动更新" : "分流数据库设置已保存", "success");
          renderSysLogs(container);
        } catch (err) {
          toast("保存设置失败：" + err.message, "error");
        } finally {
          saveBtn.disabled = false;
        }
      });
    },
  });
}

function bindGeoDataPanel(container) {
  const cfgBtn = container.querySelector("#geo-btn-cfg");
  const allBtn = container.querySelector("#geo-btn-all");
  const statusEl = container.querySelector("#geo-status-text");
  const targetBtns = container.querySelectorAll("[data-geo-target]");

  cfgBtn?.addEventListener("click", () => openGeoDataConfigModal(container));

  const triggerUpdate = async (target, label) => {
    if (store.geoUpdating) return;
    store.geoUpdating = true;
    if (allBtn) allBtn.disabled = true;
    if (cfgBtn) cfgBtn.disabled = true;
    targetBtns.forEach((b) => (b.disabled = true));
    if (statusEl) {
      statusEl.textContent = `正在拉取并校验 ${label}（含 dae 配置兼容预检，请稍候）…`;
    }
    try {
      const res = await apiFetch("/api/v1/geodata/update", {
        method: "POST",
        timeoutMs: 210000,
        body: JSON.stringify({ target }),
      });
      if (res.geoDataStatus) {
        store.geoDataStatus = res.geoDataStatus;
        if (store.state && res.geoDataStatus.config) {
          store.state.geoData = res.geoDataStatus.config;
        }
      }
      if (!res.ok) {
        throw new Error(res.error || "更新分流数据库失败");
      }
      toast(res.message || "分流数据库已更新", "success");
      store.geoUpdating = false;
      renderSysLogs(container);
    } catch (err) {
      store.geoUpdating = false;
      toast("更新失败：" + err.message, "error");
      renderSysLogs(container);
    }
  };

  allBtn?.addEventListener("click", () => triggerUpdate("all", "geoip.dat 与 geosite.dat"));
  targetBtns.forEach((btn) => {
    btn.addEventListener("click", () => {
      const t = btn.dataset.geoTarget;
      triggerUpdate(t, t === "geoip" ? "geoip.dat" : "geosite.dat");
    });
  });
}

function renderSysLogs(container) {
  const checks = store.doctor?.checks || [];
  const activeRepo = store.updateRepo || "未指定";
  const geoSt = store.geoDataStatus || {};
  const geoCfg = geoSt.config || store.state?.geoData || { preset: "loyalsoldier", autoUpdate: false, scheduleMode: "daily", scheduleTime: "04:00" };
  const geoIP = geoSt.geoip || {};
  const geoSite = geoSt.geosite || {};
  const busyGeo = Boolean(store.geoUpdating || geoSt.updating);
  const geoSubMsg = geoCfg.lastMessage
    ? `${geoCfg.lastStatus === "error" ? "上次异常：" : ""}${geoCfg.lastMessage}`
    : `存储目录 ${geoSt.dir || "/usr/share/dae"} · 更新前自动执行 SHA-256 与 dae 配置兼容预检`;

  container.innerHTML = `
    <div class="split-2">
      <div class="panel" style="display:flex;flex-direction:column;justify-content:space-between;">
        <div class="panel-head">
          <div>
            <div style="display:flex;align-items:center;gap:8px;">
              <span class="panel-title">DaeStudio 面板更新</span>
              <span class="tag tag-primary mono">${escapeHTML(store.panelVersion || "v1.0.9")}</span>
            </div>
            <div id="panel-update-status" class="section-sub" aria-live="polite">当前版本 ${escapeHTML(store.panelVersion || "v1.0.9")} · 支持在线检测与 SHA-256 校验升级</div>
          </div>
          <div class="toolbar">
            <a id="panel-release-link" class="btn btn-sm hidden" target="_blank" rel="noopener noreferrer">发行说明</a>
            <button class="btn btn-sm" id="panel-check">检测更新</button>
            <button class="btn btn-primary btn-sm hidden" id="panel-auth-setup">设置登录并继续</button>
            <button class="btn btn-primary btn-sm hidden" id="panel-install">一键升级</button>
          </div>
        </div>
        <div class="panel-foot">
          <span>更新仓库：<span class="mono">${escapeHTML(activeRepo)}</span></span>
          <button type="button" class="btn btn-subtle btn-sm" id="panel-repo-btn" style="height:26px;min-height:26px;padding:0 8px;font-size:12px;">更新源</button>
        </div>
      </div>

      <div class="panel" style="display:flex;flex-direction:column;justify-content:space-between;">
        <div class="panel-head">
          <div>
            <div class="panel-title">dae 内核更新</div>
            <div id="core-update-status" class="section-sub" aria-live="polite">${escapeHTML((store.doctor?.daeVersion || "").split("\n")[0])}</div>
          </div>
          <div class="toolbar">
            <a id="core-release-link" class="btn btn-sm hidden" target="_blank" rel="noopener noreferrer">发行说明</a>
            <button class="btn btn-sm" id="core-check">检查更新</button>
            <button class="btn btn-primary btn-sm hidden" id="core-install">下载并安装</button>
          </div>
        </div>
        <div class="panel-foot">
          <span>自动同步官方 <span class="mono">daeuniverse/dae</span> 稳定版内核并校验配置兼容性</span>
        </div>
      </div>
    </div>

    <div id="geodata-panel" class="panel">
      <div class="panel-head">
        <div>
          <div style="display:flex;align-items:center;gap:8px;flex-wrap:wrap;">
            <span class="panel-title">GeoIP / GeoSite 分流数据库</span>
            <span class="tag tag-primary">${escapeHTML(formatGeoPresetName(geoCfg.preset))}</span>
            <span class="tag ${geoCfg.autoUpdate ? "tag-ok" : ""} mono">${escapeHTML(formatGeoScheduleBadge(geoCfg))}</span>
          </div>
          <div id="geo-status-text" class="section-sub" aria-live="polite">${escapeHTML(geoSubMsg)}</div>
        </div>
        <div class="toolbar">
          <button class="btn btn-sm" id="geo-btn-cfg" ${busyGeo ? "disabled" : ""}>定时与源设置</button>
          <button class="btn btn-primary btn-sm" id="geo-btn-all" ${busyGeo ? "disabled" : ""}>${busyGeo ? "正在更新…" : "立即更新全部"}</button>
        </div>
      </div>

      <div class="geo-grid">
        <div class="geo-file-card">
          <div style="min-width:0;">
            <div class="geo-file-title">
              <span class="mono">geoip.dat</span>
              <span class="tag ${geoIP.exists ? "tag-ok" : "tag-warn"}">${geoIP.exists ? "已就绪" : "缺失"}</span>
            </div>
            <div class="geo-file-meta mono">
              ${geoIP.exists ? `${formatBytes(geoIP.sizeBytes)}${geoIP.modified ? ` · 文件时间 ${escapeHTML(formatDateTimeShort(geoIP.modified))}` : ""}` : "尚未检测到 geoip.dat，请点击更新自动下载"}
            </div>
          </div>
          <button type="button" class="btn btn-sm" data-geo-target="geoip" ${busyGeo ? "disabled" : ""}>更新 GeoIP</button>
        </div>

        <div class="geo-file-card">
          <div style="min-width:0;">
            <div class="geo-file-title">
              <span class="mono">geosite.dat</span>
              <span class="tag ${geoSite.exists ? "tag-ok" : "tag-warn"}">${geoSite.exists ? "已就绪" : "缺失"}</span>
            </div>
            <div class="geo-file-meta mono">
              ${geoSite.exists ? `${formatBytes(geoSite.sizeBytes)}${geoSite.modified ? ` · 文件时间 ${escapeHTML(formatDateTimeShort(geoSite.modified))}` : ""}` : "尚未检测到 geosite.dat，请点击更新自动下载"}
            </div>
          </div>
          <button type="button" class="btn btn-sm" data-geo-target="geosite" ${busyGeo ? "disabled" : ""}>更新 GeoSite</button>
        </div>
      </div>

      <div class="panel-foot">
        <span>${
          geoCfg.autoUpdate && geoSt.nextRunAt
            ? `下次自动更新：<strong class="mono" style="color:var(--text);">${escapeHTML(formatDateTimeShort(geoSt.nextRunAt))}</strong>（替换后自动平滑热重载 dae）`
            : "点击「定时与源设置」可开启每天 / 每周或按间隔天数自动更新，并切换上游规则源"
        }</span>
        ${geoCfg.lastUpdatedAt ? `<span class="mono">最近执行：${escapeHTML(formatDateTimeShort(geoCfg.lastUpdatedAt))}</span>` : ""}
      </div>
    </div>
    <div class="conn-bar">
      ${checks
        .map((c) => {
          const ok = c.status === "pass" || c.status === "info";
          const isWarn = c.status === "warn";
          const dotCls = ok ? "ok" : isWarn ? "warn" : "fail";
          const tagCls = ok ? "tag-ok" : isWarn ? "tag-warn" : "tag-err";
          return `
            <div class="conn-item ${ok ? "is-ok" : "is-fail"}" title="${escapeHTML(c.remediation || c.requirement || "")}">
              <div style="min-width:0;">
                <div class="conn-name">
                  <span class="status-dot ${dotCls}"></span>
                  <span>${escapeHTML(c.title.split(" (")[0])}</span>
                </div>
                <div class="text-muted mono" style="font-size: 12.5px;margin-top:3px;padding-left:17px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;">${escapeHTML(c.current.split("\n")[0])}</div>
              </div>
              <span class="tag ${tagCls} mono">${c.status.toUpperCase()}</span>
            </div>
          `;
        })
        .join("")}
    </div>

    <div class="panel">
      <div class="panel-head">
        <div class="toolbar">
          <span class="panel-title">实时运行日志</span>
          <select id="lg-lvl" class="select select-sm" style="width:115px;">
            ${["ALL", "info", "warn", "error", "debug"].map((l) => `<option value="${l}" ${store.logLevelFilter === l ? "selected" : ""}>${l === "ALL" ? "全部级别" : l.toUpperCase()}</option>`).join("")}
          </select>
          <input id="lg-q" class="input input-sm" style="width:200px;" placeholder="筛选日志内容..." value="${escapeHTML(store.logSearch)}" />
        </div>
        <button class="btn btn-sm" id="lg-clear">清空日志</button>
      </div>
      <div id="lg-box" class="log-box mono"></div>
    </div>
  `;

  bindPanelUpdate(container);
  bindCoreUpdate(container);
  bindGeoDataPanel(container);
  renderLogsListDOM();

  container.querySelector("#lg-lvl")?.addEventListener("change", (e) => { store.logLevelFilter = e.target.value; renderLogsListDOM(); });
  container.querySelector("#lg-q")?.addEventListener("input", (e) => { store.logSearch = e.target.value; renderLogsListDOM(); });
  container.querySelector("#lg-clear")?.addEventListener("click", async () => {
    const res = await apiFetch("/api/v1/logs/clear", { method: "POST", body: "{}" });
    store.logs = res.logs || [];
    renderLogsListDOM();
  });
}

function renderLogsListDOM() {
  const box = document.getElementById("lg-box");
  if (!box) return;
  const q = (store.logSearch || "").toLowerCase();
  const rows = (store.logs || []).filter((l) => {
    if (store.logLevelFilter !== "ALL" && l.level !== store.logLevelFilter) return false;
    if (q && !`${l.level} ${l.message}`.toLowerCase().includes(q)) return false;
    return true;
  });
  box.innerHTML =
    rows
      .map(
        (l) => `
        <div class="log-row">
          <span class="log-ts">[${escapeHTML(l.time)}]</span>
          <span class="log-${escapeHTML(l.level)}">${escapeHTML(l.level.toUpperCase())}</span>
          <span>${escapeHTML(l.message)}</span>
        </div>`
      )
      .join("") || `<div class="text-muted">暂无日志记录</div>`;
  box.scrollTop = box.scrollHeight;
}

// ============================================================================
// Modal & Auth Helpers
// ============================================================================

function openLoginModal() {
  openModal({
    title: "登录 DaeStudio",
    locked: true,
    bodyHTML: `
      <div class="field"><label class="field-label">管理员账号</label><input id="lg-u" class="input" value="admin" autocomplete="username" /></div>
      <div class="field"><label class="field-label">访问密码</label><input id="lg-p" type="password" class="input" placeholder="请输入面板密码" autocomplete="current-password" /></div>
    `,
    footerHTML: `<button class="btn btn-primary" id="lg-go">登录面板</button>`,
    onMount(box) {
      const uEl = box.querySelector("#lg-u");
      const pEl = box.querySelector("#lg-p");
      const goBtn = box.querySelector("#lg-go");
      const submitLogin = async () => {
        if (goBtn.disabled) return;
        goBtn.disabled = true;
        goBtn.textContent = "登录中…";
        try {
          const res = await apiFetch("/api/v1/auth/login", {
            method: "POST",
            body: JSON.stringify({
              username: uEl.value.trim(),
              password: pEl.value,
            }),
          });
          if (res.ok && res.token) {
            store.token = res.token;
            localStorage.setItem("daed_next_token", res.token);
            closeModal(true);
            await loadBootstrap();
            checkConnectivitySilent();
          } else {
            toast(res.error || "用户名或密码错误", "error");
          }
        } catch (err) {
          toast("登录失败：" + err.message, "error");
        } finally {
          goBtn.disabled = false;
          goBtn.textContent = "登录面板";
        }
      };
      goBtn?.addEventListener("click", submitLogin);
      const onEnter = (e) => {
        if (e.key === "Enter") {
          e.preventDefault();
          submitLogin();
        }
      };
      uEl?.addEventListener("keydown", onEnter);
      pEl?.addEventListener("keydown", onEnter);
      pEl?.focus();
    },
  });
}

function openModal({ title, bodyHTML, footerHTML, onMount, locked = false }) {
  modalLocked = Boolean(locked);
  document.getElementById("modal-title").textContent = title;
  document.getElementById("modal-body").innerHTML = bodyHTML;
  document.getElementById("modal-footer").innerHTML = footerHTML || "";
  document.getElementById("modal-close")?.classList.toggle("hidden", modalLocked);
  document.getElementById("modal-overlay").classList.remove("hidden");
  if (onMount) onMount(document.getElementById("modal-box"));
}

function closeModal(force = false) {
  if (modalLocked && force !== true) return;
  modalLocked = false;
  document.getElementById("modal-overlay")?.classList.add("hidden");
}

function toggleItem(arr, val) {
  const s = new Set(arr);
  if (s.has(val)) s.delete(val);
  else s.add(val);
  return [...s];
}

function splitCSV(str) {
  return String(str || "")
    .split(/[,，]/)
    .map((x) => x.trim())
    .filter(Boolean);
}

initApp();
