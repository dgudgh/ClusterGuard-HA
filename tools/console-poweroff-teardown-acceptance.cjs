// Acceptance: after a host power-off is submitted the console page must settle
// itself. The operator confirms "整机关机", the control plane answers once and
// then dies with the host, and the page closes the shutdown dialog, shows an
// explicit offline screen and closes its own tab - instead of leaving a dialog
// on screen whose API can never answer again.
//
// Two scenarios cover that, because the page really does close itself:
//   1. window.close is stubbed so the run can collect what the operator sees
//      (offline screen text, dialog closed, and the close request itself).
//   2. window.close is real, and the run watches the tab disappear from the
//      browser's own target list.
//
// The whole run is local: tools/console-ui-fixture.cjs serves the real console
// HTML with isolated example data and answers every request in-process. A
// Chrome/Chromium binary is driven over the DevTools protocol with no extra
// dependency, so this stays runnable without installing playwright.
//
//   node tools/console-poweroff-teardown-acceptance.cjs
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

const TOTAL_BUDGET_MS = 120000;

// Shared by every scenario: drive the real dialog, confirm with the exact
// cluster name, and submit the shutdown in the requested mode.
const prelude = mode => `
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
  const result = { steps: [] };
  window.__poweroffAcceptance = result;
  try {
    if (!await until(() => state.clusterDataReady && state.clusterDetail && state.topology, 20000)) {
      throw new Error('cluster evidence never became ready: ' + document.getElementById('live-status').textContent);
    }
    result.steps.push('cluster-evidence-ready');
    document.getElementById('open-power-shutdown').click();
    if (!await until(() => document.getElementById('power-shutdown-dialog').open, 5000)) {
      throw new Error('shutdown dialog never opened');
    }
    result.steps.push('dialog-open');
    document.getElementById('power-mode-${mode}').click();
    const name = document.getElementById('power-confirm-name');
    name.value = document.getElementById('power-confirm-phrase').textContent;
    name.dispatchEvent(new Event('input', { bubbles: true }));
    const acknowledgement = document.getElementById('power-confirm-impact');
    acknowledgement.checked = true;
    acknowledgement.dispatchEvent(new Event('change', { bubbles: true }));
    await until(() => !document.getElementById('confirm-power-shutdown').disabled, 3000);
    if (document.getElementById('confirm-power-shutdown').disabled) {
      throw new Error('confirm button stayed disabled: ' + document.getElementById('power-shutdown-result').textContent);
    }
    document.getElementById('confirm-power-shutdown').click();
    result.steps.push('shutdown-submitted');
  } catch (error) {
    result.error = String((error && error.message) || error);
    result.passed = false;
  }
`;

// Scenario 1: keep the tab alive long enough to read what the operator would
// see, and record that the page asked to be closed.
const OBSERVER_DRIVER = `(async () => {${prelude('poweroff')}
  if (result.error) { result.passed = false; return result; }
  window.close = function () { result.closeRequested = true; };
  if (!await until(() => document.getElementById('power-offline-screen').hidden === false, 25000)) {
    result.error = 'offline screen never appeared: ' + document.getElementById('power-shutdown-result').textContent;
    result.passed = false;
  } else {
    result.steps.push('offline-screen-shown');
    result.passed = true;
  }
  result.dialogOpen = document.getElementById('power-shutdown-dialog').open;
  result.offlineScreenHidden = document.getElementById('power-offline-screen').hidden;
  result.title = document.getElementById('power-offline-title').textContent;
  result.summary = document.getElementById('power-offline-summary').textContent;
  result.detail = document.getElementById('power-offline-detail').textContent;
  result.hint = document.getElementById('power-offline-hint').textContent;
  result.closeRequested = result.closeRequested === true;
  return result;
})()`;

// Scenario 2: leave window.close alone and watch the tab disappear.
const SUBMIT_DRIVER = `(async () => {${prelude('poweroff')}
  result.passed = !result.error;
  return result;
})()`;

// Scenario 3: stopping only the database service must never take the console
// page down - the control plane stays up and the operator keeps working.
const SERVICE_DRIVER = `(async () => {${prelude('service')}
  result.passed = !result.error;
  return result;
})()`;

