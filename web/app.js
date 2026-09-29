// adcp-test web UI — vanilla JS, no dependencies.

// Screen navigation: buttons with data-screen switch the visible section.
// Disabled buttons stay hidden until their screen is ready.
document.querySelectorAll("nav button[data-screen]").forEach((btn) => {
  btn.addEventListener("click", () => showScreen(btn.dataset.screen));
});

// Dashboard capability cards: data-goto jumps to a screen.
document.querySelectorAll("[data-goto]").forEach((btn) => {
  btn.addEventListener("click", () => showScreen(btn.dataset.goto));
});

function showScreen(name) {
  document.querySelectorAll("nav button[data-screen]").forEach((b) => {
    b.classList.toggle("active", b.dataset.screen === name);
  });
  document.querySelectorAll(".screen").forEach((s) => {
    s.classList.toggle("hidden", s.id !== "screen-" + name);
  });
  if (name === "inspect") {
    refreshInspect().catch((err) => console.error("inspect refresh failed", err));
  }
  if (name === "mock") {
    refreshMock().catch((err) => console.error("mock refresh failed", err));
  }
  if (name === "scenarios") {
    refreshScenarios().catch((err) => console.error("scenarios refresh failed", err));
  }
  if (name === "load") {
    refreshLoad().catch((err) => console.error("load refresh failed", err));
  }
  if (name === "webhooks") {
    refreshWebhooks().catch((err) => console.error("webhooks refresh failed", err));
  }
  if (name === "snapshots") {
    refreshSnapshots().catch((err) => console.error("snapshots refresh failed", err));
  }
  if (name === "reports") {
    refreshReports().catch((err) => console.error("reports refresh failed", err));
  }
  if (name !== "webhooks") {
    stopWebhookPoll();
  }
}

function esc(s) {
  return String(s == null ? "" : s).replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  })[c]);
}

// lastReports keeps the latest run report per kind so the Snapshots and
// Reports screens can reuse them without re-running.
const lastReports = { conformance: null, scenarios: null, load: null, lifecycle: null, fuzz: null };

// Conformance screen: POST the target URL, render the report.
const conformanceForm = document.getElementById("conformance-form");
if (conformanceForm) {
  conformanceForm.addEventListener("submit", runConformance);
}

async function runConformance(event) {
  event.preventDefault();
  const targetUrl = document.getElementById("target-url").value.trim();
  const bearerToken = document.getElementById("bearer-token").value;
  const btn = document.getElementById("run-btn");
  const results = document.getElementById("conformance-results");

  btn.disabled = true;
  btn.textContent = "Running…";
  results.innerHTML = '<p class="meta">Running conformance checks…</p>';

  try {
    const resp = await fetch("/api/conformance/run", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        target_url: targetUrl,
        bearer_token: bearerToken || undefined,
      }),
    });
    const report = await resp.json();
    if (!resp.ok) {
      results.innerHTML = errorCard(report.error || "request failed");
    } else {
      lastReports.conformance = report;
      results.innerHTML = renderReport(report);
    }
  } catch (err) {
    results.innerHTML = errorCard("request failed: " + err.message);
  } finally {
    btn.disabled = false;
    btn.textContent = "Run conformance";
  }
}

function errorCard(msg) {
  return `<div class="card error"><h3>Error</h3><p>${esc(msg)}</p></div>`;
}

function renderReport(rep) {
  const s = rep.summary || { total: 0, passed: 0, failed: 0, skipped: 0 };
  const cls = s.failed > 0 ? "failed" : "passed";
  const banner = `
    <div class="card summary ${cls}">
      <h3>Conformance report</h3>
      <p>Target: <code>${esc(rep.target_url)}</code></p>
      <p><strong>${s.passed}</strong> passed,
         <strong>${s.failed}</strong> failed,
         <strong>${s.skipped}</strong> skipped,
         of ${s.total} checks.</p>
    </div>`;
  const rows = (rep.checks || []).map((c) => `
    <div class="check">
      <span class="badge ${esc(c.status)}">${esc(c.status).toUpperCase()}</span>
      <span class="check-name">${esc(c.name)}</span>
      <span class="check-dur">${Number(c.duration_ms).toFixed(0)} ms</span>
      <details><summary>detail</summary><p>${esc(c.detail)}</p></details>
    </div>`).join("");
  return banner + `<div class="card"><h3>Checks</h3>${rows || '<p class="meta">No checks ran.</p>'}</div>`;
}

console.log("adcp-test shell loaded");

// ---------------------------------------------------------------------------
// Inspect screen: sessions timeline, record/replay controls.
// ---------------------------------------------------------------------------

async function fetchJSON(url, opts) {
  const r = await fetch(url, opts);
  const data = await r.json().catch(() => ({}));
  if (!r.ok) {
    throw new Error((data && data.error) || ("request failed: " + r.status));
  }
  return data;
}

function fmtTime(s) {
  if (!s) return "";
  const d = new Date(s);
  return isNaN(d) ? String(s) : d.toLocaleString();
}

async function refreshInspect() {
  await Promise.all([refreshSessions(), refreshCassettes()]);
}

async function refreshSessions() {
  const el = document.getElementById("sessions-list");
  const sessions = await fetchJSON("/api/sessions");
  if (!sessions.length) {
    el.innerHTML = '<p class="meta">No sessions yet. Start a recording above.</p>';
    return;
  }
  el.innerHTML = sessions.map((s) => `
    <div class="sessrow">
      <button class="linklike" data-session="${esc(s.id)}">${esc(s.id)}</button>
      <span class="meta">${esc(s.target_url || "")} · ${s.steps} steps · ${esc(fmtTime(s.started_at))}</span>
    </div>`).join("");
  el.querySelectorAll("[data-session]").forEach((b) =>
    b.addEventListener("click", () => showSession(b.dataset.session)));
}

async function showSession(id) {
  const detail = document.getElementById("session-detail");
  detail.innerHTML = '<p class="meta">Loading…</p>';
  try {
    const s = await fetchJSON("/api/sessions/" + encodeURIComponent(id));
    detail.innerHTML = renderTimeline(s);
  } catch (err) {
    detail.innerHTML = errorCard(err.message);
  }
}

function payloadDetails(label, payload) {
  let pretty;
  try {
    pretty = JSON.stringify(typeof payload === "string" ? JSON.parse(payload) : payload, null, 2);
  } catch (e) {
    pretty = String(payload);
  }
  return `<details><summary>${label}</summary><pre class="payload">${esc(pretty)}</pre></details>`;
}

function renderTimeline(s) {
  const steps = (s.steps || []).map((st) => {
    const dirCls = st.direction === "out" ? "dir-out" : "dir-in";
    const dirLabel = st.direction === "out" ? "OUT" : "IN";
    const tool = esc(st.method) + (st.tool ? " · " + esc(st.tool) : "");
    let meta = "#" + esc(st.request_id) + " · " + esc(fmtTime(st.ts));
    if (st.duration_ms) {
      meta += " · " + Number(st.duration_ms).toFixed(1) + " ms";
    }
    let extra = "";
    if (st.error) {
      extra += `<p class="step-error">${esc(st.error)}</p>`;
    }
    if (st.arg_problems && st.arg_problems.length) {
      extra += `<span class="badge warn">ARGS · ${st.arg_problems.length} issue${st.arg_problems.length > 1 ? "s" : ""}</span>` +
        `<ul class="problems">${st.arg_problems.map((p) => `<li>${esc(p)}</li>`).join("")}</ul>`;
    } else if (st.direction === "out" && st.method === "tools/call") {
      extra += `<span class="badge ok">args ok</span>`;
    }
    const reqBlock = st.request ? payloadDetails("request", st.request) : "";
    const resBlock = st.response ? payloadDetails("response", st.response) : "";
    return `<div class="step">
      <span class="badge ${dirCls}">${dirLabel}</span>
      <span class="step-tool">${tool}</span>
      <span class="step-meta">${meta}</span>
      <div class="step-body">${extra}${reqBlock}${resBlock}</div>
    </div>`;
  }).join("");
  const n = (s.steps || []).length;
  return `<div class="card"><h3>Session <code>${esc(s.id)}</code></h3>
    <p class="meta">${esc(s.target_url || "")} · ${n} steps</p>
    ${steps || '<p class="meta">No steps recorded.</p>'}</div>`;
}

// Record flow.
let activeRecording = null;

