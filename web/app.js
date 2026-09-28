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
