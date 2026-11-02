// Opt-in real browser + real SQL acceptance. No fixture API or request routing.
// NODE_PATH=<runtime modules> node tools/console-gateway-real-mysql.cjs LAB_ROOT
const {chromium} = require('playwright');
const fs = require('node:fs');
const path = require('node:path');
const {spawn} = require('node:child_process');
const assert = require('node:assert/strict');
const root = path.resolve(process.argv[2]);
const m = JSON.parse(fs.readFileSync(path.join(root, 'manifest.json')));
const s = JSON.parse(fs.readFileSync(path.join(root, 'secrets.json')));
const mysql = process.env.CG_TEST_MYSQL || '/opt/homebrew/bin/mysql';
assert.equal(new URL(m.base_url).hostname,'127.0.0.1','lab only accepts loopback control plane');
for(const port of [...m.db_ports,...m.gateway_frontends]) assert.ok(Number.isInteger(port)&&port>1024&&port<=65535);
const sleep = ms => new Promise(r => setTimeout(r, ms));
const sessions = new Set();
const previous=path.join(root,'browser-real-result.json');
if(fs.existsSync(previous)) fs.renameSync(previous,path.join(root,`browser-real-result-before-${Date.now()}.json`));
const evidence = {scope:m.scope, operations:[], writes:[], errors:[], started:new Date().toISOString()};
const save = () => fs.writeFileSync(path.join(root, 'browser-real-result.json'), JSON.stringify(evidence,null,2));
function query(port, sql, user='cg_app') {
 const file = path.join(root, `client-${port}-${user}.cnf`);
 fs.writeFileSync(file, `[client]\nuser=${user}\npassword=${s.db}\nhost=127.0.0.1\nport=${port}\nprotocol=TCP\nconnect_timeout=2\n`, {mode:0o600});
 return new Promise(resolve => {
  const p=spawn(mysql,[`--defaults-file=${file}`,'-NBr','--skip-reconnect']);
  let out='',err=''; const timer=setTimeout(()=>p.kill(),6000);
  p.stdout.on('data',x=>out+=x);p.stderr.on('data',x=>err+=x);
  p.on('close',code=>{clearTimeout(timer);resolve({code,out:out.trim(),err:err.trim()})});
  p.stdin.end(sql);
 });
}
async function openSession(port) {
 const file=path.join(root,`client-${port}-cg_app.cnf`);
 const p=spawn(mysql,[`--defaults-file=${file}`,'-NBr','--unbuffered','--skip-reconnect']);
 sessions.add(p);
 let out='',err='',exit=null;
 p.stdout.on('data',x=>out+=x);p.stderr.on('data',x=>err+=x);
 p.on('close',code=>exit=code);
 p.stdin.on('error',()=>{});
 const execute=async marker=>{
  p.stdin.write(`SELECT '${marker}';\n`);
  for(let n=0;n<100;n++){
   if(out.includes(marker))return true;
   if(exit!==null || err)return false;
   await sleep(50);
  }
  throw Error('real SQL session did not respond or close');
 };
 assert.equal(await execute('session-before-switch'),true);
 return {execute,close:()=>{p.kill();sessions.delete(p)},state:()=>({out,err,exit})};
}
(async()=>{
 const browser=await chromium.launch({channel:'chrome',headless:true});
 const page=await browser.newPage({viewport:{width:1440,height:1000}});
 let writing=false, worker;
 page.on('pageerror', e=>evidence.errors.push(e.message));
 page.on('response', async r=>{
  if (new URL(r.url()).pathname==='/api/v1/operations/execute') {
   try { evidence.operations.push({http:r.status(),body:await r.json()});save() } catch(e) {evidence.errors.push(e.message)}
  }
 });
 try {
  await page.goto(`${m.base_url}/#operations`);
  const login = async password => {
   await page.locator('#login-username').fill('admin');
   await page.locator('#login-password').fill(password);
   await page.locator('#login-submit').click();
  };
  await login(process.env.CG_LAB_PASSWORD_CHANGED ? s.password : s.bootstrap);
  if(!process.env.CG_LAB_PASSWORD_CHANGED){
   await page.locator('#password-modal[open]').waitFor();
   await page.locator('#current-password').fill(s.bootstrap);
   await page.locator('#new-password').fill(s.password);
   await page.locator('#confirm-new-password').fill(s.password);
   await page.locator('#save-password').click();
   await page.locator('#login-submit').waitFor({state:'visible'});
   await login(s.password);
  }
  await page.waitForFunction(()=>state.selectedClusterId && state.clusterDetail && state.gatewayRoute?.ready, null, {timeout:30000});
  assert.equal(await page.locator('#execute-switchover').isDisabled(),true);
  await page.screenshot({path:path.join(root,'real-before.png'),fullPage:true});
  const first=m.instances.find(x=>x.port===m.db_ports[0]);
  const other=m.instances.find(x=>x.port===m.db_ports[1]);
  for(const port of m.gateway_frontends){
   const r=await query(port,'SELECT @@server_uuid;'); assert.equal(r.code,0,r.err);
   assert.equal(r.out,first.engine_identity.server_uuid);
  }
  writing=true;
  worker=(async()=>{
   let n=0;
   while(writing){
    const id=`real-${Date.now()}-${++n}`, started=Date.now();
    const r=await query(m.gateway_frontends[0],`INSERT INTO cg_acceptance.writes VALUES ('${id}',${n}); SELECT @@server_uuid;`);
    evidence.writes.push({id,started,ended:Date.now(),acknowledged:r.code===0,server:r.out,error:r.err});
    save();await sleep(100);
   }
  })();
  await sleep(1500);
  for(const target of [other,first]){
   const session=await openSession(m.gateway_frontends[0]);
   await page.locator('#candidate-select').selectOption(target.resource_id);
   await page.locator('#switch-lock').click();
   await page.locator('#execute-switchover').waitFor({state:'visible'});
   assert.equal(await page.locator('#execute-switchover').isEnabled(),true,await page.locator('#operation-result').textContent());
   const started=Date.now();
   await page.locator('#execute-switchover').click();
   await page.waitForFunction(()=>!state.operationRunning,null,{timeout:120000});
   const op=evidence.operations.at(-1);
   assert.ok(op,'actual execute response missing');
   assert.equal(op.body.result?.status,'succeeded',JSON.stringify(op));
   assert.equal(op.body.result?.verification?.passed,true,JSON.stringify(op));
   assert.equal(await session.execute(`old-session-after-${target.port}`),false,'old database session survived switch');
   op.old_session=session.state();session.close();
   for(const port of m.gateway_frontends){
    const r=await query(port,'SELECT @@server_uuid;');assert.equal(r.code,0,r.err);
    assert.equal(r.out,target.engine_identity.server_uuid);
   }
   const states=[];
   for(const port of m.db_ports){
    const r=await query(port,'SELECT @@server_uuid,@@read_only,@@super_read_only;','cg_lab');
    assert.equal(r.code,0,r.err);states.push(r.out);
   }
   assert.equal(states.filter(x=>x.endsWith('\t0\t0')).length,1,JSON.stringify(states));
   op.actual_sql={states,elapsed_ms:Date.now()-started};save();
   await page.screenshot({path:path.join(root,`real-after-${target.port}.png`),fullPage:true});
   await sleep(6000);
  }
  await sleep(1500);writing=false;await worker;
  const ack=evidence.writes.filter(x=>x.acknowledged);
  assert.ok(ack.length>10,'too few confirmed writes');
  for(const port of m.db_ports){
   const r=await query(port,`SELECT COUNT(*) FROM cg_acceptance.writes WHERE id IN (${ack.map(x=>`'${x.id}'`).join(',')});`);
   assert.equal(r.code,0,r.err);assert.equal(Number(r.out),ack.length,'confirmed writes missing on replica');
  }
  const gaps=[];
  for(let i=1;i<ack.length;i++)gaps.push(ack[i].ended-ack[i-1].ended);
  evidence.summary={confirmed_writes:ack.length,failed_or_unknown:evidence.writes.length-ack.length,
   max_confirmed_write_gap_ms:Math.max(...gaps),round_trips:1,real_mysql:true,real_raft:true,real_browser:true};
  assert.deepEqual(evidence.errors,[]);evidence.passed=true;
  console.log(JSON.stringify(evidence.summary));
 } catch(e) {
  evidence.failure=e.stack;
  await page.screenshot({path:path.join(root,'real-failure.png'),fullPage:true}).catch(()=>{});
  console.error(e.stack);process.exitCode=1;
 } finally {
  writing=false;if(worker)await worker;for(const session of sessions)session.kill();save();await browser.close();
 }
})();