const recordForm = document.getElementById("record-form");
if (recordForm) {
  recordForm.addEventListener("submit", async (event) => {
    event.preventDefault();
    const upstream = document.getElementById("record-upstream").value.trim();
    const btn = document.getElementById("record-start-btn");
    const status = document.getElementById("record-status");
    btn.disabled = true;
    try {
      const data = await fetchJSON("/api/record/start", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ upstream_url: upstream }),
      });
      activeRecording = data;
      status.innerHTML = `
        <p><strong>Recording.</strong> Proxy: <code>${esc(data.proxy_url)}</code></p>
        <p class="meta">Point your buyer client at the proxy URL and send header
           <code>X-Session-ID: ${esc(data.session_id)}</code> to pin traffic to this session.</p>
        <button id="record-stop-btn" type="button">Stop &amp; save cassette</button>`;
      document.getElementById("record-stop-btn").addEventListener("click", stopRecording);
    } catch (err) {
      status.innerHTML = errorCard(err.message);
    } finally {
      btn.disabled = false;
    }
  });
}

async function stopRecording() {
  const status = document.getElementById("record-status");
  if (!activeRecording) return;
  try {
    const data = await fetchJSON("/api/record/stop", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ session_id: activeRecording.session_id }),
    });
    status.innerHTML = `<p>Cassette saved: <code>${esc(data.cassette)}</code> (${data.exchanges} exchanges).</p>`;
    activeRecording = null;
    await refreshInspect();
  } catch (err) {
    status.innerHTML = errorCard(err.message);
  }
}

// Replay flow.
let activeReplay = null;

async function refreshCassettes() {
  const list = document.getElementById("cassettes-list");
  const sel = document.getElementById("replay-cassette");
  const cassettes = await fetchJSON("/api/cassettes");
  list.innerHTML = cassettes.length
    ? cassettes.map((c) => `
      <div class="sessrow"><code>${esc(c.name)}</code>
        <span class="meta">${esc(c.target || "")} · ${c.exchanges} exchanges · ${esc(fmtTime(c.recorded_at))}</span>
      </div>`).join("")
    : '<p class="meta">No cassettes yet. Record a session and stop it to save one.</p>';
  sel.innerHTML = cassettes.map((c) => `<option value="${esc(c.name)}">${esc(c.name)}</option>`).join("");
}

const replayForm = document.getElementById("replay-form");
if (replayForm) {
  replayForm.addEventListener("submit", async (event) => {
    event.preventDefault();
    const cassetteName = document.getElementById("replay-cassette").value;
    const strict = document.getElementById("replay-strict").checked;
    const btn = document.getElementById("replay-start-btn");
    const status = document.getElementById("replay-status");
    if (!cassetteName) {
      status.innerHTML = errorCard("no cassette selected");
      return;
    }
    btn.disabled = true;
    try {
      const data = await fetchJSON("/api/replay/start", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ cassette: cassetteName, strict }),
      });
      activeReplay = data;
      status.innerHTML = `
        <p><strong>Replaying.</strong> Fake seller: <code>${esc(data.replay_url)}</code>
           ${strict ? "(strict matching)" : "(lenient matching)"}</p>
        <button id="replay-stop-btn" type="button">Stop replay server</button>`;
      document.getElementById("replay-stop-btn").addEventListener("click", stopReplay);
    } catch (err) {
      status.innerHTML = errorCard(err.message);
    } finally {
      btn.disabled = false;
    }
  });
}

async function stopReplay() {
  const status = document.getElementById("replay-status");
  if (!activeReplay) return;
  try {
    await fetchJSON("/api/replay/stop", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ replay_id: activeReplay.replay_id }),
    });
    status.innerHTML = '<p class="meta">Replay server stopped.</p>';
    activeReplay = null;
  } catch (err) {
    status.innerHTML = errorCard(err.message);
  }
}

// ---------------------------------------------------------------------------
// Mock builder screen: visual config editor, YAML preview, start/stop,
// one-click record mode.
// ---------------------------------------------------------------------------

// mockSvc holds the service being edited; mockRest preserves any other
// services in a multi-service (compose) config untouched.
let mockSvc = null;
let mockRest = [];
let mockEdits = []; // per-route editing state
let mockValidateTimer = null;
let mockLoaded = false;

function blankRouteEdit() {
  return {
    tool: "", argsText: "",
    respMode: "inline", respText: '{\n  "ok": true\n}',
    latProfile: "none", fixed: "50ms", p50: "40ms", p99: "250ms",
    spike: false, spikeEvery: "10s", spikeFor: "2s", spikeAdd: "1s",
    errRate: 0, timeoutRate: 0, malformedRate: 0,
    stateMachine: "none",
  };
}

// editFromModel converts one config route into editing state.
function editFromModel(r) {
  const e = blankRouteEdit();
  e.tool = (r.match && r.match.tool) || "";
  const argLines = [];
  const flat = flattenArgs((r.match && r.match.args) || {}, "", {});
  for (const [p, v] of Object.entries(flat)) argLines.push(p + ": " + v);
  e.argsText = argLines.join("\n");
  const resp = r.respond || {};
  if (typeof resp.file === "string") { e.respMode = "file"; e.respText = resp.file; }
  else if (typeof resp.template === "string") { e.respMode = "template"; e.respText = resp.template; }
  else if (Array.isArray(resp.sequence)) {
    e.respMode = "sequence";
    e.respText = resp.sequence.map((it) => {
      if (typeof it === "string") return it;
      const inl = it && typeof it === "object" && "inline" in it ? it.inline : it;
      return JSON.stringify(inl);
    }).join("\n");
  } else {
    e.respMode = "inline";
    const inl = resp.inline !== undefined ? resp.inline : resp;
    e.respText = JSON.stringify(inl, null, 2);
  }
  const lat = r.latency || {};
  if (lat.fixed) { e.latProfile = "fixed"; e.fixed = lat.fixed; }
  else if (lat.p50 || lat.p99) { e.latProfile = "distribution"; if (lat.p50) e.p50 = lat.p50; if (lat.p99) e.p99 = lat.p99; }
  if (lat.spike) {
    e.spike = true;
    e.spikeEvery = lat.spike.every || e.spikeEvery;
    e.spikeFor = lat.spike.for || e.spikeFor;
    e.spikeAdd = lat.spike.add || e.spikeAdd;
  }
  const f = r.faults || {};
  e.errRate = Math.round((f.error_rate || 0) * 100);
  e.timeoutRate = Math.round((f.timeout_rate || 0) * 100);
  e.malformedRate = Math.round((f.malformed_rate || 0) * 100);
  e.stateMachine = r.state_machine || "none";
  return e;
}

function flattenArgs(obj, prefix, out) {
  for (const [k, v] of Object.entries(obj || {})) {
    const p = prefix ? prefix + "." + k : k;
    if (v && typeof v === "object" && !Array.isArray(v)) flattenArgs(v, p, out);
    else out[p] = typeof v === "string" ? v : JSON.stringify(v);
  }
  return out;
}

function parseScalar(v) {
  try { return JSON.parse(v); } catch { return v; }
}

function parseArgsText(text) {
  const args = {};
  for (const line of text.split("\n")) {
    const t = line.trim();
    if (!t) continue;
    const ci = t.indexOf(":");
    if (ci < 0) throw new Error("argument pattern needs 'path: value' — got: " + t);
    args[t.slice(0, ci).trim()] = parseScalar(t.slice(ci + 1).trim());
  }
  return args;
}

// buildMockModel converts the editing state back into a config model.
function buildMockModel() {
  const routes = mockEdits.map((e) => {
    if (!e.tool.trim()) throw new Error("every route needs a tool name");
    const route = { match: { tool: e.tool.trim() } };
    const args = parseArgsText(e.argsText);
    if (Object.keys(args).length) route.match.args = args;
    const resp = {};
    if (e.respMode === "file") {
      if (!e.respText.trim()) throw new Error("file response needs a path");
      resp.file = e.respText.trim();
    } else if (e.respMode === "template") {
      if (!e.respText.trim()) throw new Error("template response needs a path");
      resp.template = e.respText.trim();
    } else if (e.respMode === "sequence") {
      const items = e.respText.split("\n").map((l) => l.trim()).filter(Boolean).map((line) => {
        if (line.startsWith("{") || line.startsWith("[")) {
          try { return { inline: JSON.parse(line) }; }
          catch { throw new Error("sequence line is not valid JSON or a file path: " + line); }
        }
        return line;
      });
      if (!items.length) throw new Error("sequence needs at least one entry");
      resp.sequence = items;
    } else {
      try { resp.inline = JSON.parse(e.respText); }
      catch { throw new Error("inline response is not valid JSON"); }
    }
    route.respond = resp;
    const lat = {};
    if (e.latProfile === "fixed") lat.fixed = e.fixed.trim();
    else if (e.latProfile === "distribution") { lat.p50 = e.p50.trim(); lat.p99 = e.p99.trim(); }
    if (e.spike) lat.spike = { every: e.spikeEvery.trim(), for: e.spikeFor.trim(), add: e.spikeAdd.trim() };
    if (Object.keys(lat).length) route.latency = lat;
    if (e.errRate > 0 || e.timeoutRate > 0 || e.malformedRate > 0) {
      route.faults = {
        error_rate: e.errRate / 100,
        timeout_rate: e.timeoutRate / 100,
        malformed_rate: e.malformedRate / 100,
      };
    }
    if (e.stateMachine && e.stateMachine !== "none") route.state_machine = e.stateMachine;
    return route;
  });
  const svc = {
    name: document.getElementById("mock-name").value.trim(),
    protocol: "adcp",
    listen: document.getElementById("mock-listen").value.trim(),
    routes,
  };
  const up = document.getElementById("mock-record-upstream").value.trim();
  const cap = document.getElementById("mock-record-capture").value.trim();
  if (up) svc.record = { upstream: up, capture_to: cap };
  return { mocks: [svc, ...mockRest] };
}