const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
const step = message => console.log(`[${new Date().toISOString().slice(11, 19)}] ${message}`);

const getJSON = async (url, options = {}) => {
  const response = await fetch(url, { ...options, signal: AbortSignal.timeout(5000) });
  if (!response.ok) throw new Error(`${options.method || 'GET'} ${url} -> ${response.status}`);
  return response.json();
};

const waitForDevTools = async port => {
  for (let attempt = 0; attempt < 100; attempt += 1) {
    try { await getJSON(`http://127.0.0.1:${port}/json/version`); return; }
    catch (_) { await sleep(100); }
  }
  throw new Error('chrome devtools endpoint never became reachable');
};

const connect = url => new Promise((resolve, reject) => {
  const socket = new WebSocket(url);
  const timer = setTimeout(() => reject(new Error('devtools websocket never opened')), 5000);
  socket.addEventListener('open', () => { clearTimeout(timer); resolve(socket); }, { once: true });
  socket.addEventListener('error', () => { clearTimeout(timer); reject(new Error('devtools websocket failed')); }, { once: true });
});

const session = socket => {
  let sequence = 0;
  const pending = new Map();
  socket.addEventListener('message', event => {
    const message = JSON.parse(event.data);
    const handler = pending.get(message.id);
    if (!handler) return;
    pending.delete(message.id);
    if (message.error) handler.reject(new Error(message.error.message));
    else handler.resolve(message.result);
  });
  return (method, params = {}, timeoutMs = 10000) => new Promise((resolve, reject) => {
    sequence += 1;
    pending.set(sequence, { resolve, reject });
    socket.send(JSON.stringify({ id: sequence, method, params }));
    setTimeout(() => { if (pending.delete(sequence)) reject(new Error(`${method} timed out after ${timeoutMs}ms`)); }, timeoutMs);
  });
};

// A console fixture whose control plane dies right after it answers the host
// power-off request, exactly like a control plane that goes down with its host.
const startFixture = () => {
  const fixture = createConsoleFixture();
  const { server, control, clusters } = fixture;
  const cluster = clusters[0];
  if (!cluster) throw new Error('fixture exposed no cluster to shut down');
  const state = { controlPlaneLost: false };
  const sockets = new Set();
  server.on('connection', socket => { sockets.add(socket); socket.on('close', () => sockets.delete(socket)); });
  control.hook = async ({ url }) => {
    if (url.pathname.endsWith('/power/precheck')) return { result: { power_operation: { resource_id: 'power-acceptance', cluster_id: cluster.resource_id, state: 'prechecking', operation_type: 'poweroff' }, blocking_reasons: [], risk: 'low', co_resident_clusters: [] } };
    if (url.pathname.endsWith('/power/plan')) return { result: { power_operation: { resource_id: 'power-acceptance', cluster_id: cluster.resource_id, state: 'shutdown_planned', operation_type: 'poweroff' }, plan_steps: [] } };
    if (url.pathname.endsWith('/power/execute')) {
      setTimeout(() => {
        state.controlPlaneLost = true;
        for (const socket of sockets) socket.destroy();
        server.close();
      }, 200);
      return { result: { power_operation: { resource_id: 'power-acceptance', cluster_id: cluster.resource_id, state: 'power_off', operation_type: 'poweroff' }, operation_record: { resource_id: 'operation-acceptance' }, protection: { recovery_freeze: true, instances_in_maintenance: [] } } };
    }
    return null;
  };
  return new Promise(resolve => server.listen(0, '127.0.0.1', () => resolve({ server, sockets, state, origin: `http://127.0.0.1:${server.address().port}` })));
};

const targetAlive = async (debugPort, targetId) => {
  try {
    const list = await getJSON(`http://127.0.0.1:${debugPort}/json/list`);
    return list.some(target => target.id === targetId);
  } catch (_) { return true; }
};

