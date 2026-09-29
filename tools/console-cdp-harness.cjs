// Shared harness for real-browser console acceptance: boots the actual console HTML
// from tools/console-ui-fixture.cjs, drives it over the DevTools protocol with no
// third-party dependency, and hands the caller back whatever the in-page driver
// returned.
//
//   const { runConsoleDriver } = require('./console-cdp-harness.cjs');
//   const outcome = await runConsoleDriver({ fixture, driver:DRIVER, hash:'#settings' });
//   if (!outcome) return;                       // no browser on this machine -> SKIP
//   outcome.outcome  -> the driver's resolved value
//   outcome.pageErrors -> Runtime.exceptionThrown descriptions seen during the run
//
// Set CHROME_BIN to point at a specific browser.

const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawn } = require('node:child_process');

const CHROME_CANDIDATES = [
  process.env.CHROME_BIN,
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  '/Applications/Chromium.app/Contents/MacOS/Chromium',
  '/usr/bin/google-chrome',
  '/usr/bin/chromium',
  '/usr/bin/chromium-browser',
].filter(Boolean);

const DEFAULT_BUDGET_MS = 90000;

// A confined or containerised shell cannot always give Chrome the namespaces it
// wants, and Chrome exits with "Failed to initialize sandbox / GPU process
// isn't usable" before the DevTools port ever opens - which reads as a hang
// unless the launch is retried without the sandbox. The retry is deliberately a
// second attempt rather than the default, so an unconfined machine keeps the
// browser's own sandbox.
const BASE_ARGS = ['--headless=new', '--no-first-run', '--no-default-browser-check', '--disable-extensions'];
const CONFINED_ARGS = ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'];

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

const startFixture = fixture => {
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
const launch = async (browser, port, extraArgs, attempts, profilePrefix) => {
  const profile = fs.mkdtempSync(path.join(os.tmpdir(), profilePrefix));
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

// Resolves to null when the machine has no browser, so a caller can report SKIP
// instead of failing a machine that cannot run this kind of check.
const runConsoleDriver = async ({ fixture, driver, hash = '', profilePrefix = 'cg-console-', timeoutMs = DEFAULT_BUDGET_MS }) => {
  const browser = CHROME_CANDIDATES.find(candidate => fs.existsSync(candidate));
  if (!browser) {
    console.log('SKIP: no Chrome/Chromium binary found; set CHROME_BIN to run this acceptance.');
    return null;
  }
  step(`browser: ${browser}`);

  const basePort = 9601 + (process.pid % 200);
  let launched = await launch(browser, basePort, [], 40, profilePrefix);
  if (!launched.chrome) {
    // Chrome refused to start under its own sandbox. The page under test is a
    // local fixture with no outbound network, so a second attempt without the
    // sandbox is the lesser evil - and it is what makes this runnable in a
    // confined shell at all.
    step('chrome could not start its own sandbox; retrying without it');
    try { fs.rmSync(launched.profile, { recursive:true, force:true }); } catch (_) {}
    launched = await launch(browser, basePort + 2, CONFINED_ARGS, 60, profilePrefix);
  }
  const { chrome, profile, stderr: chromeErrors, port: debugPort } = launched;
  if (!chrome) {
    console.error('chrome never opened its devtools port:');
    console.error(chromeErrors.join('').split('\n').slice(-8).join('\n'));
    process.exitCode = 1;
    return null;
  }

  let started = null;
  let send = null;
  const watchdog = setTimeout(() => {
    console.error(`watchdog: acceptance exceeded ${timeoutMs}ms`);
    console.error(chromeErrors.join('').split('\n').slice(-10).join('\n'));
    try { chrome.kill('SIGKILL'); } catch (_) {}
    process.exit(1);
  }, timeoutMs);

  try {
    await waitForDevTools(debugPort);
    step('devtools reachable');
    started = await startFixture(fixture);
    const target = await getJSON(`http://127.0.0.1:${debugPort}/json/new?${encodeURIComponent(started.origin + hash)}`, { method:'PUT' });
    send = await openSession(target.webSocketDebuggerUrl);
    await send('Runtime.enable');
    const pageErrors = [];
    send.on(message => {
      if (message.method === 'Runtime.exceptionThrown') {
        const details = message.params && message.params.exceptionDetails;
        pageErrors.push((details && (details.exception && details.exception.description || details.text)) || 'unknown exception');
      }
    });

    const driven = await send('Runtime.evaluate', { expression:driver, awaitPromise:true, returnByValue:true }, 60000);
    if (driven.exceptionDetails) {
      throw new Error(`driver threw: ${(driven.exceptionDetails.exception || {}).description || driven.exceptionDetails.text}`);
    }
    const outcome = driven.result && driven.result.value;
    if (!outcome) throw new Error('the console never reported a result');
    return { outcome, pageErrors };
  } finally {
    clearTimeout(watchdog);
    try { if (send) send.close(); } catch (_) {}
    try { if (started) { for (const item of started.sockets) item.destroy(); started.server.close(); } } catch (_) {}
    try { chrome.kill('SIGKILL'); } catch (_) {}
    try { if (path.basename(profile).startsWith(profilePrefix)) fs.rmSync(profile, { recursive:true, force:true }); } catch (_) {}
  }
};

module.exports = { CHROME_CANDIDATES, DEFAULT_BUDGET_MS, getJSON, launch, openSession, runConsoleDriver, sleep, startFixture, step };
