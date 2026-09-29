// Acceptance: the settings view merged "控制台状态" and "账户与偏好" into a
// single "状态设置" section, and the merge has to be observable in a real
// browser rather than only in the HTML source.
//
// Three things can silently regress here and none of them are visible to a
// string contract: the fourth tab creeping back, the account block ending up
// outside the panel it now belongs to (so selecting 运行参数 leaves it on
// screen), and the keyboard rotation still stepping through four sections.
// The run drives the real console served by tools/console-ui-fixture.cjs over
// the DevTools protocol, with no extra dependency.
//
//   node tools/console-settings-merge-acceptance.cjs
//
// Set CHROME_BIN to point at a specific browser. When no browser is found the
// script reports SKIP instead of failing a machine that cannot run it.

const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawn } = require('node:child_process');
const { createConsoleFixture } = require('./console-ui-fixture.cjs');

const CHROME_CANDIDATES = [
  process.env.CHROME_BIN,
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  '/Applications/Chromium.app/Contents/MacOS/Chromium',
  '/usr/bin/google-chrome',
  '/usr/bin/chromium',
  '/usr/bin/chromium-browser',
].filter(Boolean);

const TOTAL_BUDGET_MS = 90000;

// A confined or containerised shell cannot always give Chrome the namespaces it
// wants, and Chrome exits with "Failed to initialize sandbox / GPU process
// isn't usable" before the DevTools port ever opens - which reads as a hang
// unless the launch is retried without the sandbox. The retry is deliberately a
// second attempt rather than the default, so an unconfined machine keeps the
// browser's own sandbox.
const BASE_ARGS = ['--headless=new', '--no-first-run', '--no-default-browser-check', '--disable-extensions'];
const CONFINED_ARGS = ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'];