async function refreshMock() {
  const data = await fetchJSON("/api/mock/config");
  const model = normalizeMockModel(data.model);
  mockRest = model.mocks.slice(1);
  const svc = model.mocks[0] || { name: "seller", listen: ":8080", routes: [] };
  mockSvc = svc;
  document.getElementById("mock-name").value = svc.name || "";
  document.getElementById("mock-listen").value = svc.listen || "";
  document.getElementById("mock-record-upstream").value = (svc.record && svc.record.upstream) || "";
  document.getElementById("mock-record-capture").value = (svc.record && svc.record.capture_to) || "";
  mockEdits = (svc.routes || []).map(editFromModel);
  document.getElementById("mock-yaml").textContent = data.yaml || "";
  const note = document.getElementById("mock-multi-note");
  note.innerHTML = mockRest.length
    ? `<p class="meta">This config defines ${mockRest.length + 1} services (compose); the editor shows the first — the rest are preserved as-is.</p>`
    : "";
  renderMockRoutes();
  renderMockRunning(data.running || []);
  mockLoaded = true;
}

function normalizeMockModel(model) {
  model = model || {};
  if (model.mock && !model.mocks) return { mocks: [model.mock] };
  if (!Array.isArray(model.mocks) || !model.mocks.length) {
    return { mocks: [{ name: "seller", listen: ":8080", routes: [] }] };
  }
  return model;
}

function renderMockRoutes() {
  const el = document.getElementById("mock-routes");
  if (!mockEdits.length) {
    el.innerHTML = '<p class="meta">No routes yet — add one.</p>';
    return;
  }
  el.innerHTML = mockEdits.map((e, i) => `
    <div class="routecard" data-idx="${i}">
      <div class="routecard-head">
        <strong>${esc(e.tool || "(unnamed)")}</strong>
        <button type="button" class="linklike" data-del="${i}">delete</button>
      </div>
      <label>Tool <input data-f="tool" value="${esc(e.tool)}" placeholder="get_products"></label>
      <label>Argument patterns <span class="meta">one per line: <code>path: glob</code></span>
        <textarea data-f="argsText" rows="2" placeholder="buyer_ref: acme*">${esc(e.argsText)}</textarea></label>
      <label>Response mode
        <select data-f="respMode">
          ${["inline", "file", "sequence", "template"].map((m) =>
            `<option value="${m}"${e.respMode === m ? " selected" : ""}>${m}</option>`).join("")}
        </select></label>
      <label>Response <span class="meta">${esc(respHint(e.respMode))}</span>
        <textarea data-f="respText" rows="4" spellcheck="false">${esc(e.respText)}</textarea></label>
      <div class="grid2">
        <label>Latency
          <select data-f="latProfile">
            ${["none", "fixed", "distribution"].map((m) =>
              `<option value="${m}"${e.latProfile === m ? " selected" : ""}>${m}</option>`).join("")}
          </select></label>
        <label>State machine
          <select data-f="stateMachine">
            <option value="none"${e.stateMachine === "none" ? " selected" : ""}>none</option>
            <option value="media-buy-lifecycle"${e.stateMachine === "media-buy-lifecycle" ? " selected" : ""}>media-buy-lifecycle</option>
          </select></label>
      </div>
      <div class="latency-inputs" ${e.latProfile === "none" ? 'hidden' : ""}>
        ${e.latProfile === "fixed"
          ? `<label>Fixed <input data-f="fixed" value="${esc(e.fixed)}"></label>`
          : e.latProfile === "distribution"
            ? `<div class="grid2"><label>p50 <input data-f="p50" value="${esc(e.p50)}"></label>
               <label>p99 <input data-f="p99" value="${esc(e.p99)}"></label></div>`
            : ""}
        <label class="checkline"><input type="checkbox" data-f="spike" ${e.spike ? "checked" : ""}> spike</label>
        <div class="grid3" ${e.spike ? "" : "hidden"}>
          <label>every <input data-f="spikeEvery" value="${esc(e.spikeEvery)}"></label>
          <label>for <input data-f="spikeFor" value="${esc(e.spikeFor)}"></label>
          <label>add <input data-f="spikeAdd" value="${esc(e.spikeAdd)}"></label>
        </div>
      </div>
      <div class="faults">
        ${[["errRate", "error"], ["timeoutRate", "timeout"], ["malformedRate", "malformed"]].map(([f, name]) => `
          <label>${name} <span data-out="${f}">${e[f]}%</span>
            <input type="range" min="0" max="100" value="${e[f]}" data-f="${f}" data-range="1"></label>`).join("")}
      </div>
    </div>`).join("");
}

function respHint(mode) {
  return {
    inline: "JSON result payload",
    file: "path to a JSON file (relative to the server working dir)",
    sequence: "one per line: file path, or inline JSON",
    template: "path to a JSON template file; {{args.path}} placeholders",
  }[mode] || "";
}

// Delegated editing: any input inside a route card updates the edit state
// and revalidates (debounced).
const mockRoutesEl = document.getElementById("mock-routes");
if (mockRoutesEl) {
  mockRoutesEl.addEventListener("input", onMockEdit);
  mockRoutesEl.addEventListener("change", onMockEdit);
  mockRoutesEl.addEventListener("click", (ev) => {
    const del = ev.target.closest("[data-del]");
    if (del) {
      mockEdits.splice(Number(del.dataset.del), 1);
      renderMockRoutes();
      scheduleMockValidate();
    }
  });
}

function onMockEdit(ev) {
  const card = ev.target.closest(".routecard");
  if (!card) return;
  const e = mockEdits[Number(card.dataset.idx)];
  const f = ev.target.dataset.f;
  if (!e || !f) return;
  if (ev.target.dataset.range) {
    e[f] = Number(ev.target.value);
    const out = card.querySelector(`[data-out="${f}"]`);
    if (out) out.textContent = e[f] + "%";
  } else if (ev.target.type === "checkbox") {
    e[f] = ev.target.checked;
    renderMockRoutes(); // reveal/hide spike inputs
  } else {
    e[f] = ev.target.value;
    if (f === "respMode" || f === "latProfile") renderMockRoutes(); // swap sub-inputs
  }
  scheduleMockValidate();
}

const mockServiceForm = document.getElementById("mock-service-form");
if (mockServiceForm) {
  mockServiceForm.addEventListener("input", scheduleMockValidate);
}

const mockAddBtn = document.getElementById("mock-add-route");
if (mockAddBtn) {
  mockAddBtn.addEventListener("click", () => {
    mockEdits.push(blankRouteEdit());
    renderMockRoutes();
    scheduleMockValidate();
  });
}

function scheduleMockValidate() {
  if (!mockLoaded) return;
  clearTimeout(mockValidateTimer);
  mockValidateTimer = setTimeout(() => doMockValidate(true), 400);
}

// doMockValidate validates the editor model; when quiet is false the
// result is announced. Valid models auto-save.
async function doMockValidate(quiet) {
  const errBox = document.getElementById("mock-errors");
  const yamlEl = document.getElementById("mock-yaml");
  let model;
  try {
    model = buildMockModel();
  } catch (err) {
    errBox.innerHTML = errorCard(err.message);
    return { valid: false };
  }
  let res;
  try {
    res = await fetchJSON("/api/mock/validate", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ model }),
    });
  } catch (err) {
    errBox.innerHTML = errorCard(err.message);
    return { valid: false };
  }
  if (res.valid) {
    yamlEl.textContent = res.yaml || "";
    errBox.innerHTML = quiet ? "" : '<p class="meta ok">Config is valid.</p>';
    try {
      await fetchJSON("/api/mock/config", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ model }),
      });
    } catch (err) {
      errBox.innerHTML = errorCard("auto-save failed: " + err.message);
      return { valid: false };
    }
    return { valid: true };
  }
  yamlEl.textContent = res.yaml || yamlEl.textContent;
  errBox.innerHTML = `<div class="card error"><h3>Invalid config</h3><ul class="problems">${
    (res.errors || []).map((e) => `<li>${esc(e)}</li>`).join("")}</ul></div>`;
  return { valid: false };
}

