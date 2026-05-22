package queryapi

// uiHTML is the complete single-page application served at GET /.
// It is a self-contained HTML file with no external dependencies so it works
// in air-gapped clusters without internet access.
var uiHTML = []byte(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Agentic Autoscaler — Decision Inspector</title>
<style>
  *, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }

  :root {
    --bg:        #0f1117;
    --surface:   #1a1d27;
    --border:    #2a2d3e;
    --accent:    #6c8fff;
    --green:     #34d399;
    --red:       #f87171;
    --yellow:    #fbbf24;
    --muted:     #8b92a5;
    --text:      #e2e8f0;
    --radius:    8px;
    --mono:      'JetBrains Mono', 'Fira Code', monospace;
  }

  html, body { height: 100%; background: var(--bg); color: var(--text); font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif; font-size: 14px; }

  /* ── Layout ── */
  .layout { display: grid; grid-template-columns: 1fr 380px; grid-template-rows: 56px 1fr; height: 100vh; }
  .header { grid-column: 1 / -1; display: flex; align-items: center; gap: 12px; padding: 0 20px; border-bottom: 1px solid var(--border); background: var(--surface); }
  .header h1 { font-size: 15px; font-weight: 600; }
  .header .subtitle { color: var(--muted); font-size: 12px; }
  .dot { width: 8px; height: 8px; border-radius: 50%; background: var(--green); animation: pulse 2s infinite; }
  @keyframes pulse { 0%,100% { opacity:1; } 50% { opacity:.4; } }

  /* ── Chat panel ── */
  .chat-panel { display: flex; flex-direction: column; border-right: 1px solid var(--border); overflow: hidden; }
  .messages { flex: 1; overflow-y: auto; padding: 20px; display: flex; flex-direction: column; gap: 16px; }
  .messages::-webkit-scrollbar { width: 4px; }
  .messages::-webkit-scrollbar-thumb { background: var(--border); border-radius: 2px; }

  .msg { max-width: 88%; display: flex; flex-direction: column; gap: 4px; }
  .msg.user { align-self: flex-end; }
  .msg.assistant { align-self: flex-start; }
  .msg-bubble { padding: 10px 14px; border-radius: var(--radius); line-height: 1.5; white-space: pre-wrap; word-break: break-word; }
  .msg.user .msg-bubble { background: var(--accent); color: #fff; border-bottom-right-radius: 2px; }
  .msg.assistant .msg-bubble { background: var(--surface); border: 1px solid var(--border); border-bottom-left-radius: 2px; }
  .msg-meta { font-size: 11px; color: var(--muted); padding: 0 4px; }
  .msg.user .msg-meta { text-align: right; }

  .typing { display: flex; gap: 4px; padding: 10px 14px; }
  .typing span { width: 6px; height: 6px; border-radius: 50%; background: var(--muted); animation: bounce 1.2s infinite; }
  .typing span:nth-child(2) { animation-delay: .2s; }
  .typing span:nth-child(3) { animation-delay: .4s; }
  @keyframes bounce { 0%,80%,100% { transform: translateY(0); } 40% { transform: translateY(-6px); } }

  .input-row { padding: 16px; border-top: 1px solid var(--border); display: flex; gap: 10px; align-items: flex-end; }
  .input-wrap { flex: 1; display: flex; flex-direction: column; gap: 6px; }
  .deployment-filter { background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius); padding: 6px 10px; color: var(--text); font-size: 12px; width: 100%; }
  .deployment-filter::placeholder { color: var(--muted); }
  .question-input { background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius); padding: 10px 14px; color: var(--text); font-size: 14px; resize: none; min-height: 44px; max-height: 120px; overflow-y: auto; width: 100%; transition: border-color .15s; }
  .question-input:focus { outline: none; border-color: var(--accent); }
  .question-input::placeholder { color: var(--muted); }
  .send-btn { background: var(--accent); border: none; border-radius: var(--radius); padding: 10px 16px; color: #fff; cursor: pointer; font-size: 14px; font-weight: 600; white-space: nowrap; transition: opacity .15s; }
  .send-btn:hover { opacity: .85; }
  .send-btn:disabled { opacity: .4; cursor: default; }

  /* ── Timeline panel ── */
  .timeline-panel { display: flex; flex-direction: column; overflow: hidden; }
  .timeline-header { padding: 14px 16px; border-bottom: 1px solid var(--border); display: flex; justify-content: space-between; align-items: center; }
  .timeline-header h2 { font-size: 13px; font-weight: 600; }
  .refresh-btn { background: none; border: 1px solid var(--border); border-radius: var(--radius); padding: 4px 10px; color: var(--muted); cursor: pointer; font-size: 12px; }
  .refresh-btn:hover { color: var(--text); }
  .timeline { flex: 1; overflow-y: auto; padding: 12px; display: flex; flex-direction: column; gap: 8px; }
  .timeline::-webkit-scrollbar { width: 4px; }
  .timeline::-webkit-scrollbar-thumb { background: var(--border); border-radius: 2px; }

  .decision-card { background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius); padding: 10px 12px; cursor: pointer; transition: border-color .15s; }
  .decision-card:hover { border-color: var(--accent); }
  .decision-card .card-top { display: flex; justify-content: space-between; align-items: center; margin-bottom: 5px; }
  .card-action { font-size: 11px; font-weight: 700; padding: 2px 7px; border-radius: 4px; text-transform: uppercase; letter-spacing: .5px; }
  .card-action.scale-up   { background: rgba(52,211,153,.15); color: var(--green); }
  .card-action.scale-down { background: rgba(96,165,250,.15); color: #60a5fa; }
  .card-action.hold       { background: rgba(139,146,165,.1); color: var(--muted); }
  .card-time { font-size: 11px; color: var(--muted); font-family: var(--mono); }
  .card-deployment { font-size: 12px; font-weight: 600; margin-bottom: 3px; }
  .card-replicas { font-family: var(--mono); font-size: 12px; color: var(--muted); }
  .card-reason { font-size: 12px; color: var(--muted); margin-top: 5px; line-height: 1.4; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; }
  .card-patterns { display: flex; gap: 4px; flex-wrap: wrap; margin-top: 6px; }
  .pattern-tag { font-size: 10px; background: rgba(108,143,255,.12); color: var(--accent); padding: 1px 6px; border-radius: 3px; font-family: var(--mono); }
  .dry-run-badge { font-size: 10px; background: rgba(251,191,36,.1); color: var(--yellow); padding: 1px 6px; border-radius: 3px; margin-left: 4px; }

  .empty-state { text-align: center; color: var(--muted); padding: 40px 20px; font-size: 13px; }
  .welcome { color: var(--muted); font-size: 13px; text-align: center; margin: auto; padding: 40px 20px; }
  .welcome h3 { color: var(--text); margin-bottom: 8px; }
  .suggestion { background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius); padding: 8px 12px; margin: 6px 0; cursor: pointer; font-size: 13px; text-align: left; color: var(--text); width: 100%; transition: border-color .15s; }
  .suggestion:hover { border-color: var(--accent); }