const DRIVER = `(async () => {
  const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
  const until = async (probe, budget) => {
    const started = Date.now();
    while (Date.now() - started < budget) {
      let ready = false;
      try { ready = probe(); } catch (_) {}
      if (ready) return true;
      await sleep(50);
    }
    return false;
  };
  const shown = element => !!element && element.getClientRects().length > 0;
  const result = { steps: [], failures: [] };
  const check = (name, ok, detail) => {
    result.steps.push((ok ? 'ok   ' : 'BAD  ') + name + (detail === undefined ? '' : ' -> ' + detail));
    if (!ok) result.failures.push(name);
  };

  try {
    if (!await until(() => !!document.querySelector('[data-nav="settings"]'), 20000)) {
      throw new Error('console never finished booting');
    }
    document.querySelector('[data-nav="settings"]').click();
    if (!await until(() => document.querySelector('[data-view="settings"]').hidden === false, 8000)) {
      throw new Error('the settings view never became visible');
    }

    const tabs = [...document.querySelectorAll('.settings-section-tabs [role="tab"]')];
    const labels = tabs.map(tab => tab.textContent.trim());
    result.tabs = tabs.map(tab => ({ id:tab.id, label:tab.textContent.trim(), section:tab.dataset.settingsSection }));
    check('exactly three settings tabs remain', tabs.length === 3, tabs.length + ' -> ' + labels.join(' / '));
    check('the tab is renamed 状态设置, and 运行参数 / 版本更新 are untouched',
      JSON.stringify(labels) === JSON.stringify(['状态设置', '运行参数', '版本更新']), labels.join(' / '));
    check('no separate account tab is left behind',
      !document.getElementById('settings-account-tab') && !document.getElementById('settings-account-panel'));

    const statusPanel = document.getElementById('settings-status-panel');
    const accountBlock = document.querySelector('.settings-account-block');
    // The shell is still hidden while the console boots against the fixture, so
    // nothing has a layout box yet. Wait for the block itself to be laid out
    // before measuring anything about it.
    const laidOut = await until(() => shown(accountBlock), 20000);
    result.waitedForLayout = laidOut;
    check('the account block exists exactly once', document.querySelectorAll('.settings-account-block').length === 1);
    check('the account block lives inside the status panel',
      !!statusPanel && !!accountBlock && statusPanel.contains(accountBlock));
    check('the account block is not a tabpanel of its own', !!accountBlock && accountBlock.getAttribute('role') !== 'tabpanel');
    check('the status panel is still the selected tabpanel',
      !!statusPanel && statusPanel.getAttribute('role') === 'tabpanel' &&
      document.getElementById('settings-status-tab').getAttribute('aria-controls') === 'settings-status-panel');

    const summary = document.getElementById('settings-section-summary').textContent.trim();
    check('the section summary now covers account preferences', /账户/.test(summary), summary);

    const why = () => {
      const chain = [];
      for (let node = accountBlock; node && node !== document.body; node = node.parentElement) {
        const style = getComputedStyle(node);
        chain.push((node.id || node.className || node.tagName) + '|hidden=' + node.hidden + '|display=' + style.display + '|rects=' + node.getClientRects().length);
      }
      return 'view=' + document.querySelector('[data-view="settings"]').hidden + '|panel=' + statusPanel.hidden + '|' + chain.join(' << ');
    };
    const visible = shown(accountBlock);
    check('the account block is visible while 状态设置 is selected', visible, visible ? undefined : why());
    const password = shown(document.getElementById('settings-change-password'));
    check('the 修改密码 button is reachable in the merged section', password, password ? undefined : why());
    const preferences = shown(document.getElementById('language-select')) && shown(document.getElementById('refresh-interval'));
    check('界面语言 and 自动刷新 are still rendered in the merged section', preferences, preferences ? undefined : why());

    document.getElementById('settings-configuration-tab').click();
    await sleep(200);
    check('selecting 运行参数 hides the status panel', statusPanel.hidden === true);
    check('selecting 运行参数 takes the account block off screen', !shown(accountBlock));

    document.getElementById('settings-status-tab').click();
    await sleep(200);
    check('selecting 状态设置 brings the account block back', shown(accountBlock));

    document.getElementById('settings-change-password').click();
    check('修改密码 still opens its dialog from the merged section',
      await until(() => document.getElementById('password-modal').open, 5000));
    if (document.getElementById('password-modal').open) document.getElementById('cancel-password-change').click();

    const rotation = [];
    document.getElementById('settings-status-tab').focus();
    for (let step = 0; step < 4; step += 1) {
      const active = [...document.querySelectorAll('.settings-section-tabs [role="tab"]')]
        .find(tab => tab.getAttribute('aria-selected') === 'true');
      rotation.push(active ? active.id : 'none');
      const from = document.activeElement && document.activeElement.getAttribute('role') === 'tab' ? document.activeElement : active;
      from.dispatchEvent(new KeyboardEvent('keydown', { key:'ArrowRight', bubbles:true, cancelable:true }));
      await sleep(120);
    }
    result.rotation = rotation;
    check('ArrowRight rotates across exactly three sections', new Set(rotation).size === 3 && rotation.length === 4,
      rotation.join(' -> '));

    result.passed = result.failures.length === 0;
  } catch (error) {
    result.error = String((error && error.message) || error);
    result.passed = false;
  }
  return result;
})()`;

const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
const step = message => console.log(`[${new Date().toISOString().slice(11, 19)}] ${message}`);

const getJSON = async (url, options = {}) => {
  const response = await fetch(url, { ...options, signal: AbortSignal.timeout(5000) });
  if (!response.ok) throw new Error(`${options.method || 'GET'} ${url} -> ${response.status}`);
  return response.json();
};

const waitForDevTools = async (port, attempts = 100) => {
  for (let attempt = 0; attempt < attempts; attempt += 1) {
    try { await getJSON(`http://127.0.0.1:${port}/json/version`); return; }
    catch (_) { await sleep(100); }
  }
  throw new Error(`chrome devtools endpoint on port ${port} never became reachable`);
};

