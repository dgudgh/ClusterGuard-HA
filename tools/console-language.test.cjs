const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs'),vm=require('node:vm');
const page=fs.readFileSync(require('node:path').join(__dirname,'../internal/api/console.html'),'utf8');
const start=page.indexOf('    const uiCatalog = '),end=page.indexOf('    const staticUILanguageBindings =',start);
const context={document:{documentElement:{lang:'en-US'}},state:{}};vm.createContext(context);
vm.runInContext(page.slice(start,end)+'\nglobalThis.catalog=uiCatalog;globalThis.ui=ui;globalThis.localize=localizeUIMessage;',context);
test('every authored UI entry has English and identical interpolation slots',()=>{
 assert.ok(Object.keys(context.catalog).length>=1000,'full console catalogue required');
 const slots=s=>[...s.matchAll(/\{\d+\}/g)].map(m=>m[0]).sort();
 for(const [source,english] of Object.entries(context.catalog)){
  assert.ok(english.trim(),source);assert.doesNotMatch(english,/[\u3400-\u9fff]/,source);
  assert.deepEqual(slots(source),slots(english),source);
 }
});
test('interpolation preserves user values, markup and braces literally',()=>{
 const raw='管理员 <img src=x> {1}';assert.equal(context.ui('配置文件：{0}',raw),'Configuration file: '+raw);
 context.document.documentElement.lang='zh-CN';assert.equal(context.ui('配置文件：{0}',raw),'配置文件：'+raw);
});
test('owned cached prompts switch both ways, unknown diagnostics remain intact',()=>{
 context.document.documentElement.lang='en-US';assert.equal(context.localize('集群状态已更新。'),'Cluster status updated.');
 assert.equal(context.localize('保存失败：native E42'),'Save failed: native E42');
 context.document.documentElement.lang='zh-CN';assert.equal(context.localize('Save failed: native E42'),'保存失败：native E42');
 assert.equal(context.localize('unknown database diagnostic <script>'),'unknown database diagnostic <script>');
});
test('the actual backend approval note and file warning follow both UI languages',()=>{
 const runtime=fs.readFileSync(require('node:path').join(__dirname,'../internal/runtime/configuration_view.go'),'utf8');
 const expression=runtime.match(/value\.Note = ([\s\S]*?)\n\treturn value/);
 assert.ok(expression,'approval-token note must exist');
 const note=[...expression[1].matchAll(/"(?:[^"\\]|\\.)*"/g)].map(match=>JSON.parse(match[0])).join('');
 context.document.documentElement.lang='en-US';
 const english=context.localize(note);
 assert.doesNotMatch(english,/[\u3400-\u9fff]/);
 assert.match(english,/Deprecated/);assert.match(english,/node lifecycle/);assert.match(english,/empty/);assert.match(english,/rejected/);
 const reason='open /etc/clusterguard/clusterguard.json: permission denied <literal> {0}';
 const warning='配置文件当前不可读（'+reason+'）；以下为进程启动时已生效的值。';
 const translated=context.localize(warning);
 assert.ok(translated.startsWith('The configuration file is currently unreadable'));
 assert.ok(translated.includes(reason));
 context.document.documentElement.lang='zh-CN';
 assert.equal(context.localize(english),note);assert.equal(context.localize(translated),warning);
});
test('static authored markup has catalogue coverage, including accessibility attributes',()=>{
 const markup=page.slice(page.indexOf('<body>'),page.indexOf('  <script>'));
 for(const m of markup.matchAll(/>([^<]+)</g)){
  const value=m[1].trim();if(/[\u3400-\u9fff]/.test(value)&&value!=='中文')assert.ok(Object.hasOwn(context.catalog,value),value);
 }
 for(const m of markup.matchAll(/(?:title|placeholder|aria-label)="([^"]+)"/g))if(/[\u3400-\u9fff]/.test(m[1]))assert.ok(Object.hasOwn(context.catalog,m[1]),m[1]);
});