const mockValidateBtn = document.getElementById("mock-validate-btn");
if (mockValidateBtn) {
  mockValidateBtn.addEventListener("click", () => doMockValidate(false));
}

function renderMockRunning(running) {
  const el = document.getElementById("mock-status");
  if (!running.length) {
    el.innerHTML = '<p class="meta">No mocks running.</p>';
    return;
  }
  el.innerHTML = `<ul class="problems">${
    running.map((m) => `<li><strong>${esc(m.name)}</strong> — <code>${esc(m.url)}</code></li>`).join("")
  }</ul>`;
}

const mockStartBtn = document.getElementById("mock-start-btn");
if (mockStartBtn) {
  mockStartBtn.addEventListener("click", async () => {
    const status = document.getElementById("mock-status");
    const v = await doMockValidate(true);
    if (!v.valid) {
      status.innerHTML = errorCard("Fix the config errors before starting.");
      return;
    }
    mockStartBtn.disabled = true;
    try {
      const res = await fetchJSON("/api/mock/start", { method: "POST" });
      renderMockRunning(res.started || []);
    } catch (err) {
      status.innerHTML = errorCard(err.message);
    } finally {
      mockStartBtn.disabled = false;
    }
  });
}

const mockStopBtn = document.getElementById("mock-stop-btn");
if (mockStopBtn) {
  mockStopBtn.addEventListener("click", async () => {
    const status = document.getElementById("mock-status");
    try {
      const res = await fetchJSON("/api/mock/stop", { method: "POST" });
      renderMockRunning([]);
      const caps = (res.captured || []).map((c) => `<li>${esc(c.name)} → <code>${esc(c.path)}</code></li>`).join("");
      status.innerHTML = '<p class="meta">Mocks stopped.</p>' +
        (caps ? `<ul class="problems">${caps}</ul>` : "");
    } catch (err) {
      status.innerHTML = errorCard(err.message);
    }
  });
}

// Record mode: start a capture proxy, then finish to generate a config.
const mockRecordForm = document.getElementById("mock-record-form");
if (mockRecordForm) {
  mockRecordForm.addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const status = document.getElementById("mock-record-status");
    const btn = document.getElementById("mock-record-start-btn");
    const upstream = document.getElementById("mock-record-url").value.trim();
    btn.disabled = true;
    try {
      const res = await fetchJSON("/api/mock/record", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ upstream_url: upstream }),
      });
      status.innerHTML = `
        <p>Capturing through <code>${esc(res.proxy_url)}</code> — point your buyer
           client at it and generate traffic, then:</p>
        <button type="button" id="mock-record-finish-btn" class="primary"
                data-record-id="${esc(res.record_id)}">Finish &amp; generate config</button>`;
      document.getElementById("mock-record-finish-btn").addEventListener("click", finishMockRecord);
    } catch (err) {
      status.innerHTML = errorCard(err.message);
    } finally {
      btn.disabled = false;
    }
  });
}

async function finishMockRecord(ev) {
  const status = document.getElementById("mock-record-status");
  const recordId = ev.target.dataset.recordId;
  ev.target.disabled = true;
  try {
    const res = await fetchJSON("/api/mock/record/finish", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ record_id: recordId }),
    });
    await fetchJSON("/api/mock/config", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ model: res.model }),
    });
    status.innerHTML = `<p class="meta ok">Generated a config from ${res.exchanges} captured exchange(s) — loaded into the editor.</p>`;
    await refreshMock();
  } catch (err) {
    status.innerHTML = errorCard(err.message);
    ev.target.disabled = false;
  }
}

// ---------------------------------------------------------------------------
// Shared SSE reader: POST a JSON body, stream event frames.
// ---------------------------------------------------------------------------

// readSSE posts body to url and invokes onEvent(eventName, data) for each
// server-sent event frame until the stream closes.
async function readSSE(url, body, onEvent) {
  const resp = await fetch(url, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!resp.ok) {
    let msg = "request failed";
    try {
      const errBody = await resp.json();
      if (errBody && errBody.error) msg = errBody.error;
    } catch (_) { /* keep default */ }
    throw new Error(msg);
  }
  const reader = resp.body.getReader();
  const decoder = new TextDecoder();
  let buf = "";
  let event = "";
  let data = "";
  const flush = () => {
    if (event) {
      let parsed = null;
      try { parsed = JSON.parse(data); } catch (_) { parsed = data; }
      onEvent(event, parsed);
    }
    event = "";
    data = "";
  };
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    buf += decoder.decode(value, { stream: true });
    let idx;
    while ((idx = buf.indexOf("\n")) >= 0) {
      const line = buf.slice(0, idx).replace(/\r$/, "");
      buf = buf.slice(idx + 1);
      if (line === "") { flush(); continue; }
      if (line.startsWith("event:")) event = line.slice(6).trim();
      else if (line.startsWith("data:")) data += line.slice(5);
    }
  }
  flush();
}

// ---------------------------------------------------------------------------
// Scenarios screen: built-in packs as runnable cards, live progress,
// drill into step results and assertion failures.
// ---------------------------------------------------------------------------

let scenarioPacks = [];
let scenarioRunSeq = 0;

async function refreshScenarios() {
  const box = document.getElementById("scenario-packs");
  try {
    const resp = await fetch("/api/scenarios");
    const body = await resp.json();
    scenarioPacks = body.packs || [];
  } catch (err) {
    box.innerHTML = errorCard("could not load scenario packs: " + err.message);
    return;
  }
  if (!scenarioPacks.length) {
    box.innerHTML = '<p class="meta">No scenario packs available.</p>';
    return;
  }
  box.innerHTML = scenarioPacks.map((p) => `
    <div class="card packcard">
      <h3>${esc(p.name)}</h3>
      <p>${esc(p.description || "")}</p>
      <p class="meta">${p.scenario_count} scenario(s): ${(p.scenarios || []).map(esc).join(", ")}</p>
      <button type="button" data-pack="${esc(p.id)}" class="primary scenario-run-btn">Run</button>
    </div>`).join("");
  box.querySelectorAll(".scenario-run-btn").forEach((btn) => {
    btn.addEventListener("click", () => runScenarioPack(btn.dataset.pack, btn));
  });
}

