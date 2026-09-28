// adcp-test web UI — vanilla JS, no dependencies.

// Screen navigation: buttons with data-screen switch the visible section.
// Disabled buttons are placeholders for later milestones.
document.querySelectorAll("nav button[data-screen]").forEach((btn) => {
  btn.addEventListener("click", () => showScreen(btn.dataset.screen));
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
}

function esc(s) {
  return String(s == null ? "" : s).replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  })[c]);
}

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