</style>
</head>
<body>
<div class="layout">

  <header class="header">
    <div class="dot"></div>
    <h1>Agentic Autoscaler</h1>
    <span class="subtitle">— Decision Inspector</span>
  </header>

  <!-- Chat panel -->
  <section class="chat-panel">
    <div class="messages" id="messages">
      <div class="welcome">
        <h3>Ask about scaling decisions</h3>
        <p style="margin-bottom:16px">Questions answered using the live decision audit log.</p>
        <button class="suggestion" onclick="ask('Why did payment-service scale up?')">Why did payment-service scale up?</button>
        <button class="suggestion" onclick="ask('Show me all scale-up decisions in the last 24 hours')">Show me all scale-up decisions in the last 24 hours</button>
        <button class="suggestion" onclick="ask('Which log patterns fired the most this week?')">Which log patterns fired the most this week?</button>
        <button class="suggestion" onclick="ask('Were there any low-confidence decisions I should review?')">Were there any low-confidence decisions I should review?</button>
      </div>
    </div>
    <div class="input-row">
      <div class="input-wrap">
        <input class="deployment-filter" id="deploymentFilter" type="text" placeholder="Filter by deployment (optional)">
        <textarea class="question-input" id="questionInput" rows="1"
          placeholder="Ask anything about scaling decisions…"
          onkeydown="handleKey(event)"
          oninput="autoResize(this)"></textarea>
      </div>
      <button class="send-btn" id="sendBtn" onclick="sendQuestion()">Ask</button>
    </div>
  </section>

  <!-- Timeline panel -->
  <aside class="timeline-panel">
    <div class="timeline-header">
      <h2>Recent Decisions</h2>
      <button class="refresh-btn" onclick="loadDecisions()">↻ Refresh</button>
    </div>
    <div class="timeline" id="timeline">
      <div class="empty-state">Loading decisions…</div>
    </div>
  </aside>

</div>