async function runScenarioPack(packId, btn) {
  const target = document.getElementById("scenario-target").value.trim();
  const bearer = document.getElementById("scenario-bearer").value;
  const chaos = document.getElementById("scenario-chaos").checked;
  const chaosSeed = Number(document.getElementById("scenario-chaos-seed").value) || 0;
  const allowRemote = document.getElementById("scenario-allow-remote").checked;
  const results = document.getElementById("scenario-results");
  if (!target) {
    results.innerHTML = errorCard("Enter a target MCP endpoint URL first.");
    return;
  }
  const runId = ++scenarioRunSeq;
  const pack = scenarioPacks.find((p) => p.id === packId);
  btn.disabled = true;
  const origText = btn.textContent;
  btn.textContent = "Running…";
  results.innerHTML = `
    <div class="card"><h3>${esc(pack ? pack.name : packId)}</h3>
    <p class="meta">Target: <code>${esc(target)}</code></p>
    <div id="scenario-live-${runId}"></div></div>`;

  const live = () => document.getElementById("scenario-live-" + runId);
  const scenarioDivs = {};
  try {
    await readSSE("/api/scenarios/run", { pack: packId, target_url: target, bearer_token: bearer, chaos: chaos, chaos_seed: chaosSeed, allow_remote: allowRemote }, (event, data) => {
      if (runId !== scenarioRunSeq) return; // superseded by a newer run
      const el = live();
      if (!el) return;
      if (event === "scenario_started") {
        const d = document.createElement("div");
        d.className = "card scncard";
        d.innerHTML = `<h4>${esc(data.scenario)}</h4><div class="scnsteps"></div>`;
        el.appendChild(d);
        scenarioDivs[data.scenario] = d.querySelector(".scnsteps");
      } else if (event === "step_started") {
        const steps = scenarioDivs[data.scenario];
        if (steps) {
          const row = document.createElement("div");
          row.className = "steprow running";
          row.id = `scn-${runId}-step-${data.step_index}`;
          row.innerHTML = `<span class="badge skip">RUN</span>
            <span class="check-name">${esc(data.step)}</span>
            <span class="check-dur">…</span>`;
          steps.appendChild(row);
        }
      } else if (event === "step_finished") {
        const row = document.getElementById(`scn-${runId}-step-${data.step_index}`);
        if (row && data.step_result) {
          const sr = data.step_result;
          row.className = "steprow";
          const badge = sr.passed ? '<span class="badge pass">PASS</span>' : '<span class="badge fail">FAIL</span>';
          const asserts = (sr.assertions || []).map((a) =>
            `<div class="assertrow"><span class="badge ${a.passed ? "pass" : "fail"}">${a.passed ? "PASS" : "FAIL"}</span>
             <code>${esc(a.kind)}</code>${a.detail ? ` <span class="meta">${esc(a.detail)}</span>` : ""}</div>`).join("");
          const chaosMark = sr.chaos
            ? `<span class="badge chaos" title="${esc(sr.chaos.detail)}">CHAOS: ${esc(sr.chaos.injected)}</span>` : "";
          row.innerHTML = `${badge}${chaosMark}
            <span class="check-name">${esc(sr.name)} <span class="meta">(${esc(sr.tool)})</span></span>
            <span class="check-dur">${Number(sr.latency_ms).toFixed(0)} ms</span>
            <div class="step-body"><details><summary>assertions (${(sr.assertions || []).length})</summary>
              ${asserts || '<p class="meta">No assertions.</p>'}
              ${sr.error ? `<p class="step-error">${esc(sr.error)}</p>` : ""}
            </details></div>`;
        }
      } else if (event === "scenario_finished") {
        // Step rows already reflect the outcome; nothing extra needed.
      } else if (event === "report") {
        const rep = data;
        lastReports.scenarios = rep;
        const s = rep.summary || { total: 0, passed: 0, failed: 0 };
        const cls = s.failed > 0 ? "failed" : "passed";
        const banner = document.createElement("div");
        banner.className = "card summary " + cls;
        banner.innerHTML = `<h3>Scenario report</h3>
          <p>Pack: <code>${esc(rep.pack)}</code> — target <code>${esc(rep.target_url)}</code></p>
          <p><strong>${s.passed}</strong> passed, <strong>${s.failed}</strong> failed, of ${s.total} scenarios.</p>
          ${rep.chaos ? `<p class="meta">Chaos: seed <code>${rep.chaos.seed}</code>, ` +
            `${rep.chaos.injected_faults} dropped calls, ${rep.chaos.injected_spikes} latency spikes, ` +
            `${rep.chaos.steps_survived}/${rep.chaos.total_steps} injected steps survived.</p>` : ""}
          <p class="meta">Calls were recorded to the Inspect screen's session store.</p>`;
        results.prepend(banner);
      } else if (event === "error") {
        results.prepend(errorCard("scenario run failed: " + (data && data.error ? data.error : "unknown error")));
      }
    });
  } catch (err) {
    results.prepend(errorCard("scenario run failed: " + err.message));
  } finally {
    btn.disabled = false;
    btn.textContent = origText;
  }
}

// ---------------------------------------------------------------------------
// Load screen: form -> live charts + results with threshold verdicts.
// ---------------------------------------------------------------------------

let loadPresets = [];
let loadSeries = null; // {lat: [...], rps: [...], t0}

function loadSeriesInit() {
  loadSeries = { lat: [], rps: [], t0: Date.now() };
}

async function refreshLoad() {
  // Fill the preset select and the scenario-pack select (once each).
  const presetSel = document.getElementById("load-preset");
  if (presetSel && presetSel.options.length <= 1) {
    try {
      const resp = await fetch("/api/load/presets");
      const body = await resp.json();
      loadPresets = body.presets || [];
      for (const p of loadPresets) {
        const opt = document.createElement("option");
        opt.value = p.id;
        opt.textContent = p.name + " — " + p.description;
        presetSel.appendChild(opt);
      }
    } catch (err) {
      console.error("load presets failed", err);
    }
  }
  const packSel = document.getElementById("load-pack");
  if (packSel && packSel.options.length === 0) {
    try {
      const resp = await fetch("/api/scenarios");
      const body = await resp.json();
      for (const p of body.packs || []) {
        const opt = document.createElement("option");
        opt.value = p.id;
        opt.textContent = p.name;
        packSel.appendChild(opt);
      }
    } catch (err) {
      console.error("scenario packs for load failed", err);
    }
  }
}

const loadForm = document.getElementById("load-form");
if (loadForm) {
  loadForm.addEventListener("submit", runLoadTest);
  document.getElementById("load-mode").addEventListener("change", (e) => {
    const isScenario = e.target.value === "scenario";
    document.getElementById("load-tool-wrap").classList.toggle("hidden", isScenario);
    document.getElementById("load-args-wrap").classList.toggle("hidden", isScenario);
    document.getElementById("load-pack-wrap").classList.toggle("hidden", !isScenario);
    document.getElementById("load-scenario-name-wrap").classList.toggle("hidden", !isScenario);
  });
  document.getElementById("load-preset").addEventListener("change", (e) => {
    const p = loadPresets.find((x) => x.id === e.target.value);
    if (p) applyLoadPreset(p);
  });
}

// applyLoadPreset fills the load form from a preset's YAML (flat keys only).
function applyLoadPreset(preset) {
  const get = (key) => {
    const m = preset.yaml.match(new RegExp("^" + key + ":\\s*(.+)$", "m"));
    return m ? m[1].trim() : "";
  };
  const set = (id, v) => { if (v) document.getElementById(id).value = v; };
  set("load-target", get("target_url"));
  const tool = get("tool");
  if (tool) {
    document.getElementById("load-mode").value = "tool";
    document.getElementById("load-mode").dispatchEvent(new Event("change"));
    set("load-tool", tool);
  }
  set("load-concurrency", get("concurrency"));
  set("load-ramp", get("ramp_up"));
  set("load-duration", get("duration"));
  set("load-iterations", get("iterations"));
  const th = preset.yaml.match(/^thresholds:\n((?:  .+\n?)+)/m);
  if (th) {
    const tv = (k) => {
      const m = th[1].match(new RegExp("^  " + k + ":\\s*(.+)$", "m"));
      return m ? m[1].trim() : "";
    };
    set("load-p99", tv("p99_ms_lt"));
    set("load-err", tv("error_rate_lt"));
    set("load-timeout-rate", tv("timeout_rate_lt"));
  }
}

function loadFormConfig() {
  const val = (id) => document.getElementById(id).value.trim();
  const isScenario = document.getElementById("load-mode").value === "scenario";
  let args = {};
  if (!isScenario) {
    const raw = val("load-args") || "{}";
    try {
      args = JSON.parse(raw);
    } catch (err) {
      throw new Error("arguments is not valid JSON: " + err.message);
    }
  }
  const cfg = {
    target_url: val("load-target"),
    concurrency: parseInt(val("load-concurrency") || "1", 10),
    ramp_up: val("load-ramp"),
    duration: val("load-duration"),
    allow_remote: document.getElementById("load-allow-remote").checked,
    thresholds: {},
  };
  const iters = val("load-iterations");
  if (iters) cfg.iterations = parseInt(iters, 10);
  if (isScenario) {
    cfg.scenario = document.getElementById("load-pack").value;
    const sn = val("load-scenario-name");
    if (sn) cfg.scenario_name = sn;
  } else {
    cfg.tool = val("load-tool");
    cfg.arguments = args;
  }
  const p99 = val("load-p99");
  if (p99 !== "") cfg.thresholds.p99_ms_lt = parseFloat(p99);
  const er = val("load-err");
  if (er !== "") cfg.thresholds.error_rate_lt = parseFloat(er);
  const tr = val("load-timeout-rate");
  if (tr !== "") cfg.thresholds.timeout_rate_lt = parseFloat(tr);
  return cfg;
}