// The message listener is attached in the same tick the socket is created, not
// after the open event: a socket that is already delivering its greeting
// messages loses anything dispatched in between, and the first command then
// waits for an answer nobody is listening for.
//
// The session also keeps an event listener list, so a page error cannot be
// mistaken for a quiet pass - every Runtime.exceptionThrown is collected
// alongside the driver's own verdict.
const openSession = url => new Promise((resolve, reject) => {
  const socket = new WebSocket(url);
  let sequence = 0;
  const pending = new Map();
  const listeners = new Set();
  socket.addEventListener('message', event => {
    const message = JSON.parse(event.data);
    if (process.env.CG_CDP_TRACE) console.log(`cdp >> ${JSON.stringify(message).slice(0, 180)}`);
    if (!message.id) { for (const listener of listeners) listener(message); return; }
    const handler = pending.get(message.id);
    if (!handler) return;
    pending.delete(message.id);
    if (message.error) handler.reject(new Error(message.error.message));
    else handler.resolve(message.result);
  });
  const timer = setTimeout(() => reject(new Error('devtools websocket never opened')), 5000);
  socket.addEventListener('error', () => { clearTimeout(timer); reject(new Error('devtools websocket failed')); }, { once:true });
  const send = (method, params = {}, timeoutMs = 10000) => new Promise((res, rej) => {
    sequence += 1;
    pending.set(sequence, { resolve:res, reject:rej });
    if (process.env.CG_CDP_TRACE) console.log(`cdp << ${JSON.stringify({ id: sequence, method })}`);
    socket.send(JSON.stringify({ id:sequence, method, params }));
    setTimeout(() => { if (pending.delete(sequence)) rej(new Error(`${method} timed out after ${timeoutMs}ms`)); }, timeoutMs);
  });
  send.on = listener => { listeners.add(listener); return () => listeners.delete(listener); };
  send.close = () => { try { socket.close(); } catch (_) {} };
  socket.addEventListener('open', () => { clearTimeout(timer); resolve(send); }, { once:true });
});

const startFixture = () => {
  const fixture = createConsoleFixture();
  const { server } = fixture;
  const sockets = new Set();
  server.on('connection', socket => { sockets.add(socket); socket.on('close', () => sockets.delete(socket)); });
  return new Promise(resolve => server.listen(0, '127.0.0.1', () =>
    resolve({ server, sockets, origin:`http://127.0.0.1:${server.address().port}` })));
};

// A reachable port is not a working browser. When Chrome cannot set up its own
// sandbox it opens the DevTools port and then dies, so a probe that stops at
// "the port answers" reports success for a browser that will never reply to a
// single command - the run then hangs on its first CDP call. Round-trip one
// command instead, and treat only that as a live browser.
// The probe has to reach a *page* target, not the browser target: with a broken
// renderer sandbox the browser-level endpoint still answers happily while every
// page command is swallowed, which is exactly the failure being detected.
const pageAnswersOverCDP = async port => {
  const target = await getJSON(`http://127.0.0.1:${port}/json/new?${encodeURIComponent('about:blank')}`, { method:'PUT' });
  const send = await openSession(target.webSocketDebuggerUrl);
  try { await send('Runtime.enable', {}, 5000); return true; }
  catch (_) { return false; }
  finally {
    send.close();
    try { await fetch(`http://127.0.0.1:${port}/json/close/${target.id}`, { signal:AbortSignal.timeout(3000) }); } catch (_) {}
  }
};

// Start the browser and prove it answers over CDP. `chrome` comes back null
// when it does not, so the caller can decide whether to retry.
const launch = async (browser, port, extraArgs, attempts) => {
  const profile = fs.mkdtempSync(path.join(os.tmpdir(), 'cg-settings-merge-'));
  const stderr = [];
  const chrome = spawn(browser, [...BASE_ARGS, ...extraArgs, `--remote-debugging-port=${port}`, `--user-data-dir=${profile}`, 'about:blank'],
    { stdio:['ignore', 'ignore', 'pipe'] });
  chrome.stderr.on('data', chunk => stderr.push(String(chunk)));
  try {
    await waitForDevTools(port, attempts);
    if (!await pageAnswersOverCDP(port)) throw new Error('devtools port opened but no page target answered a command');
    return { chrome, profile, stderr, port };
  } catch (_) {
    try { chrome.kill('SIGKILL'); } catch (_ignored) {}
    return { chrome:null, profile, stderr, port };
  }
};

