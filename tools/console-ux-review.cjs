const fs = require('node:fs');
const path = require('node:path');
const { createConsoleFixture } = require('./console-ui-fixture.cjs');

const root = path.resolve(__dirname, '..');
const evidence = path.join(root, '.build/ui-ux-review-20260910');
const fieldPath = path.join(root, '.build/field-repair-20260910/final/152-api.json');
const views = ['overview', 'topology', 'operations', 'nodes', 'metrics', 'operation-log', 'about', 'settings'];

// The local review never forwards traffic. Replay only UI fields, excluding raw evidence and credentials.
function clean(value) {
  if (Array.isArray(value)) return value.map(clean);
  if (value && typeof value === 'object') return Object.fromEntries(Object.entries(value)
    .filter(([key]) => !/password|secret|token|passfile|conninfo|credential|raw|stdout|stderr|output|environment/i.test(key))
    .map(([key, item]) => [key, clean(item)]));
  if (typeof value === 'string') return value
    .replace(/\b(password|passwd|token|secret|passfile)\s*[=:]\s*(?:'[^']*'|"[^"]*"|[^\s,;]+)/gi, '$1=[REDACTED]')
    .replace(/(postgres(?:ql)?:\/\/[^:@/]+:)[^@/]+@/gi, '$1[REDACTED]@');
  return value;
}

function createReviewFixture({ htmlPath, field = false } = {}) {
  const fixture = createConsoleFixture(htmlPath);
  for (const operation of fixture.operations) operation.target_id ||= operation.operation.target_id;
  fixture.field = false;
  if (field) {
    const snapshot = JSON.parse(fs.readFileSync(fieldPath, 'utf8'));
    const source = clean(snapshot.result);
    fixture.field = true;
    fixture.capturedAt = snapshot.utc;
    fixture.clusters.splice(0, fixture.clusters.length, ...source.clusters.body.result);
    fixture.operations.splice(0);
    fixture.control.hook = ({ req, url }) => {
      if (req.method !== 'GET') {
        fixture.control.mutations++;
        return { status:403, message:'只读评审：未提交现场操作。' };
      }
      const route = url.pathname.replace('/api/v1/', '');
      if (route === 'auth/me') return { result:{ user:{ username:'review-only', display_name:'只读评审', role:'viewer' } } };
      if (source[route]) return { status:source[route].http, result:source[route].body?.result, message:source[route].body?.message };
      return { status:503, message:'此项未包含在现场快照中；不是实时查询。' };
    };
  }
  const original = fixture.server.listeners('request')[0];
  const samples = field ? createReviewFixture({ htmlPath }) : null;
  fixture.server.removeAllListeners('request');
  fixture.server.on('request', (req, res) => {
    const url = new URL(req.url, 'http://localhost');
    if (samples && (url.pathname.startsWith('/sample/') || (url.pathname.startsWith('/api/') && new URL(req.headers.referer || 'http://localhost').pathname.startsWith('/sample/')))) {
      samples.server.emit('request', req, res); return;
    }
    if (url.pathname.startsWith('/api/')) return original(req, res);
    if (url.pathname === '/design-rules') {
      res.setHeader('Content-Type','text/plain; charset=utf-8');
      res.end(fs.readFileSync(path.join(root,'docs/zh-CN/ui-ux-review-2026-09-10.md'))); return;
    }
    if (url.pathname.startsWith('/evidence/')) {
      const relative = url.pathname.slice('/evidence/'.length);
      const file = path.resolve(evidence, relative);
      if (!file.startsWith(evidence + path.sep) || !/\.(png|json)$/.test(file) || !fs.existsSync(file)) { res.writeHead(404); res.end(); return; }
      res.setHeader('Content-Type', file.endsWith('.png') ? 'image/png' : 'application/json');
      res.end(fs.readFileSync(file)); return;
    }
    if (url.pathname === '/review' && fs.existsSync(path.join(root, 'preview/ux-review/index.html'))) {
      res.setHeader('Content-Type', 'text/html; charset=utf-8');
      res.end(fs.readFileSync(path.join(root, 'preview/ux-review/index.html'))); return;
    }
    const before = url.pathname === '/before/';
    const file = before ? path.join(evidence, 'before/console.html') : htmlPath || path.join(root, 'internal/api/console.html');
    const label = field ? `现场快照 ${fixture.capturedAt} · 只读回放 · 日志/指标未采集` : '隔离测试数据 · 不连接现场';
    let html = fs.readFileSync(file, 'utf8');
    html = html.replace('<body>', `<body><div style="padding:8px 16px;border-bottom:1px solid #ddd;background:#fff;color:#555;font:12px/1.5 system-ui" role="note">${label} <a href="/review" style="color:inherit;margin-left:12px">评审材料</a></div>`);
    res.setHeader('Content-Type', 'text/html; charset=utf-8');
    res.setHeader('Cache-Control', 'no-store');
    res.end(html);
  });
  return fixture;
}

async function capture(phase) {
  const { chromium } = require('playwright');
  const fixture = createReviewFixture();
  await new Promise(resolve => fixture.server.listen(0, '127.0.0.1', resolve));
  const browser = await chromium.launch({ channel:'chrome', headless:true });
  const directory = path.join(evidence, phase);
  fs.mkdirSync(directory, { recursive:true });
  const checks = [], errors = [];
  try {
    const page = await browser.newPage();
    page.on('pageerror', error => errors.push(error.message));
    const prefix = phase === 'before' ? '/before/' : '/?ui=review';
    await page.goto(`http://127.0.0.1:${fixture.server.address().port}${prefix}#overview`);
    await page.waitForFunction(() => state.clusterDataReady);
    for (const width of [1440, 1024, 768, 390]) {
      await page.setViewportSize({ width, height:width < 500 ? 844 : 1000 });
      for (const view of views) {
        await page.locator(`[data-nav="${view}"]`).click();
        await page.locator(`[data-view="${view}"]`).waitFor({ state:'visible' });
        if (view === 'operation-log') await page.waitForFunction(() => state.logLoaded && !state.logController);
        await page.locator('#live-status').waitFor({ state:'hidden' });
        await page.evaluate(() => scrollTo(0,0));
        await page.screenshot({ path:path.join(directory, `${view}-${width}.png`) });
        await page.screenshot({ path:path.join(directory, `${view}-${width}-full.png`), fullPage:true });
        const issues = await page.evaluate(() => ({
          overflow:document.documentElement.scrollWidth > innerWidth + 1,
          clipped:[...document.querySelectorAll('button, select, a')].filter(el => el.getBoundingClientRect().width > 0 && el.scrollWidth > el.clientWidth + 3).map(el => el.id || el.textContent.trim()).slice(0,20)
        }));
        checks.push({ view, width, visibleView:await page.locator('.view:not([hidden])').getAttribute('data-view'), ...issues });
      }
    }
    const report = { phase, scope:'isolated browser fixture; not production acceptance', checks, errors, mutations:fixture.control.mutations };
    fs.writeFileSync(path.join(directory, 'capture.json'), JSON.stringify(report, null, 2));
    console.log(JSON.stringify(report));
  } finally { await browser.close(); fixture.server.closeAllConnections(); await new Promise(resolve => fixture.server.close(resolve)); }
}

module.exports = { createReviewFixture, evidence, views };
if (require.main === module) {
  const phase = process.argv[2];
  if (phase === 'before' || phase === 'after') capture(phase).catch(error => { console.error(error); process.exitCode = 1; });
  else {
    const fixture = createReviewFixture({ field:!process.argv.includes('--fixtures') });
    fixture.server.listen(Number(process.env.PORT || 18810), '127.0.0.1', () => console.log(`UI review: http://127.0.0.1:${fixture.server.address().port}/review`));
  }
}
