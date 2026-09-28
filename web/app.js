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
