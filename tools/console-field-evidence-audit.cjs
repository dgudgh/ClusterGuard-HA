const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const { chromium } = require('playwright');
const { createConsoleFixture } = require('./console-ui-fixture.cjs');

// Replay sanitized field responses locally. Never proxy browser mutations to a host.
(async () => {
  const root = path.resolve(process.env.FIELD_AUDIT_OUTPUT || '.build/end-to-end-audit-20260910/field');
  const source = JSON.parse(fs.readFileSync(path.join(root, '152-api.json'))).result;
  const fixture = createConsoleFixture();
  fixture.clusters.splice(0, fixture.clusters.length, ...source.clusters.body.result);
  const pg = fixture.clusters.find(c => c.engine === 'postgresql');
  const mysql = fixture.clusters.find(c => c.engine === 'mysql');
  const counts = { pgCandidates:0 };
  fixture.control.hook = ({url}) => {
    const route = url.pathname.replace('/api/v1/', '');
    if (route === `clusters/${pg.resource_id}/candidates`) {
      counts.pgCandidates++;
      return { status:409, message:'candidate evaluation requires exactly one current primary' };
    }
    if (source[route]) return {status:source[route].http, result:source[route].body?.result, message:source[route].body?.message};
    return null;
  };
  await new Promise(resolve => fixture.server.listen(0, '127.0.0.1', resolve));
  const browser = await chromium.launch({channel:'chrome',headless:true});
  const checks = [];
  try {
    for (const width of [1440,390]) {
      const page = await browser.newPage({viewport:{width,height:1000}});
      await page.goto(`http://127.0.0.1:${fixture.server.address().port}/#topology`);
      await page.waitForFunction(() => state.clusterDataReady);
      counts.pgCandidates = 0;
      const started = Date.now();
      await page.locator('#cluster-select').selectOption(pg.resource_id);
      await page.waitForFunction(() => !state.clusterLoading && state.topology?.instances?.length === 3);
      const value = await page.evaluate(() => ({loading:state.clusterLoading, ready:state.clusterDataReady,
        links:state.topology.links.length, health:state.topology.health.state, candidates:state.candidates.length,
        locked:!state.switchUnlocked, executeDisabled:byId('execute-switchover').disabled,
        overflow:document.documentElement.scrollWidth > innerWidth + 1}));
      assert.deepEqual(value, {loading:false,ready:false,links:0,health:'degraded',candidates:0,locked:true,executeDisabled:true,overflow:false});
      assert.equal(counts.pgCandidates, 1, 'missing primary must not trigger observation retry loop');
      checks.push({width,scenario:'field PG has no primary',passed:true,seconds:(Date.now()-started)/1000,...value});
      await page.screenshot({path:path.join(root, `pg-no-primary-${width}.png`)});
      await page.locator('#cluster-select').selectOption(mysql.resource_id);
      await page.waitForFunction(() => state.clusterDataReady && state.topology?.links?.length === 2);
      assert.equal(fixture.control.mutations,0);
      checks.push({width,scenario:'switch back to healthy MySQL',passed:true});
      await page.close();
    }
    fs.writeFileSync(path.join(root,'browser-replay.json'),JSON.stringify({status:'passed',scope:'local browser replay of sanitized field responses; not live browser deployment',checks,mutations:fixture.control.mutations},null,2));
    console.log(JSON.stringify({checks,mutations:fixture.control.mutations}));
  } finally {
    await browser.close();
    await new Promise(resolve => fixture.server.close(resolve));
  }
})().catch(error => {console.error(error);process.exitCode=1;});