async function runLoadTest(event) {
  event.preventDefault();
  const btn = document.getElementById("load-run-btn");
  const liveCard = document.getElementById("load-live-card");
  const live = document.getElementById("load-live");
  const results = document.getElementById("load-results");
  let cfg;
  try {
    cfg = loadFormConfig();
  } catch (err) {
    results.innerHTML = errorCard(err.message);
    return;
  }
  btn.disabled = true;
  btn.textContent = "Running…";
  results.innerHTML = "";
  liveCard.classList.remove("hidden");
  loadSeriesInit();
  live.innerHTML = '<p class="meta">Starting…</p>';
  drawCharts();

  try {
    await readSSE("/api/load/run", cfg, (evName, data) => {
      if (evName === "progress") {
        const t = (data.elapsed_ms || 0) / 1000;
        loadSeries.lat.push({ t, p50: data.p50_ms, p99: data.p99_ms });
        loadSeries.rps.push({ t, rps: data.throughput_rps });
        live.innerHTML = `
          <div class="loadstats">
            <span><strong>${data.completed}</strong> req</span>
            <span><strong>${Number(data.throughput_rps).toFixed(1)}</strong> rps</span>
            <span>p50 <strong>${Number(data.p50_ms).toFixed(0)}</strong> ms</span>
            <span>p99 <strong>${Number(data.p99_ms).toFixed(0)}</strong> ms</span>
            <span>errors <strong>${data.errors}</strong></span>
            <span>timeouts <strong>${data.timeouts}</strong></span>
          </div>`;
        drawCharts();
      } else if (evName === "result") {
        lastReports.load = data.result;
        results.innerHTML = renderLoadResult(data.result, data.id);
      } else if (evName === "error") {
        results.innerHTML = errorCard("load test failed: " + (data && data.error ? data.error : "unknown error"));
      }
    });
  } catch (err) {
    results.innerHTML = errorCard("load test failed: " + err.message);
  } finally {
    btn.disabled = false;
    btn.textContent = "Run load test";
  }
}

function renderLoadResult(res, id) {
  const cls = res.passed ? "passed" : "failed";
  const verdict = (tr) => `
    <div class="check"><span class="badge ${tr.passed ? "pass" : "fail"}">${tr.passed ? "PASS" : "FAIL"}</span>
    <span class="check-name">${esc(tr.name)} &lt; ${esc(String(tr.limit))}</span>
    <span class="check-dur">actual ${esc(String(round4(tr.actual)))}</span>
    ${tr.detail ? `<details><summary>detail</summary><p>${esc(tr.detail)}</p></details>` : ""}</div>`;
  return `
    <div class="card summary ${cls}"><h3>Load result</h3>
      <p>Run id: <code>${esc(id)}</code> — <strong>${res.passed ? "PASSED" : "FAILED"}</strong></p>
      <p><strong>${res.total_requests}</strong> requests in ${(Number(res.duration_ms) / 1000).toFixed(1)}s
         (${Number(res.throughput_rps).toFixed(1)} rps)</p>
    </div>
    <div class="card"><h3>Latency &amp; rates</h3>
      <p>p50 <strong>${Number(res.p50_ms).toFixed(1)}</strong> ms ·
         p95 <strong>${Number(res.p95_ms).toFixed(1)}</strong> ms ·
         p99 <strong>${Number(res.p99_ms).toFixed(1)}</strong> ms</p>
      <p>errors <strong>${res.errors}</strong> (${(Number(res.error_rate) * 100).toFixed(2)}%) ·
         timeouts <strong>${res.timeouts}</strong> (${(Number(res.timeout_rate) * 100).toFixed(2)}%)</p>
    </div>
    <div class="card"><h3>Thresholds</h3>
      ${(res.thresholds || []).map(verdict).join("") || '<p class="meta">No thresholds configured.</p>'}
      <p class="meta"><a href="/api/load/results/${esc(id)}" target="_blank" rel="noopener">raw JSON</a></p>
    </div>`;
}

function round4(n) {
  return Math.round(Number(n) * 10000) / 10000;
}

// drawCharts renders the live latency and throughput series on canvas.
function drawCharts() {
  drawLineChart("load-chart-lat", loadSeries ? loadSeries.lat : [],
    [(p) => p.p50, (p) => p.p99], ["#1a73e8", "#c5221f"], ["p50", "p99"], "ms");
  drawLineChart("load-chart-rps", loadSeries ? loadSeries.rps : [],
    [(p) => p.rps], ["#137333"], ["rps"], "rps");
}

function drawLineChart(canvasId, points, getters, colors, labels, unit) {
  const cv = document.getElementById(canvasId);
  if (!cv) return;
  const dark = window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches;
  const ink = dark ? "#e9edf1" : "#333";
  const faint = dark ? "#9aa4b0" : "#666";
  const grid = dark ? "#2a323c" : "#ddd";
  const ctx = cv.getContext("2d");
  const W = cv.width, H = cv.height;
  ctx.clearRect(0, 0, W, H);
  ctx.fillStyle = dark ? "#161b21" : "#fff";
  ctx.fillRect(0, 0, W, H);
  const pad = { l: 44, r: 8, t: 8, b: 20 };
  const iw = W - pad.l - pad.r, ih = H - pad.t - pad.b;
  let maxV = 1, maxT = 1;
  for (const p of points) {
    if (p.t > maxT) maxT = p.t;
    for (const g of getters) {
      const v = g(p) || 0;
      if (v > maxV) maxV = v;
    }
  }
  maxV *= 1.1;
  // axes
  ctx.strokeStyle = grid;
  ctx.beginPath();
  ctx.moveTo(pad.l, pad.t);
  ctx.lineTo(pad.l, pad.t + ih);
  ctx.lineTo(pad.l + iw, pad.t + ih);
  ctx.stroke();
  // y labels
  ctx.fillStyle = faint;
  ctx.font = "10px sans-serif";
  ctx.fillText("0", 6, pad.t + ih);
  ctx.fillText(fmtNum(maxV) + " " + unit, 6, pad.t + 10);
  ctx.fillText(fmtNum(maxT) + "s", pad.l + iw - 30, H - 6);
  // series
  getters.forEach((g, gi) => {
    ctx.strokeStyle = colors[gi % colors.length];
    ctx.lineWidth = 1.5;
    ctx.beginPath();
    let started = false;
    for (const p of points) {
      const x = pad.l + (p.t / maxT) * iw;
      const y = pad.t + ih - ((g(p) || 0) / maxV) * ih;
      if (!started) { ctx.moveTo(x, y); started = true; }
      else ctx.lineTo(x, y);
    }
    ctx.stroke();
  });
  // legend
  ctx.font = "10px sans-serif";
  labels.forEach((lb, i) => {
    ctx.fillStyle = colors[i % colors.length];
    ctx.fillRect(pad.l + i * 52, H - 12, 10, 8);
    ctx.fillStyle = ink;
    ctx.fillText(lb, pad.l + i * 52 + 13, H - 4);
  });
}

function fmtNum(n) {
  if (n >= 1000) return (n / 1000).toFixed(1) + "k";
  if (n >= 100) return n.toFixed(0);
  if (n >= 1) return n.toFixed(1);
  return n.toFixed(2);
}

// ---------------------------------------------------------------------------
// Signing debugger, lifecycle check, protocol fuzz, webhooks,
// snapshots, reports.
// ---------------------------------------------------------------------------

// --- Signing debugger ------------------------------------------------------

const signdebugForm = document.getElementById("signdebug-form");
if (signdebugForm) {
  signdebugForm.addEventListener("submit", runSigndebug);
}

function parseHeaderLines(text) {
  const out = {};
  for (const line of String(text || "").split("\n")) {
    const t = line.trim();
    if (!t) continue;
    const i = t.indexOf(":");
    if (i < 0) continue;
    out[t.slice(0, i).trim()] = t.slice(i + 1).trim();
  }
  return out;
}

async function runSigndebug(event) {
  event.preventDefault();
  const btn = document.getElementById("sd-verify-btn");
  const results = document.getElementById("signdebug-results");
  btn.disabled = true;
  btn.textContent = "Verifying…";
  results.innerHTML = '<p class="meta">Reconstructing signature base…</p>';
  const payload = {
    request: {
      method: document.getElementById("sd-method").value.trim() || "POST",
      url: document.getElementById("sd-url").value.trim(),
      headers: parseHeaderLines(document.getElementById("sd-headers").value),
      body: document.getElementById("sd-body").value,
    },
    key_pem: document.getElementById("sd-key").value,
    expected_base: document.getElementById("sd-expected").value,
  };
  try {
    const rep = await fetchJSON("/api/signdebug/verify", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    });
    results.innerHTML = renderSigndebug(rep);
  } catch (err) {
    results.innerHTML = errorCard("verification failed: " + err.message);
  } finally {
    // Never keep key material in the page longer than the single check.
    document.getElementById("sd-key").value = "";
    btn.disabled = false;
    btn.textContent = "Verify signature";
  }
}

