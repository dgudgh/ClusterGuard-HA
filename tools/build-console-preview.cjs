const fs = require('node:fs');
const path = require('node:path');
const { createConsoleFixture, operationFixturePage } = require('./console-ui-fixture.cjs');

// Snapshot only isolated fixture responses. The generated file has no network fallback.
async function buildPreview(destination = path.resolve(__dirname, '../preview/console-redesign/index.html')) {
  const fixture = createConsoleFixture();
  await new Promise(resolve => fixture.server.listen(0, '127.0.0.1', resolve));
  try {
    const paths = ['/auth/me', '/clusters', '/nodes', '/capabilities', '/operations', '/operations?view=summary', '/operations?view=context',
      '/nodes/sync/tasks', '/nodes/sync/capabilities', '/control-plane/status', '/platform/version', '/platform/updates'];
    for (const cluster of fixture.clusters) {
      const prefix = `/clusters/${cluster.resource_id}`;
      paths.push(prefix, ...['topology', 'health', 'candidates', 'metrics', 'power/status', 'recovery/status'].map(tail => `${prefix}/${tail}`));
      paths.push(`/operations?cluster_id=${cluster.resource_id}&view=summary`);
      paths.push(`/operations?cluster_id=${cluster.resource_id}&view=context`);
    }
    for (const operation of fixture.operations) paths.push(`/operations/${operation.resource_id}`);
    const responses = {};
    for (const route of paths) {
      const url = new URL('/api/v1' + route, `http://127.0.0.1:${fixture.server.address().port}`);
      const response = await fetch(url);
      url.searchParams.sort();
      responses[url.pathname + url.search] = await response.json();
    }
    responses['/api/v1/nodes/sync/capabilities'] = { status:'ok', result:{ available:true, reason:'仅本地界面预览', capabilities:{ clone_available:true, postgresql_basebackup_available:true, postgresql_rewind_available:true } } };
    responses['/api/v1/platform/version'].result = { version:'2.2', release:'91', architecture:'x86_64' };
    responses['/api/v1/platform/updates'].result.current_version = '2.2-91';
    const payload = JSON.stringify(responses).replace(/</g, '\\u003c');
    const adapter = `<script>
      (() => {
        const responses = ${payload};
        const operationPage = ${operationFixturePage.toString()};
        window.__consolePreview = { offline:true, requests:[], blockedWrites:0 };
        window.fetch = async (input, init = {}) => {
          if (init.signal?.aborted) throw new DOMException('Aborted', 'AbortError');
          const request = input instanceof Request ? input : null;
          const url = new URL(request ? request.url : String(input), 'https://preview.invalid');
          url.searchParams.delete('observation_id');
          url.searchParams.sort();
          const method = (init.method || request?.method || 'GET').toUpperCase();
          const key = url.pathname + url.search;
          window.__consolePreview.requests.push({ method, path:key });
          let status = 200, body = responses[key];
          if (method !== 'GET') {
            window.__consolePreview.blockedWrites++;
            status = 403;
            body = { status:'error', message:'本地设计预览：不会提交数据库、升级或账户操作。' };
          } else if (url.pathname === '/api/v1/operations' && url.searchParams.get('view') === 'page') {
            body = { status:'ok', result:operationPage(responses['/api/v1/operations'].result, url, responses['/api/v1/clusters'].result) };
          } else if (!body) {
            status = 404;
            body = { status:'error', message:'本地预览未提供此操作的执行证据。' };
          }
          return new Response(JSON.stringify(body), { status, headers:{ 'Content-Type':'application/json' } });
        };
      })();
    </script>`;
    const source = fs.readFileSync(path.resolve(__dirname, '../internal/api/console.html'), 'utf8');
    const html = source.replace('<head>', `<head>\n  <meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:; connect-src 'none'; form-action 'none'; base-uri 'none'">`)
      .replace('</head>', '<style>.brand .brand-subtitle { display:block; }</style></head>')
      .replace('<body>', '<body>\n' + adapter)
      .replace('<p class="eyebrow">ClusterGuard HA Console</p>', '<p class="eyebrow">本地设计预览 · 示例数据 · 不连接现场</p>')
      .replace('<div class="brand-subtitle">Multi-DB HA Control</div>', '<div class="brand-subtitle">本地预览 · 示例数据</div>');
    fs.mkdirSync(path.dirname(destination), { recursive:true });
    fs.writeFileSync(destination, html);
    return { destination, bytes:Buffer.byteLength(html), routes:Object.keys(responses).length, externalNetwork:false };
  } finally {
    fixture.server.closeAllConnections();
    await new Promise(resolve => fixture.server.close(resolve));
  }
}

module.exports = { buildPreview };
if (require.main === module) buildPreview(process.argv[2]).then(result => console.log(JSON.stringify(result))).catch(error => { console.error(error); process.exitCode = 1; });
