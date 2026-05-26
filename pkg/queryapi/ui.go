package queryapi

// uiHTML is the complete single-page application served at GET /.
// It is a self-contained HTML file with no external dependencies so it works
// in air-gapped clusters without internet access.
//
// The page has four tabs:
//   - Explore     — natural-language Q&A over the decision audit log
//   - Autoscalers — list / edit / delete AgenticAutoscaler CRs
//   - Onboard     — a form that creates a CR for any Deployment
//   - Load        — drive the load generator and watch replicas react (hidden
//     unless a load generator is configured)
//
// The JS uses string concatenation (never template literals) so the source can
// live inside a Go raw-string literal delimited by backticks.
var uiHTML = []byte(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Agentic Autoscaler — Console</title>
<style>
  *, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }
  :root {
    --bg:#0f1117; --surface:#1a1d27; --surface2:#21253208; --border:#2a2d3e;
    --accent:#6c8fff; --green:#34d399; --red:#f87171; --yellow:#fbbf24;
    --blue:#60a5fa; --muted:#8b92a5; --text:#e2e8f0; --radius:8px;
    --mono:'JetBrains Mono','Fira Code',monospace;
  }
  html, body { height:100%; background:var(--bg); color:var(--text);
    font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif; font-size:14px; }

  .header { display:flex; align-items:center; gap:14px; height:56px; padding:0 20px;
    border-bottom:1px solid var(--border); background:var(--surface); }
  .header h1 { font-size:15px; font-weight:600; }
  .dot { width:8px; height:8px; border-radius:50%; background:var(--green); animation:pulse 2s infinite; }
  @keyframes pulse { 0%,100%{opacity:1;} 50%{opacity:.4;} }
  .tabs { display:flex; gap:4px; margin-left:18px; }
  .tab { background:none; border:none; color:var(--muted); padding:8px 14px; cursor:pointer;
    font-size:13px; border-radius:var(--radius); }
  .tab:hover { color:var(--text); }
  .tab.active { color:var(--text); background:var(--bg); border:1px solid var(--border); }
  .spacer { flex:1; }
  .mode-badge { font-size:11px; padding:3px 8px; border-radius:4px; font-family:var(--mono); }
  .mode-badge.ro { background:rgba(139,146,165,.15); color:var(--muted); }
  .mode-badge.rw { background:rgba(52,211,153,.15); color:var(--green); }

  .view { display:none; height:calc(100vh - 56px); overflow:hidden; }
  .view.active { display:block; }

  /* Explore (chat + timeline) */
  .explore { display:grid; grid-template-columns:1fr 380px; height:100%; }
  .chat-panel { display:flex; flex-direction:column; border-right:1px solid var(--border); overflow:hidden; }
  .messages { flex:1; overflow-y:auto; padding:20px; display:flex; flex-direction:column; gap:16px; }
  .msg { max-width:88%; display:flex; flex-direction:column; gap:4px; }
  .msg.user { align-self:flex-end; }
  .msg.assistant { align-self:flex-start; }
  .msg-bubble { padding:10px 14px; border-radius:var(--radius); line-height:1.5; white-space:pre-wrap; word-break:break-word; }
  .msg.user .msg-bubble { background:var(--accent); color:#fff; }
  .msg.assistant .msg-bubble { background:var(--surface); border:1px solid var(--border); }
  .msg-meta { font-size:11px; color:var(--muted); padding:0 4px; }
  .typing { display:flex; gap:4px; padding:10px 14px; }
  .typing span { width:6px; height:6px; border-radius:50%; background:var(--muted); animation:bounce 1.2s infinite; }
  .typing span:nth-child(2){animation-delay:.2s;} .typing span:nth-child(3){animation-delay:.4s;}
  @keyframes bounce { 0%,80%,100%{transform:translateY(0);} 40%{transform:translateY(-6px);} }
  .input-row { padding:16px; border-top:1px solid var(--border); display:flex; gap:10px; align-items:flex-end; }
  .input-wrap { flex:1; display:flex; flex-direction:column; gap:6px; }
  .timeline-panel { display:flex; flex-direction:column; overflow:hidden; }
  .timeline-header { padding:14px 16px; border-bottom:1px solid var(--border); display:flex; justify-content:space-between; align-items:center; }
  .timeline { flex:1; overflow-y:auto; padding:12px; display:flex; flex-direction:column; gap:8px; }
  .decision-card { background:var(--surface); border:1px solid var(--border); border-radius:var(--radius); padding:10px 12px; }
  .card-top { display:flex; justify-content:space-between; align-items:center; margin-bottom:5px; }
  .card-action { font-size:11px; font-weight:700; padding:2px 7px; border-radius:4px; text-transform:uppercase; }
  .card-action.scale-up{background:rgba(52,211,153,.15);color:var(--green);}
  .card-action.scale-down{background:rgba(96,165,250,.15);color:var(--blue);}
  .card-action.hold{background:rgba(139,146,165,.1);color:var(--muted);}
  .card-time { font-size:11px; color:var(--muted); font-family:var(--mono); }
  .card-deployment { font-size:12px; font-weight:600; }
  .card-reason { font-size:12px; color:var(--muted); margin-top:5px; line-height:1.4; }

  /* Generic panels (Autoscalers / Onboard / Load) */
  .panel { height:100%; overflow-y:auto; padding:24px 28px; }
  .panel-head { display:flex; align-items:center; gap:12px; margin-bottom:18px; }
  .panel-head h2 { font-size:16px; font-weight:600; }
  .panel-head p { color:var(--muted); font-size:12px; }

  table { width:100%; border-collapse:collapse; font-size:13px; }
  th, td { text-align:left; padding:9px 10px; border-bottom:1px solid var(--border); }
  th { color:var(--muted); font-weight:600; font-size:11px; text-transform:uppercase; letter-spacing:.5px; }
  tr:hover td { background:var(--surface); }
  .pill { font-size:11px; padding:2px 7px; border-radius:4px; font-family:var(--mono); }
  .pill.dry { background:rgba(251,191,36,.12); color:var(--yellow); }
  .pill.live { background:rgba(52,211,153,.12); color:var(--green); }
  .pill.owner { background:rgba(108,143,255,.12); color:var(--accent); }
  .pill.calibrated { background:rgba(96,165,250,.12); color:var(--blue); }

  input, select, textarea { background:var(--bg); border:1px solid var(--border); border-radius:var(--radius);
    padding:9px 11px; color:var(--text); font-size:13px; width:100%; font-family:inherit; }
  input:focus, select:focus, textarea:focus { outline:none; border-color:var(--accent); }
  textarea { resize:vertical; }
  label { display:block; font-size:12px; color:var(--muted); margin:0 0 5px; }
  .field { margin-bottom:14px; }
  .grid2 { display:grid; grid-template-columns:1fr 1fr; gap:14px; }
  .grid3 { display:grid; grid-template-columns:1fr 1fr 1fr; gap:14px; }
  .form-card { max-width:760px; background:var(--surface); border:1px solid var(--border);
    border-radius:var(--radius); padding:22px; }
  .form-section { font-size:11px; text-transform:uppercase; letter-spacing:.6px; color:var(--muted);
    margin:18px 0 10px; border-top:1px solid var(--border); padding-top:14px; }
  .form-section:first-of-type { border-top:none; padding-top:0; margin-top:0; }
  .checkbox-row { display:flex; align-items:center; gap:8px; }
  .checkbox-row input { width:auto; }

  .btn { background:var(--accent); border:none; border-radius:var(--radius); padding:9px 16px; color:#fff;
    cursor:pointer; font-size:13px; font-weight:600; }
  .btn:hover { opacity:.88; }
  .btn:disabled { opacity:.4; cursor:default; }
  .btn.ghost { background:none; border:1px solid var(--border); color:var(--muted); }
  .btn.ghost:hover { color:var(--text); }
  .btn.danger { background:var(--red); }
  .btn.sm { padding:4px 9px; font-size:12px; }
  .refresh-btn, .send-btn { background:var(--accent); border:none; border-radius:var(--radius); padding:9px 16px; color:#fff; cursor:pointer; font-weight:600; }
  .refresh-btn { background:none; border:1px solid var(--border); color:var(--muted); padding:4px 10px; font-size:12px; }

  .banner { padding:10px 14px; border-radius:var(--radius); margin-bottom:16px; font-size:13px; display:none; }
  .banner.show { display:block; }
  .banner.err { background:rgba(248,113,113,.12); color:var(--red); border:1px solid rgba(248,113,113,.3); }
  .banner.ok  { background:rgba(52,211,153,.12); color:var(--green); border:1px solid rgba(52,211,153,.3); }
  .empty-state { text-align:center; color:var(--muted); padding:40px 20px; }
  .welcome { color:var(--muted); font-size:13px; text-align:center; margin:auto; padding:40px 20px; }
  .welcome h3 { color:var(--text); margin-bottom:8px; }
  .suggestion { background:var(--surface); border:1px solid var(--border); border-radius:var(--radius);
    padding:8px 12px; margin:6px 0; cursor:pointer; font-size:13px; text-align:left; color:var(--text); width:100%; }
  .suggestion:hover { border-color:var(--accent); }
  .metric { display:inline-block; min-width:120px; }
  .metric .v { font-family:var(--mono); font-size:22px; }
  .metric .l { font-size:11px; color:var(--muted); }
  .hint { font-size:11px; color:var(--muted); margin-top:4px; }
</style>
</head>
<body>

<header class="header">
  <div class="dot"></div>
  <h1>Agentic Autoscaler</h1>
  <nav class="tabs">
    <button class="tab active" data-view="explore" onclick="showView('explore')">Explore</button>
    <button class="tab" data-view="autoscalers" onclick="showView('autoscalers')">Autoscalers</button>
    <button class="tab" data-view="onboard" onclick="showView('onboard')">Onboard</button>
    <button class="tab" data-view="load" id="loadTab" style="display:none" onclick="showView('load')">Load</button>
  </nav>
  <div class="spacer"></div>
  <span class="mode-badge ro" id="modeBadge">read-only</span>
</header>

<!-- ───────────────────────── Explore ───────────────────────── -->
<section class="view active" id="view-explore">
  <div class="explore">
    <div class="chat-panel">
      <div class="messages" id="messages">
        <div class="welcome">
          <h3>Ask about scaling decisions</h3>
          <p style="margin-bottom:16px">Answered from the live decision audit log.</p>
          <button class="suggestion" onclick="ask('Why did payment-service scale up?')">Why did payment-service scale up?</button>
          <button class="suggestion" onclick="ask('Show me all scale-up decisions in the last 24 hours')">Show me all scale-up decisions in the last 24 hours</button>
          <button class="suggestion" onclick="ask('Which log patterns fired the most this week?')">Which log patterns fired the most this week?</button>
        </div>
      </div>
      <div class="input-row">
        <div class="input-wrap">
          <input id="deploymentFilter" type="text" placeholder="Filter by deployment (optional)">
          <textarea id="questionInput" rows="1" placeholder="Ask anything about scaling decisions…"
            onkeydown="handleKey(event)" oninput="autoResize(this)"></textarea>
        </div>
        <button class="send-btn" id="sendBtn" onclick="sendQuestion()">Ask</button>
      </div>
    </div>
    <aside class="timeline-panel">
      <div class="timeline-header">
        <h2 style="font-size:13px">Recent Decisions</h2>
        <button class="refresh-btn" onclick="loadDecisions()">↻ Refresh</button>
      </div>
      <div class="timeline" id="timeline"><div class="empty-state">Loading…</div></div>
    </aside>
  </div>
</section>

<!-- ───────────────────────── Autoscalers ───────────────────────── -->
<section class="view" id="view-autoscalers">
  <div class="panel">
    <div class="panel-head">
      <h2>Managed autoscalers</h2><p>Every Deployment under agentic control.</p>
      <div class="spacer"></div>
      <button class="refresh-btn" onclick="loadAutoscalers()">↻ Refresh</button>
    </div>
    <div class="banner" id="asBanner"></div>
    <table>
      <thead><tr><th>Namespace</th><th>Target</th><th>Mode</th><th>Min</th><th>Max</th><th>Current</th><th>Mode</th><th>Last decision</th><th></th></tr></thead>
      <tbody id="asBody"><tr><td colspan="9" class="empty-state">Loading…</td></tr></tbody>
    </table>
  </div>
</section>

<!-- ───────────────────────── Onboard ───────────────────────── -->
<section class="view" id="view-onboard">
  <div class="panel">
    <div class="panel-head"><h2>Onboard a deployment</h2><p>Create an AgenticAutoscaler — no kubectl required.</p></div>
    <div class="banner" id="obBanner"></div>
    <div class="form-card">
      <div class="form-section">Target</div>
      <div class="grid2">
        <div class="field"><label>Namespace</label><select id="ob_ns" onchange="loadObDeployments()"></select></div>
        <div class="field"><label>Deployment</label><select id="ob_dep" onchange="obDepChanged()"></select></div>
      </div>
      <div class="field"><label>Autoscaler name (optional)</label><input id="ob_name" placeholder="defaults to <deployment>-autoscaler"></div>

      <div class="form-section">Signal sources</div>
      <div class="field"><label>Prometheus URL</label><input id="ob_prom" value="http://prometheus-operated.monitoring:9090"></div>
      <div class="grid2">
        <div class="field"><label>Loki URL</label><input id="ob_loki" value="http://loki.monitoring:3100"></div>
        <div class="field"><label>LogQL query</label><input id="ob_logql" value='{app="payment-service"}'></div>
      </div>

      <details style="margin-bottom:6px">
        <summary style="cursor:pointer; font-size:11px; text-transform:uppercase; letter-spacing:.6px; color:var(--muted); padding:8px 0;">Advanced — custom metric queries (optional)</summary>
        <p class="hint" style="margin:4px 0 12px">Leave blank to use the defaults (standard Prometheus HTTP metrics). Override these if your service exports different metric names. Use <code style="font-family:var(--mono)">$TARGET</code> for the deployment name.</p>
        <div class="field"><label>p99 latency query (seconds)</label><input id="ob_q_lat" placeholder='histogram_quantile(0.99, rate(http_request_duration_seconds_bucket{service="$TARGET"}[2m]))'></div>
        <div class="field"><label>Error rate query (%)</label><input id="ob_q_err" placeholder='rate(http_requests_total{service="$TARGET",status=~"5.."}[2m]) / rate(http_requests_total{service="$TARGET"}[2m]) * 100'></div>
        <div class="field"><label>CPU utilization query (%)</label><input id="ob_q_cpu" placeholder='avg(rate(container_cpu_usage_seconds_total{pod=~"$TARGET-.*"}[2m])) * 100'></div>
        <div class="field"><label>Requests/sec query</label><input id="ob_q_rps" placeholder='rate(http_requests_total{service="$TARGET"}[2m])'></div>
      </details>

      <div class="form-section">Scaling bounds</div>
      <div class="grid3">
        <div class="field"><label>Min replicas</label><input id="ob_min" type="number" value="2" min="0"></div>
        <div class="field"><label>Max replicas</label><input id="ob_max" type="number" value="10" min="1"></div>
        <div class="field"><label>Cooldown (s)</label><input id="ob_cooldown" type="number" value="60" min="0"></div>
      </div>
      <div class="field checkbox-row"><input id="ob_dryrun" type="checkbox" checked><label style="margin:0">Dry-run (log decisions, do not scale) — recommended for the first 7 days</label></div>

      <div class="form-section">AI provider</div>
      <div class="grid2">
        <div class="field"><label>Provider</label><select id="ob_provider"><option value="anthropic">anthropic</option><option value="openai">openai</option><option value="ollama">ollama</option></select></div>
        <div class="field"><label>Secret ref (API key)</label><input id="ob_secret" value="ai-provider-secret"></div>
      </div>

      <div class="form-section">HPA coexistence</div>
      <div class="grid2">
        <div class="field"><label>Mode</label><select id="ob_mode" onchange="obModeChanged()"><option value="owner">owner — sole controller</option><option value="calibrated">calibrated — nested in HPA</option></select></div>
        <div class="field" id="ob_hpaname_field" style="display:none"><label>HPA name</label><input id="ob_hpaname" placeholder="e.g. payment-service-hpa"></div>
      </div>

      <div class="form-section">Observability</div>
      <div class="grid2">
        <div class="field"><label>Grafana URL</label><input id="ob_grafana" value="http://kube-prometheus-stack-grafana.monitoring"></div>
        <div class="field"><label>Grafana secret ref</label><input id="ob_grafana_secret" value="grafana-api-secret"></div>
      </div>

      <div style="margin-top:18px; display:flex; gap:10px;">
        <button class="btn" id="ob_submit" onclick="submitOnboard()">Create autoscaler</button>
        <button class="btn ghost" onclick="showView('autoscalers')">Cancel</button>
      </div>
    </div>
  </div>
</section>

<!-- ───────────────────────── Load ───────────────────────── -->
<section class="view" id="view-load">
  <div class="panel">
    <div class="panel-head"><h2>Load generator</h2><p>Drive traffic at a service and watch the autoscaler react.</p></div>
    <div class="banner" id="ldBanner"></div>
    <div class="form-card">
      <div class="field"><label>Target URL</label><input id="ld_target" value="http://payment-service.production:9898"></div>
      <div class="grid3">
        <div class="field"><label>Requests / sec</label><input id="ld_rps" type="number" value="200" min="1"></div>
        <div class="field"><label>Duration (s)</label><input id="ld_dur" type="number" value="600" min="1"></div>
        <div class="field"><label>Error inject %</label><input id="ld_err" type="number" value="0" min="0" max="100"></div>
      </div>
      <div class="field"><label>Latency inject (ms, fraction of requests routed to a slow endpoint)</label><input id="ld_delay" type="number" value="0" min="0"></div>
      <div style="display:flex; gap:10px; margin-top:6px;">
        <button class="btn" onclick="startLoad()">▶ Start load</button>
        <button class="btn danger" onclick="stopLoad()">■ Stop</button>
        <button class="btn ghost" onclick="loadStatus()">↻ Status</button>
      </div>
    </div>
    <div style="margin-top:22px; display:flex; gap:32px;">
      <div class="metric"><div class="v" id="ld_state">idle</div><div class="l">generator</div></div>
      <div class="metric"><div class="v" id="ld_sent">0</div><div class="l">requests sent</div></div>
      <div class="metric"><div class="v" id="ld_rpsnow">0</div><div class="l">current rps</div></div>
    </div>
    <p class="hint" style="margin-top:18px">Tip: open the Autoscalers tab in another window to watch replicas climb, then press Stop and watch them fall.</p>
  </div>
</section>

<script>
  // ── State ────────────────────────────────────────────────────────────
  var config = { writeEnabled:false, authRequired:false, loadgenEnabled:false };
  var token = '';
  var decisions = [];

  // ── API helper ───────────────────────────────────────────────────────
  function isMutating(m){ return m === 'POST' || m === 'PATCH' || m === 'DELETE'; }
  async function api(method, path, body) {
    var headers = {};
    if (body !== undefined) headers['Content-Type'] = 'application/json';
    if (isMutating(method) && token) headers['Authorization'] = 'Bearer ' + token;
    var res = await fetch(path, {
      method: method, headers: headers,
      body: body !== undefined ? JSON.stringify(body) : undefined
    });
    var data = null;
    try { data = await res.json(); } catch(e) { data = null; }
    if (!res.ok) { var msg = (data && data.error) ? data.error : ('HTTP ' + res.status); throw new Error(msg); }
    return data;
  }
  function banner(id, kind, msg) {
    var el = document.getElementById(id);
    el.className = 'banner show ' + kind; el.textContent = msg;
    if (kind === 'ok') setTimeout(function(){ el.className = 'banner'; }, 4000);
  }
  function ensureToken() {
    if (config.writeEnabled && config.authRequired && !token) {
      token = (prompt('Enter the console auth token:') || '').trim();
    }
    return !config.authRequired || !!token;
  }

  // ── Tabs ─────────────────────────────────────────────────────────────
  function showView(name) {
    document.querySelectorAll('.view').forEach(function(v){ v.classList.remove('active'); });
    document.querySelectorAll('.tab').forEach(function(t){ t.classList.toggle('active', t.dataset.view === name); });
    document.getElementById('view-' + name).classList.add('active');
    if (name === 'autoscalers') loadAutoscalers();
    if (name === 'onboard') initOnboard();
    if (name === 'load') loadStatus();
  }

  // ── Config bootstrap ─────────────────────────────────────────────────
  async function loadConfig() {
    try { config = await api('GET', '/api/config'); } catch(e) {}
    var badge = document.getElementById('modeBadge');
    if (config.writeEnabled) { badge.textContent = 'read-write'; badge.className = 'mode-badge rw'; }
    else { badge.textContent = 'read-only'; badge.className = 'mode-badge ro'; }
    document.getElementById('loadTab').style.display = config.loadgenEnabled ? '' : 'none';
  }

  // ── Explore (chat + timeline) ────────────────────────────────────────
  async function loadDecisions() {
    try { decisions = await api('GET', '/api/decisions'); renderTimeline(decisions); }
    catch(e) { document.getElementById('timeline').innerHTML = '<div class="empty-state">Could not load decisions.</div>'; }
  }
  function renderTimeline(items) {
    var el = document.getElementById('timeline');
    if (!items || items.length === 0) { el.innerHTML = '<div class="empty-state">No decisions recorded yet.</div>'; return; }
    el.innerHTML = items.map(function(d){
      var t = new Date(d.timestamp);
      var timeStr = t.toLocaleString('en-US',{month:'short',day:'numeric',hour:'2-digit',minute:'2-digit'});
      var repStr = d.action === 'hold' ? d.oldReplicas + ' replicas' : d.oldReplicas + ' → ' + d.newReplicas;
      return '<div class="decision-card"><div class="card-top"><span class="card-action '+d.action+'">'+d.action+'</span>'+
        '<span class="card-time">'+timeStr+'</span></div><div class="card-deployment">'+esc(d.deployment)+'</div>'+
        '<div class="card-time">'+repStr+'</div>'+(d.reason?'<div class="card-reason">'+esc(d.reason)+'</div>':'')+'</div>';
    }).join('');
  }
  function ask(q){ document.getElementById('questionInput').value = q; sendQuestion(); }
  async function sendQuestion() {
    var input = document.getElementById('questionInput'), filter = document.getElementById('deploymentFilter');
    var btn = document.getElementById('sendBtn'), q = input.value.trim();
    if (!q) return;
    var msgs = document.getElementById('messages'), welcome = msgs.querySelector('.welcome');
    if (welcome) welcome.remove();
    appendMsg('user', q); input.value=''; autoResize(input); btn.disabled = true;
    var typingId = appendTyping();
    try {
      var data = await api('POST', '/api/query', {question:q, deployment:filter.value.trim()});
      removeTyping(typingId);
      appendMsg('assistant', data.answer || 'No response.', data.decisionsAnalyzed ? 'Analysed '+data.decisionsAnalyzed+' decisions' : '');
    } catch(e) { removeTyping(typingId); appendMsg('assistant', 'Request failed: ' + e.message); }
    finally { btn.disabled = false; }
  }
  function appendMsg(role, text, meta) {
    var msgs = document.getElementById('messages'), div = document.createElement('div');
    div.className = 'msg ' + role;
    div.innerHTML = '<div class="msg-bubble">'+esc(text)+'</div>' + (meta?'<div class="msg-meta">'+esc(meta)+'</div>':'');
    msgs.appendChild(div); msgs.scrollTop = msgs.scrollHeight; return div;
  }
  var typingCounter = 0;
  function appendTyping() {
    var id = 'typing-'+(++typingCounter), msgs = document.getElementById('messages'), div = document.createElement('div');
    div.id = id; div.className = 'msg assistant';
    div.innerHTML = '<div class="msg-bubble typing"><span></span><span></span><span></span></div>';
    msgs.appendChild(div); msgs.scrollTop = msgs.scrollHeight; return id;
  }
  function removeTyping(id){ var el = document.getElementById(id); if (el) el.remove(); }

  // ── Autoscalers ──────────────────────────────────────────────────────
  async function loadAutoscalers() {
    var body = document.getElementById('asBody');
    try {
      var items = await api('GET', '/api/autoscalers');
      if (!items.length) { body.innerHTML = '<tr><td colspan="9" class="empty-state">No autoscalers yet. Use the Onboard tab.</td></tr>'; return; }
      body.innerHTML = items.map(function(a){
        var dry = a.dryRun ? '<span class="pill dry">dry-run</span>' : '<span class="pill live">live</span>';
        var mode = '<span class="pill '+a.mode+'">'+esc(a.mode||'?')+'</span>';
        var actions = '';
        if (config.writeEnabled) {
          actions = '<button class="btn ghost sm" onclick="toggleDry('+q(a.namespace)+','+q(a.name)+','+(!a.dryRun)+')">'+(a.dryRun?'Go live':'Dry-run')+'</button> '+
                    '<button class="btn ghost sm" onclick="editBounds('+q(a.namespace)+','+q(a.name)+','+a.minReplicas+','+a.maxReplicas+')">Bounds</button> '+
                    '<button class="btn danger sm" onclick="delAutoscaler('+q(a.namespace)+','+q(a.name)+')">Delete</button>';
        }
        return '<tr><td>'+esc(a.namespace)+'</td><td>'+esc(a.targetDeployment)+'</td><td>'+dry+'</td>'+
          '<td>'+a.minReplicas+'</td><td>'+a.maxReplicas+'</td><td>'+a.currentReplicas+'</td><td>'+mode+'</td>'+
          '<td style="max-width:280px">'+esc(a.lastDecisionReason||'—')+'</td><td style="white-space:nowrap">'+actions+'</td></tr>';
      }).join('');
    } catch(e) { body.innerHTML = '<tr><td colspan="9" class="empty-state">'+esc(e.message)+'</td></tr>'; }
  }
  async function toggleDry(ns, name, dry) {
    if (!ensureToken()) return;
    try { await api('PATCH', '/api/autoscalers/'+ns+'/'+name, {dryRun:dry}); banner('asBanner','ok','Updated '+name); loadAutoscalers(); }
    catch(e) { banner('asBanner','err',e.message); }
  }
  async function editBounds(ns, name, min, max) {
    if (!ensureToken()) return;
    var nmin = parseInt(prompt('Min replicas:', min), 10); if (isNaN(nmin)) return;
    var nmax = parseInt(prompt('Max replicas:', max), 10); if (isNaN(nmax)) return;
    try { await api('PATCH','/api/autoscalers/'+ns+'/'+name, {minReplicas:nmin, maxReplicas:nmax}); banner('asBanner','ok','Updated bounds'); loadAutoscalers(); }
    catch(e) { banner('asBanner','err',e.message); }
  }
  async function delAutoscaler(ns, name) {
    if (!ensureToken()) return;
    if (!confirm('Stop managing '+name+'? The Deployment is left untouched.')) return;
    try { await api('DELETE','/api/autoscalers/'+ns+'/'+name); banner('asBanner','ok','Deleted '+name); loadAutoscalers(); }
    catch(e) { banner('asBanner','err',e.message); }
  }

  // ── Onboard ──────────────────────────────────────────────────────────
  var onboardInit = false;
  async function initOnboard() {
    if (onboardInit) return; onboardInit = true;
    try {
      var names = await api('GET','/api/namespaces');
      var sel = document.getElementById('ob_ns');
      sel.innerHTML = names.map(function(n){ return '<option>'+esc(n)+'</option>'; }).join('');
      if (names.indexOf('production') >= 0) sel.value = 'production';
      loadObDeployments();
    } catch(e) { banner('obBanner','err',e.message); }
  }
  async function loadObDeployments() {
    var ns = document.getElementById('ob_ns').value;
    try {
      var deps = await api('GET','/api/deployments?namespace='+encodeURIComponent(ns));
      var sel = document.getElementById('ob_dep');
      sel.innerHTML = deps.map(function(d){
        return '<option value="'+esc(d.name)+'"'+(d.managed?' disabled':'')+'>'+esc(d.name)+(d.managed?' (managed)':'')+(d.hasHPA?' [hpa]':'')+'</option>';
      }).join('');
      obDepChanged();
    } catch(e) { banner('obBanner','err',e.message); }
  }
  function obDepChanged() {
    var dep = document.getElementById('ob_dep').value;
    if (dep) document.getElementById('ob_logql').value = '{app="'+dep+'"}';
  }
  function obModeChanged() {
    document.getElementById('ob_hpaname_field').style.display =
      document.getElementById('ob_mode').value === 'calibrated' ? '' : 'none';
  }
  async function submitOnboard() {
    if (!ensureToken()) return;
    var ns = document.getElementById('ob_ns').value;
    var dep = document.getElementById('ob_dep').value;
    var mode = document.getElementById('ob_mode').value;
    var metrics = {};
    var qlat = document.getElementById('ob_q_lat').value.trim(); if (qlat) metrics.latencyP99 = qlat;
    var qerr = document.getElementById('ob_q_err').value.trim(); if (qerr) metrics.errorRate = qerr;
    var qcpu = document.getElementById('ob_q_cpu').value.trim(); if (qcpu) metrics.cpuUtilization = qcpu;
    var qrps = document.getElementById('ob_q_rps').value.trim(); if (qrps) metrics.requestsPerSecond = qrps;
    var payload = {
      name: document.getElementById('ob_name').value.trim(),
      namespace: ns,
      spec: {
        targetDeployment: dep, namespace: ns,
        prometheusURL: document.getElementById('ob_prom').value.trim(),
        metrics: metrics,
        logSource: { type:'loki', loki:{ url:document.getElementById('ob_loki').value.trim(), query:document.getElementById('ob_logql').value.trim() } },
        minReplicas: parseInt(document.getElementById('ob_min').value,10),
        maxReplicas: parseInt(document.getElementById('ob_max').value,10),
        cooldownSeconds: parseInt(document.getElementById('ob_cooldown').value,10),
        dryRun: document.getElementById('ob_dryrun').checked,
        aiProvider: { provider:document.getElementById('ob_provider').value, secretRef:document.getElementById('ob_secret').value.trim() },
        hpaCoexistence: { mode:mode, hpaName:document.getElementById('ob_hpaname').value.trim() },
        observability: { grafanaURL:document.getElementById('ob_grafana').value.trim(), secretRef:document.getElementById('ob_grafana_secret').value.trim() }
      }
    };
    var btn = document.getElementById('ob_submit'); btn.disabled = true;
    try {
      await api('POST','/api/autoscalers', payload);
      banner('obBanner','ok','Created autoscaler for '+dep); onboardInit = false;
      setTimeout(function(){ showView('autoscalers'); }, 800);
    } catch(e) { banner('obBanner','err',e.message); }
    finally { btn.disabled = false; }
  }

  // ── Load generator ───────────────────────────────────────────────────
  async function startLoad() {
    if (!ensureToken()) return;
    var payload = {
      targetURL: document.getElementById('ld_target').value.trim(),
      rps: parseInt(document.getElementById('ld_rps').value,10),
      durationSec: parseInt(document.getElementById('ld_dur').value,10),
      errorPct: parseInt(document.getElementById('ld_err').value,10),
      delayMs: parseInt(document.getElementById('ld_delay').value,10)
    };
    try { await api('POST','/api/load/start', payload); banner('ldBanner','ok','Load started'); loadStatus(); }
    catch(e) { banner('ldBanner','err',e.message); }
  }
  async function stopLoad() {
    if (!ensureToken()) return;
    try { await api('POST','/api/load/stop'); banner('ldBanner','ok','Load stopped'); loadStatus(); }
    catch(e) { banner('ldBanner','err',e.message); }
  }
  async function loadStatus() {
    if (!config.loadgenEnabled) return;
    if (!ensureToken()) return;
    try {
      var s = await api('GET','/api/load/status');
      document.getElementById('ld_state').textContent = s.running ? 'running' : 'idle';
      document.getElementById('ld_sent').textContent = s.requestsSent || 0;
      document.getElementById('ld_rpsnow').textContent = s.currentRPS || 0;
    } catch(e) { /* generator may be unreachable; leave readout */ }
  }

  // ── Utils ────────────────────────────────────────────────────────────
  function handleKey(e){ if (e.key === 'Enter' && !e.shiftKey){ e.preventDefault(); sendQuestion(); } }
  function autoResize(el){ el.style.height='auto'; el.style.height=Math.min(el.scrollHeight,120)+'px'; }
  function esc(s){ return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;').replace(/'/g,'&#39;'); }
  function q(s){ return JSON.stringify(String(s)); }

  // ── Init ─────────────────────────────────────────────────────────────
  loadConfig();
  loadDecisions();
  setInterval(loadDecisions, 30000);
  setInterval(function(){ if (document.getElementById('view-load').classList.contains('active')) loadStatus(); }, 4000);
</script>
</body>
</html>`)