function renderSigndebug(rep) {
  const valid = rep.verdict === "valid";
  const cls = valid ? "passed" : "failed";
  const checks = (rep.checks || []).map((c) => `
    <div class="check">
      <span class="badge ${c.ok ? "pass" : "fail"}">${c.ok ? "OK" : "MISMATCH"}</span>
      <span class="check-name">${esc(c.name)}</span>
      <details><summary>detail</summary>
        <p>${esc(c.detail || "")}</p>
        ${c.expected ? `<p>expected: <code>${esc(c.expected)}</code></p>` : ""}
        ${c.actual ? `<p>actual: <code>${esc(c.actual)}</code></p>` : ""}
      </details>
    </div>`).join("");
  const components = (rep.components || []).map((c) => `
    <div class="check"><span class="check-name"><code>${esc(c.name)}</code></span>
      <span class="meta">${esc(c.value || "")}</span></div>`).join("");
  const steps = (rep.steps || []).map((s) => `<li>${esc(s)}</li>`).join("");
  return `
    <div class="card summary ${cls}">
      <h3>Signature ${valid ? "VALID" : "INVALID"}</h3>
      <p>${esc(rep.summary || "")}</p>
      <p class="meta">label <code>${esc(rep.label || "")}</code> ·
         algorithm <code>${esc(rep.algorithm || "")}</code>
         ${rep.key_id ? ` · key id <code>${esc(rep.key_id)}</code>` : ""}</p>
    </div>
    <div class="card"><h3>Checks</h3>${checks || '<p class="meta">No checks.</p>'}</div>
    <div class="card"><h3>Covered components</h3>${components || '<p class="meta">None.</p>'}</div>
    <div class="card"><h3>Reconstructed signature base</h3>
      <pre class="prewrap">${esc(rep.signature_base || "")}</pre></div>
    <div class="card"><h3>Steps</h3><ol class="meta">${steps}</ol></div>`;
}

// --- Lifecycle check -------------------------------------------------------

const lifecycleBtn = document.getElementById("lifecycle-run-btn");
if (lifecycleBtn) {
  lifecycleBtn.addEventListener("click", runLifecycleCheck);
}

async function runLifecycleCheck() {
  const target = document.getElementById("scenario-target").value.trim();
  const bearer = document.getElementById("scenario-bearer").value;
  const results = document.getElementById("lifecycle-results");
  if (!target) {
    results.innerHTML = errorCard("Enter a target MCP endpoint URL first (Scenarios screen, target field).");
    return;
  }
  lifecycleBtn.disabled = true;
  lifecycleBtn.textContent = "Running…";
  results.innerHTML = '<p class="meta">Walking the media-buy lifecycle…</p>';
  try {
    const rep = await fetchJSON("/api/lifecycle/run", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ target_url: target, bearer_token: bearer }),
    });
    lastReports.lifecycle = rep;
    const passed = (rep.checks || []).filter((c) => c.status === "pass").length;
    const total = (rep.checks || []).length;
    const cls = passed === total && total > 0 ? "passed" : "failed";
    const rows = (rep.checks || []).map((c) => `
      <div class="check">
        <span class="badge ${esc(c.status)}">${esc(c.status).toUpperCase()}</span>
        <span class="check-name">${esc(c.name)}</span>
        <span class="check-dur">${Number(c.duration_ms).toFixed(0)} ms</span>
        <details><summary>detail</summary><p>${esc(c.detail)}</p></details>
      </div>`).join("");
    results.innerHTML = `
      <div class="card summary ${cls}"><h3>Lifecycle report</h3>
        <p>Target: <code>${esc(rep.target_url)}</code></p>
        <p><strong>${passed}</strong> passed of ${total} checks.
           ${rep.media_buy_id ? `Media buy: <code>${esc(rep.media_buy_id)}</code>` : ""}</p>
      </div>
      <div class="card"><h3>Checks</h3>${rows}</div>`;
  } catch (err) {
    results.innerHTML = errorCard("lifecycle check failed: " + err.message);
  } finally {
    lifecycleBtn.disabled = false;
    lifecycleBtn.textContent = "Run lifecycle check";
  }
}

// --- Protocol fuzz ---------------------------------------------------------

const fuzzForm = document.getElementById("fuzz-form");
if (fuzzForm) {
  fuzzForm.addEventListener("submit", runFuzz);
}

async function runFuzz(event) {
  event.preventDefault();
  const btn = document.getElementById("fuzz-run-btn");
  const results = document.getElementById("fuzz-results");
  btn.disabled = true;
  btn.textContent = "Fuzzing…";
  results.innerHTML = '<p class="meta">Sending malformed payloads…</p>';
  const payload = {
    target_url: document.getElementById("fuzz-target").value.trim(),
    iterations: Number(document.getElementById("fuzz-iterations").value) || 200,
    seed: Number(document.getElementById("fuzz-seed").value) || 0,
    allow_remote: document.getElementById("fuzz-allow-remote").checked,
  };
  try {
    const rep = await fetchJSON("/api/fuzz/run", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    });
    lastReports.fuzz = rep;
    const findings = rep.findings || [];
    const cls = findings.length === 0 ? "passed" : "failed";
    const rows = findings.map((f) => `
      <div class="check">
        <span class="badge fail">${esc(f.kind).toUpperCase()}</span>
        <span class="check-name">iteration ${esc(String(f.iteration))}</span>
        <details><summary>detail</summary>
          <p>${esc(f.detail || "")}</p>
          <p class="meta">payload</p><pre class="prewrap">${esc(f.payload || "")}</pre>
        </details>
      </div>`).join("");
    results.innerHTML = `
      <div class="card summary ${cls}"><h3>Fuzz report</h3>
        <p>Target: <code>${esc(rep.target_url)}</code> — ${rep.iterations} iterations, seed <code>${rep.seed}</code></p>
        <p><strong>${findings.length}</strong> finding(s).</p>
      </div>
      ${findings.length ? `<div class="card"><h3>Findings</h3>${rows}</div>` : ""}`;
  } catch (err) {
    results.innerHTML = errorCard("fuzz run failed: " + err.message);
  } finally {
    btn.disabled = false;
    btn.textContent = "Run fuzzer";
  }
}

// --- Webhooks --------------------------------------------------------------

let webhookPollTimer = null;

const webhookForm = document.getElementById("webhook-form");
if (webhookForm) {
  webhookForm.addEventListener("submit", startWebhookListener);
}
const webhookStopBtn = document.getElementById("webhook-stop-btn");
if (webhookStopBtn) {
  webhookStopBtn.addEventListener("click", async () => {
    await fetchJSON("/api/webhooks/stop", { method: "POST" }).catch(() => ({}));
    stopWebhookPoll();
    document.getElementById("webhook-url").textContent = "";
    document.getElementById("webhook-deliveries").innerHTML = "";
  });
}
const webhookClearBtn = document.getElementById("webhook-clear-btn");
if (webhookClearBtn) {
  webhookClearBtn.addEventListener("click", async () => {
    await fetchJSON("/api/webhooks/clear", { method: "POST" }).catch(() => ({}));
    refreshWebhookDeliveries().catch(() => {});
  });
}

async function startWebhookListener(event) {
  event.preventDefault();
  const port = Number(document.getElementById("webhook-port").value) || 0;
  const urlEl = document.getElementById("webhook-url");
  try {
    const res = await fetchJSON("/api/webhooks/listen", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ port: port }),
    });
    urlEl.innerHTML = `Listening — point the seller's webhooks at <code>${esc(res.url)}</code>`;
    startWebhookPoll();
    refreshWebhookDeliveries().catch(() => {});
  } catch (err) {
    urlEl.textContent = "Could not start listener: " + err.message;
  }
}

function startWebhookPoll() {
  stopWebhookPoll();
  webhookPollTimer = setInterval(() => {
    if (document.getElementById("screen-webhooks").classList.contains("hidden")) return;
    refreshWebhookDeliveries().catch(() => {});
  }, 2000);
}

function stopWebhookPoll() {
  if (webhookPollTimer) {
    clearInterval(webhookPollTimer);
    webhookPollTimer = null;
  }
}

async function refreshWebhooks() {
  await refreshWebhookDeliveries();
  startWebhookPoll();
}