const runObserverScenario = async (debugPort, failures) => {
  const record = (name, passed, detail = '') => {
    console.log(`${passed ? 'PASS' : 'FAIL'}  ${name}${detail ? ` - ${detail}` : ''}`);
    if (!passed) failures.push(name);
  };
  const fixture = await startFixture();
  let socket = null;
  try {
    const target = await getJSON(`http://127.0.0.1:${debugPort}/json/new?${encodeURIComponent(fixture.origin + '/#topology')}`, { method: 'PUT' });
    socket = await connect(target.webSocketDebuggerUrl);
    const send = session(socket);
    await send('Runtime.enable');
    const driven = await send('Runtime.evaluate', { expression: OBSERVER_DRIVER, awaitPromise: true, returnByValue: true }, 60000);
    if (driven.exceptionDetails) throw new Error(`driver threw: ${(driven.exceptionDetails.exception || {}).description || driven.exceptionDetails.text}`);
    const outcome = driven.result && driven.result.value;
    if (!outcome) throw new Error('the console never reported a result');
    console.log(`steps: ${(outcome.steps || []).join(' -> ') || 'none'}`);
    if (outcome.error) console.log(`driver error: ${outcome.error}`);
    record('host power-off reached the offline screen', outcome.passed === true && outcome.steps.includes('offline-screen-shown'));
    record('the shutdown dialog closed itself', outcome.dialogOpen === false, `dialogOpen=${outcome.dialogOpen}`);
    record('the offline screen names the shutdown', /关机/.test(outcome.title || ''), outcome.title);
    record('the offline screen reports what was submitted', /关机/.test(outcome.summary || ''), outcome.summary);
    record('the offline screen states the control plane is gone', /控制面/.test(outcome.detail || ''), outcome.detail);
    record('the remaining tab explains how to close it', /关闭该标签页|关闭本页/.test(outcome.hint || ''), outcome.hint);
    record('the page asked the browser to close it', outcome.closeRequested === true);
    record('the control plane really went down during the run', fixture.state.controlPlaneLost === true);
  } finally {
    try { if (socket) socket.close(); } catch (_) {}
    try { for (const socket of fixture.sockets) socket.destroy(); } catch (_) {}
    try { fixture.server.close(); } catch (_) {}
  }
};

const runCloseScenario = async (debugPort, failures) => {
  const record = (name, passed, detail = '') => {
    console.log(`${passed ? 'PASS' : 'FAIL'}  ${name}${detail ? ` - ${detail}` : ''}`);
    if (!passed) failures.push(name);
  };
  const fixture = await startFixture();
  let socket = null;
  let target = null;
  try {
    target = await getJSON(`http://127.0.0.1:${debugPort}/json/new?${encodeURIComponent(fixture.origin + '/#topology')}`, { method: 'PUT' });
    socket = await connect(target.webSocketDebuggerUrl);
    const send = session(socket);
    await send('Runtime.enable');
    const driven = await send('Runtime.evaluate', { expression: SUBMIT_DRIVER, awaitPromise: true, returnByValue: true }, 40000);
    const outcome = driven.result && driven.result.value;
    if (outcome && outcome.error) console.log(`driver error: ${outcome.error}`);
    record('host power-off was submitted from the real dialog', !!outcome && outcome.passed === true);
    let gone = false;
    for (let attempt = 0; attempt < 80; attempt += 1) {
      if (!await targetAlive(debugPort, target.id)) { gone = true; break; }
      await sleep(250);
    }
    record('the browser tab closed itself without any operator action', gone);
    record('the control plane really went down during the run', fixture.state.controlPlaneLost === true);
  } finally {
    try { if (socket) socket.close(); } catch (_) {}
    try { if (target) await fetch(`http://127.0.0.1:${debugPort}/json/close/${target.id}`, { signal: AbortSignal.timeout(3000) }); } catch (_) {}
    try { for (const socket of fixture.sockets) socket.destroy(); } catch (_) {}
    try { fixture.server.close(); } catch (_) {}
  }
};