<script>
  let decisions = [];

  // ── Timeline ──────────────────────────────────────────────────────────

  async function loadDecisions() {
    try {
      const r = await fetch('/api/decisions');
      decisions = await r.json();
      renderTimeline(decisions);
    } catch(e) {
      document.getElementById('timeline').innerHTML =
        '<div class="empty-state">Could not load decisions.<br>Is the operator running?</div>';
    }
  }

  function renderTimeline(items) {
    const el = document.getElementById('timeline');
    if (!items || items.length === 0) {
      el.innerHTML = '<div class="empty-state">No decisions recorded yet.<br>The operator writes here after every reconcile.</div>';
      return;
    }
    el.innerHTML = items.map(d => {
      const t = new Date(d.timestamp);
      const timeStr = t.toLocaleString('en-US', {month:'short',day:'numeric',hour:'2-digit',minute:'2-digit'});
      const repStr = d.action === 'hold'
        ? d.oldReplicas + ' replicas (unchanged)'
        : d.oldReplicas + ' → ' + d.newReplicas + ' replicas';
      const patterns = (d.patternsMatched || []).map(p =>
        '<span class="pattern-tag">'+esc(p)+'</span>').join('');
      const dryBadge = d.dryRun ? '<span class="dry-run-badge">dry-run</span>' : '';
      return '<div class="decision-card" onclick="fillFromCard('+JSON.stringify(esc(d.deployment))+')">' +
        '<div class="card-top">' +
          '<span class="card-action '+d.action+'">'+d.action+'</span>' +
          '<span class="card-time">'+timeStr+'</span>' +
        '</div>' +
        '<div class="card-deployment">'+esc(d.deployment)+dryBadge+'</div>' +
        '<div class="card-replicas">'+repStr+'</div>' +
        (d.reason ? '<div class="card-reason">'+esc(d.reason)+'</div>' : '') +
        (patterns ? '<div class="card-patterns">'+patterns+'</div>' : '') +
      '</div>';
    }).join('');
  }

  function fillFromCard(deployment) {
    document.getElementById('deploymentFilter').value = deployment;
    document.getElementById('questionInput').focus();
  }

  // ── Chat ──────────────────────────────────────────────────────────────

  function ask(q) {
    document.getElementById('questionInput').value = q;
    sendQuestion();
  }

  async function sendQuestion() {
    const input = document.getElementById('questionInput');
    const filter = document.getElementById('deploymentFilter');
    const btn = document.getElementById('sendBtn');
    const q = input.value.trim();
    if (!q) return;

    // Clear welcome screen on first question
    const msgs = document.getElementById('messages');
    const welcome = msgs.querySelector('.welcome');
    if (welcome) welcome.remove();

    appendMsg('user', q);
    input.value = '';
    autoResize(input);
    btn.disabled = true;

    const typingId = appendTyping();

    try {
      const res = await fetch('/api/query', {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({question: q, deployment: filter.value.trim()})
      });
      const data = await res.json();
      removeTyping(typingId);
      const meta = data.decisionsAnalyzed
        ? 'Analysed ' + data.decisionsAnalyzed + ' decisions'
        : '';
      appendMsg('assistant', data.answer || data.error || 'No response.', meta);
    } catch(e) {
      removeTyping(typingId);
      appendMsg('assistant', 'Request failed. Is the operator running?');
    } finally {
      btn.disabled = false;
    }
  }

  function appendMsg(role, text, meta) {
    const msgs = document.getElementById('messages');
    const div = document.createElement('div');
    div.className = 'msg ' + role;
    div.innerHTML =
      '<div class="msg-bubble">'+esc(text)+'</div>' +
      (meta ? '<div class="msg-meta">'+esc(meta)+'</div>' : '');
    msgs.appendChild(div);
    msgs.scrollTop = msgs.scrollHeight;
    return div;
  }

  let typingCounter = 0;
  function appendTyping() {
    const id = 'typing-' + (++typingCounter);
    const msgs = document.getElementById('messages');
    const div = document.createElement('div');
    div.id = id;
    div.className = 'msg assistant';
    div.innerHTML = '<div class="msg-bubble typing"><span></span><span></span><span></span></div>';
    msgs.appendChild(div);
    msgs.scrollTop = msgs.scrollHeight;
    return id;
  }
  function removeTyping(id) {
    const el = document.getElementById(id);
    if (el) el.remove();
  }

  // ── Utils ─────────────────────────────────────────────────────────────

  function handleKey(e) {
    if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); sendQuestion(); }
  }
  function autoResize(el) {
    el.style.height = 'auto';
    el.style.height = Math.min(el.scrollHeight, 120) + 'px';
  }
  function esc(s) {
    return String(s)
      .replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;')
      .replace(/"/g,'&quot;').replace(/'/g,'&#39;');
  }

  // ── Init ──────────────────────────────────────────────────────────────
  loadDecisions();
  setInterval(loadDecisions, 30000); // refresh timeline every 30s
</script>
</body>
</html>`)