async function refreshWebhookDeliveries() {
  const box = document.getElementById("webhook-deliveries");
  const res = await fetchJSON("/api/webhooks/deliveries", {});
  if (res.url) {
    document.getElementById("webhook-url").innerHTML =
      `Listening — point the seller's webhooks at <code>${esc(res.url)}</code>`;
    startWebhookPoll();
  }
  const deliveries = res.deliveries || [];
  if (!deliveries.length) {
    box.innerHTML = '<div class="card"><p class="meta">No deliveries captured yet.</p></div>';
    return;
  }
  box.innerHTML = `<div class="card"><h3>Deliveries (${deliveries.length})</h3>` + deliveries.map((d) => {
    const headers = Object.entries(d.headers || {}).map(([k, v]) =>
      `<div class="check"><span class="check-name"><code>${esc(k)}</code></span><span class="meta">${esc(v)}</span></div>`).join("");
    const body = d.body ? `<pre class="prewrap">${esc(typeof d.body === "string" ? d.body : JSON.stringify(d.body, null, 2))}</pre>`
      : (d.raw_body ? `<pre class="prewrap">${esc(d.raw_body)}</pre>` : '<p class="meta">empty body</p>');
    return `<div class="check">
        <span class="badge pass">#${d.id}</span>
        <span class="check-name"><code>${esc(d.method)} ${esc(d.path)}</code></span>
        <span class="check-dur">${esc(fmtTime(d.received_at))}</span>
        <details><summary>headers &amp; body</summary>${headers}${body}
        ${d.truncated ? '<p class="meta">body truncated</p>' : ""}</details>
      </div>`;
  }).join("") + "</div>";
}

// --- Snapshots -------------------------------------------------------------

const snapshotSaveForm = document.getElementById("snapshot-save-form");
if (snapshotSaveForm) {
  snapshotSaveForm.addEventListener("submit", saveSnapshot);
}
const snapshotDiffForm = document.getElementById("snapshot-diff-form");
if (snapshotDiffForm) {
  snapshotDiffForm.addEventListener("submit", diffSnapshots);
}

async function refreshSnapshots() {
  const box = document.getElementById("snapshot-list");
  let snaps = [];
  try {
    const res = await fetchJSON("/api/snapshots", {});
    snaps = res.snapshots || [];
  } catch (err) {
    box.innerHTML = errorCard("could not load snapshots: " + err.message);
    return;
  }
  if (!snaps.length) {
    box.innerHTML = '<p class="meta">No snapshots saved yet.</p>';
  } else {
    box.innerHTML = snaps.map((s) => `
      <div class="check">
        <span class="badge pass">${esc(s.kind)}</span>
        <span class="check-name"><code>${esc(s.name)}</code></span>
        <span class="check-dur">${esc(fmtTime(s.saved_at))}</span>
        <span class="meta">${esc(s.summary || "")}</span>
        <button type="button" data-del="${esc(s.name)}" class="snap-del-btn">delete</button>
      </div>`).join("");
    box.querySelectorAll(".snap-del-btn").forEach((b) => {
      b.addEventListener("click", async () => {
        await fetchJSON("/api/snapshots/delete", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ name: b.dataset.del }),
        }).catch((e) => alert("delete failed: " + e.message));
        refreshSnapshots().catch(() => {});
      });
    });
  }
  const opts = snaps.map((s) => `<option value="${esc(s.name)}">${esc(s.name)} (${esc(s.kind)})</option>`).join("");
  document.getElementById("snap-before").innerHTML = opts;
  document.getElementById("snap-after").innerHTML = opts;
}

async function saveSnapshot(event) {
  event.preventDefault();
  const kind = document.getElementById("snap-kind").value;
  const name = document.getElementById("snap-name").value.trim();
  let reportText = document.getElementById("snap-report").value.trim();
  if (!reportText && lastReports[kind]) {
    reportText = JSON.stringify(lastReports[kind]);
  }
  if (!name) {
    alert("Give the snapshot a name.");
    return;
  }
  let report;
  try {
    report = JSON.parse(reportText);
  } catch (err) {
    alert("Report is not valid JSON: " + err.message);
    return;
  }
  try {
    await fetchJSON("/api/snapshots/save", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ kind: kind, name: name, report: report }),
    });
    document.getElementById("snap-name").value = "";
    document.getElementById("snap-report").value = "";
    refreshSnapshots().catch(() => {});
  } catch (err) {
    alert("save failed: " + err.message);
  }
}

async function diffSnapshots(event) {
  event.preventDefault();
  const box = document.getElementById("snapshot-diff-results");
  const before = document.getElementById("snap-before").value;
  const after = document.getElementById("snap-after").value;
  if (!before || !after) {
    box.innerHTML = errorCard("Pick two snapshots to diff.");
    return;
  }
  box.innerHTML = '<p class="meta">Diffing…</p>';
  try {
    const r = await fetch(`/api/snapshots/diff?before=${encodeURIComponent(before)}&after=${encodeURIComponent(after)}`);
    const d = await r.json();
    if (!r.ok) throw new Error(d.error || "diff failed");
    const list = (arr) => (arr && arr.length ? arr.map((x) => `<li><code>${esc(x)}</code></li>`).join("") : "<li>none</li>");
    box.innerHTML = `
      <div class="card summary ${d.changed || (d.new && d.new.length) || (d.removed && d.removed.length) ? "failed" : "passed"}">
        <h3>Diff: <code>${esc(d.before)}</code> → <code>${esc(d.after)}</code></h3>
        <p><strong>${d.changed}</strong> changed check(s)</p>
      </div>
      <div class="card cols">
        <div class="panel"><h4>Pass → fail</h4><ul>${list(d.pass_to_fail)}</ul></div>
        <div class="panel"><h4>Fail → pass</h4><ul>${list(d.fail_to_pass)}</ul></div>
        <div class="panel"><h4>New</h4><ul>${list(d.new)}</ul></div>
        <div class="panel"><h4>Removed</h4><ul>${list(d.removed)}</ul></div>
      </div>`;
  } catch (err) {
    box.innerHTML = errorCard("diff failed: " + err.message);
  }
}

// --- Reports ---------------------------------------------------------------

const reportKinds = ["conformance", "scenarios", "load", "lifecycle", "fuzz"];

function refreshReports() {
  const box = document.getElementById("report-inputs");
  box.innerHTML = reportKinds.map((k) => `
    <div class="panel">
      <h4>${k}</h4>
      <label class="checkline"><input type="checkbox" id="rep-use-${k}" ${lastReports[k] ? "checked" : ""}>
        use last ${k} run ${lastReports[k] ? "(available)" : "(none yet)"}</label>
      <label>Paste report JSON (overrides)
        <textarea id="rep-json-${k}" rows="3" placeholder='{"checks": […]}'></textarea>
      </label>
    </div>`).join("");
  fetchJSON("/api/snapshots", {}).then((res) => {
    const snaps = res.snapshots || [];
    const opts = '<option value="">—</option>' + snaps.map((s) =>
      `<option value="${esc(s.name)}">${esc(s.name)} (${esc(s.kind)})</option>`).join("");
    document.getElementById("report-diff-before").innerHTML = opts;
    document.getElementById("report-diff-after").innerHTML = opts;
  }).catch(() => {});
}

const reportHtmlBtn = document.getElementById("report-html-btn");
if (reportHtmlBtn) reportHtmlBtn.addEventListener("click", () => buildReport("html"));
const reportPdfBtn = document.getElementById("report-pdf-btn");
if (reportPdfBtn) reportPdfBtn.addEventListener("click", () => buildReport("pdf"));

function collectReportInputs() {
  const body = {
    title: document.getElementById("report-title").value.trim(),
    diff_before: document.getElementById("report-diff-before").value,
    diff_after: document.getElementById("report-diff-after").value,
  };
  for (const k of reportKinds) {
    const pasted = document.getElementById("rep-json-" + k).value.trim();
    if (pasted) {
      try {
        body[k] = JSON.parse(pasted);
      } catch (err) {
        throw new Error(k + " JSON is invalid: " + err.message);
      }
    } else if (document.getElementById("rep-use-" + k).checked && lastReports[k]) {
      body[k] = lastReports[k];
    }
  }
  return body;
}

async function buildReport(format) {
  const status = document.getElementById("report-status");
  status.innerHTML = '<p class="meta">Building evidence pack…</p>';
  let body;
  try {
    body = collectReportInputs();
  } catch (err) {
    status.innerHTML = errorCard(err.message);
    return;
  }
  body.format = format;
  try {
    const resp = await fetch("/api/reports/build", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    if (!resp.ok) {
      let msg = "build failed";
      try {
        const errBody = await resp.json();
        if (errBody && errBody.error) msg = errBody.error;
      } catch (_) {}
      throw new Error(msg);
    }
    const blob = await resp.blob();
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = "adcp-test-evidence." + format;
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 5000);
    status.innerHTML = '<p class="meta">Download started.</p>';
  } catch (err) {
    status.innerHTML = errorCard("report build failed: " + err.message);
  }
}