// Scenario 3: stopping only the database service must never take the console
// page down, even though the same fixture kills the control plane after the
// shutdown is submitted. The console stays up and keeps the operator working.
const runServiceScenario = async (debugPort, failures) => {
  const record = (name, passed, detail = '') => {
    console.log(`${passed ? 'PASS' : 'FAIL'}  ${name}${detail ? ` - ${detail}` : ''}`);
    if (!passed) failures.push(name);
  };
  const fixture = await startFixture();
  let socket = null;
  let target = null;
  try {
    target = await getJSON(`http://127.0.0.1:${debugPort}/json/new?${encodeURIComponent(fixture.origin + '/#topology')}`, { method: 'PUT' });
    socket = await connect(target.webSocketDebuggerUrl);
    const send = session(socket);
    await send('Runtime.enable');
    const driven = await send('Runtime.evaluate', { expression: SERVICE_DRIVER, awaitPromise: true, returnByValue: true }, 40000);
    const outcome = driven.result && driven.result.value;
    if (outcome && outcome.error) console.log(`driver error: ${outcome.error}`);
    record('a service-only shutdown was submitted from the real dialog', !!outcome && outcome.passed === true);
    let survived = true;
    for (let attempt = 0; attempt < 32; attempt += 1) {
      if (!await targetAlive(debugPort, target.id)) { survived = false; break; }
      await sleep(250);
    }
    record('a service-only shutdown keeps the console page open', survived);
    const probe = await send('Runtime.evaluate', { expression: 'JSON.stringify({ screenHidden: document.getElementById("power-offline-screen").hidden })', returnByValue: true });
    const view = JSON.parse((probe.result && probe.result.value) || '{}');
    record('a service-only shutdown never shows the offline screen', view.screenHidden === true, JSON.stringify(view));
  } finally {
    try { if (socket) socket.close(); } catch (_) {}
    try { if (target) await fetch(`http://127.0.0.1:${debugPort}/json/close/${target.id}`, { signal: AbortSignal.timeout(3000) }); } catch (_) {}
    try { for (const socket of fixture.sockets) socket.destroy(); } catch (_) {}
    try { fixture.server.close(); } catch (_) {}
  }
};

const main = async () => {
  const browser = CHROME_CANDIDATES.find(candidate => fs.existsSync(candidate));
  if (!browser) {
    console.log('SKIP: no Chrome/Chromium binary found; set CHROME_BIN to run this acceptance.');
    return;
  }
  step(`browser: ${browser}`);

  const debugPort = 9333 + (process.pid % 200);
  const profile = fs.mkdtempSync(path.join(os.tmpdir(), 'cg-poweroff-acceptance-'));
  const chromeErrors = [];
  const chrome = spawn(browser, ['--headless=new', `--remote-debugging-port=${debugPort}`, `--user-data-dir=${profile}`,
    '--no-first-run', '--no-default-browser-check', '--disable-extensions', 'about:blank'], { stdio: ['ignore', 'ignore', 'pipe'] });
  chrome.stderr.on('data', chunk => chromeErrors.push(String(chunk)));

  const failures = [];
  const watchdog = setTimeout(() => {
    console.error(`watchdog: acceptance exceeded ${TOTAL_BUDGET_MS}ms`);
    console.error(chromeErrors.join('').split('\n').slice(-10).join('\n'));
    try { chrome.kill('SIGKILL'); } catch (_) {}
    process.exit(1);
  }, TOTAL_BUDGET_MS);

  try {
    await waitForDevTools(debugPort);
    step('devtools reachable');
    step('scenario 1: observe the offline screen with window.close intercepted');
    await runObserverScenario(debugPort, failures);
    step('scenario 2: leave window.close alone and watch the tab disappear');
    await runCloseScenario(debugPort, failures);
    step('scenario 3: a service-only shutdown must keep the console');
    await runServiceScenario(debugPort, failures);
  } finally {
    clearTimeout(watchdog);
    try { chrome.kill('SIGKILL'); } catch (_) {}
    // Only ever remove the throwaway profile this run created itself.
    try { if (path.basename(profile).startsWith('cg-poweroff-acceptance-')) fs.rmSync(profile, { recursive: true, force: true }); } catch (_) {}
  }

  if (failures.length) {
    console.error(`\n${failures.length} check(s) failed: ${failures.join(', ')}`);
    process.exitCode = 1;
  } else {
    console.log('\nAll host power-off teardown checks passed.');
  }
};

main().catch(error => {
  console.error(`acceptance failed: ${error.message}`);
  process.exitCode = 1;
});