const main = async () => {
  const browser = CHROME_CANDIDATES.find(candidate => fs.existsSync(candidate));
  if (!browser) {
    console.log('SKIP: no Chrome/Chromium binary found; set CHROME_BIN to run this acceptance.');
    return;
  }
  step(`browser: ${browser}`);

  const basePort = 9601 + (process.pid % 200);
  let launched = await launch(browser, basePort, [], 40);
  if (!launched.chrome) {
    // Chrome refused to start under its own sandbox. The page under test is a
    // local fixture with no outbound network, so a second attempt without the
    // sandbox is the lesser evil - and it is what makes this runnable in a
    // confined shell at all.
    step('chrome could not start its own sandbox; retrying without it');
    try { fs.rmSync(launched.profile, { recursive:true, force:true }); } catch (_) {}
    launched = await launch(browser, basePort + 2, CONFINED_ARGS, 60);
  }
  const { chrome, profile, stderr: chromeErrors, port: debugPort } = launched;
  if (!chrome) {
    console.error('chrome never opened its devtools port:');
    console.error(chromeErrors.join('').split('\n').slice(-8).join('\n'));
    process.exitCode = 1;
    return;
  }

  const failures = [];
  let fixture = null;
  let send = null;
  const watchdog = setTimeout(() => {
    console.error(`watchdog: acceptance exceeded ${TOTAL_BUDGET_MS}ms`);
    console.error(chromeErrors.join('').split('\n').slice(-10).join('\n'));
    try { chrome.kill('SIGKILL'); } catch (_) {}
    process.exit(1);
  }, TOTAL_BUDGET_MS);

  try {
    await waitForDevTools(debugPort);
    step('devtools reachable');
    fixture = await startFixture();
    const target = await getJSON(`http://127.0.0.1:${debugPort}/json/new?${encodeURIComponent(fixture.origin + '/#settings')}`, { method:'PUT' });
    send = await openSession(target.webSocketDebuggerUrl);
    await send('Runtime.enable');
    const pageErrors = [];
    send.on(message => {
      if (message.method === 'Runtime.exceptionThrown') {
        const details = message.params && message.params.exceptionDetails;
        pageErrors.push((details && (details.exception && details.exception.description || details.text)) || 'unknown exception');
      }
    });

    const driven = await send('Runtime.evaluate', { expression:DRIVER, awaitPromise:true, returnByValue:true }, 60000);
    if (driven.exceptionDetails) {
      throw new Error(`driver threw: ${(driven.exceptionDetails.exception || {}).description || driven.exceptionDetails.text}`);
    }
    const outcome = driven.result && driven.result.value;
    if (!outcome) throw new Error('the console never reported a result');

    for (const line of outcome.steps || []) console.log(`  ${line}`);
    if (outcome.error) console.log(`  driver error: ${outcome.error}`);
    failures.push(...(outcome.failures || []));
    if (pageErrors.length) console.log(`  page errors: ${pageErrors.join(' | ')}`);

    const record = (name, passed, detail = '') => {
      console.log(`${passed ? 'PASS' : 'FAIL'}  ${name}${detail ? ` - ${detail}` : ''}`);
      if (!passed) failures.push(name);
    };
    console.log('');
    record('the merged 状态设置 section renders in a real browser', outcome.passed === true);
    record('the console raised no runtime exception during the run', pageErrors.length === 0, pageErrors.join(' | '));
    record('the rendered tablist is 状态设置 / 运行参数 / 版本更新',
      JSON.stringify((outcome.tabs || []).map(tab => tab.label)) === JSON.stringify(['状态设置', '运行参数', '版本更新']),
      (outcome.tabs || []).map(tab => tab.label).join(' / '));
    record('the keyboard rotation still visits exactly three sections',
      Array.isArray(outcome.rotation) && outcome.rotation.length === 4 && new Set(outcome.rotation).size === 3,
      (outcome.rotation || []).join(' -> '));
  } finally {
    clearTimeout(watchdog);
    try { if (send) send.close(); } catch (_) {}
    try { if (fixture) { for (const item of fixture.sockets) item.destroy(); fixture.server.close(); } } catch (_) {}
    try { chrome.kill('SIGKILL'); } catch (_) {}
    try { if (path.basename(profile).startsWith('cg-settings-merge-')) fs.rmSync(profile, { recursive:true, force:true }); } catch (_) {}
  }

  if (failures.length) {
    console.error(`\n${failures.length} check(s) failed: ${failures.join(', ')}`);
    process.exitCode = 1;
  } else {
    console.log('\nAll settings-merge checks passed.');
  }
};

main().catch(error => {
  console.error(`acceptance failed: ${error.message}`);
  process.exitCode = 1;
});
