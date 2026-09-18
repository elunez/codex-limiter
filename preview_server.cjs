const http = require('http');
const fs = require('fs');
const path = require('path');

const PORT = Number(process.env.PREVIEW_PORT || 8766);
const pageGo = fs.readFileSync(path.join(__dirname, 'page.go'), 'utf8');
const match = pageGo.match(/const statusPageHTML = `([\s\S]*?)`\n/);
if (!match) throw new Error('无法从 page.go 提取页面');
const html = match[1].replace("function savedManagementKey(){", "function savedManagementKey(){return 'preview-token';");

let settings = {
  quota_source: 'realtime',
  session_affinity_enabled: true,
  session_affinity_ttl_seconds: 3600
};

const now = Date.now();
let accounts = [
  account('auth-dev', 'dev-team@example.com', 'plus', 3, 0, 78, 64, true, false),
  account('auth-ops', 'ops@example.com', 'team', 0, 0, 86, 97, true, true),
  account('auth-qa', 'qa@example.com', 'plus', 0, 0, 41, 52, false, false),
  account('auth-bot', 'bot@example.com', 'team', 3, 2, 33, 28, true, false),
  account('auth-stage', 'stage@example.com', 'plus', 0, 0, 18, 37, false, false),
  account('auth-support', 'support@example.com', 'team', 1, 0, 24, 46, true, false),
  account('auth-ci', 'ci@example.com', 'plus', 0, 0, 12, 21, false, false)
];

function account(authId, email, plan, active, queued, five, week, controlled, blocked) {
  const accountSettings = { max_concurrency_per_account: 3, queue_timeout_seconds: 300, five_hour: { enabled: true, cutoff_percent: 95 }, weekly: { enabled: true, cutoff_percent: 95 }, match_policy: 'any', query_failure_policy: 'allow' };
  return {
    auth_id: authId, auth_index: authId + '-index', name: authId + '.json', email, plan, priority: 0,
    quota: {
      five_hour: { present: true, used_percent: five, reset_at: new Date(now + 2 * 3600000 + 14 * 60000).toISOString() },
      weekly: { present: true, used_percent: week, reset_at: nextMonday().toISOString() }
    },
    control_enabled: controlled,
    concurrency: { active: controlled ? active : 0, queued: controlled ? queued : 0 }, limit: 3,
    effective_settings: accountSettings,
    override: { use_global: !controlled, settings: accountSettings },
    decision: { blocked: controlled && blocked, unknown: false, reason: controlled ? (blocked ? `周额度 ${week.toFixed(1)}% ≥ 95.0%` : '额度低于停止阈值') : '未启用调度控制' }
  };
}

function nextMonday() {
  const date = new Date();
  const days = (8 - date.getDay()) % 7 || 7;
  date.setDate(date.getDate() + days);
  date.setHours(8, 0, 0, 0);
  return date;
}

function payload() {
  const summary = accounts.reduce((out, item) => {
    out.total++;
    out.active += item.concurrency.active;
    out.queued += item.concurrency.queued;
    if (item.control_enabled && item.decision.blocked) out.blocked++; else out.schedulable++;
    return out;
  }, { schedulable: 0, total: 0, active: 0, queued: 0, blocked: 0 });
  return { plugin_id: 'codex-limiter', version: '0.0.3', generated_at: new Date().toISOString(), settings, summary, accounts };
}

function json(res, status, value) {
  res.writeHead(status, { 'Content-Type': 'application/json; charset=utf-8', 'Cache-Control': 'no-store' });
  res.end(JSON.stringify(value));
}

function readBody(req) {
  return new Promise((resolve, reject) => {
    let body = '';
    req.on('data', chunk => body += chunk);
    req.on('end', () => { try { resolve(JSON.parse(body || '{}')); } catch (error) { reject(error); } });
    req.on('error', reject);
  });
}

const prefix = '/v0/management/plugins/codex-limiter';
const server = http.createServer(async (req, res) => {
  const url = new URL(req.url, 'http://' + req.headers.host);
  if (url.pathname === '/' || url.pathname === '/status') {
    res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
    res.end(html);
    return;
  }
  if (url.pathname === prefix + '/status' && req.method === 'GET') return json(res, 200, payload());
  if (url.pathname === prefix + '/runtime' && req.method === 'GET') return json(res, 200, payload());
  if (url.pathname === prefix + '/refresh' && req.method === 'POST') return json(res, 200, payload());
  if (url.pathname === prefix + '/settings' && req.method === 'PUT') {
    const update = await readBody(req);
    settings = {
      quota_source: update.quota_source,
      session_affinity_enabled: update.session_affinity_enabled !== false,
      session_affinity_ttl_seconds: Number(update.session_affinity_ttl_seconds) || 3600
    };
    return json(res, 200, { ok: true });
  }
  if (url.pathname === prefix + '/account' && req.method === 'PUT') {
    const update = await readBody(req);
    accounts = accounts.map(item => item.auth_id === update.auth_id ? { ...item, control_enabled: !update.override.use_global, override: update.override, effective_settings: update.override.settings, limit: update.override.settings.max_concurrency_per_account, concurrency: update.override.use_global ? { active: 0, queued: 0 } : item.concurrency, decision: update.override.use_global ? { blocked: false, unknown: false, reason: '未启用调度控制' } : item.decision } : item);
    return json(res, 200, { ok: true });
  }
  json(res, 404, { error: 'not found' });
});

server.listen(PORT, '127.0.0.1', () => console.log('Preview server running at http://127.0.0.1:' + PORT + '/'));
