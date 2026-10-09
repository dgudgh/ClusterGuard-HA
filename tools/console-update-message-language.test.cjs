const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const root = path.resolve(__dirname,'..');
const page = fs.readFileSync(path.join(root,'internal/api/console.html'),'utf8');
const start=page.indexOf('    const softwareUpdateMessageText = message => {');
const source=page.slice(start,page.indexOf('    const renderSoftwareUpdateMessage =',start));
const format=(message,language='zh-CN')=>vm.runInNewContext(`${source}\nsoftwareUpdateMessageText(message);`,{state:{language},message});

test('every fixed Runner journal message has an explicit Chinese translation',()=>{
  const script=fs.readFileSync(path.join(root,'scripts/clusterguard-upgrade.sh'),'utf8');
  const messages=[...new Set([...script.matchAll(/write_journal\s+\S+\s+(?:"[^"]*"|\S+)\s+"([^"]+)"/g)].map(m=>m[1]))];
  assert.ok(messages.length>=30,'Runner journal extraction must cover progress, completion and rollback');
  for(const message of messages){
    const result=format(message);
    assert.notEqual(result,message,message);
    assert.match(result,/[\u3400-\u9fff]/,message);
    assert.ok(!result.includes('未识别'),message+' missing explicit translation');
    assert.equal(format(message,'en-US'),message);
  }
});
test('existing Chinese is preserved and unknown diagnosis is not guessed as success',()=>{
  const chinese='热修补丁完成，全部节点与控制面已验证，维护门禁已释放';
  assert.equal(format(chinese),chinese);
  assert.equal(format(''), '');
  assert.equal(format('unknown failure code=E42'),'升级器返回了未识别的诊断信息，请展开原始信息核对');
  for(const name of ['constructor','toString','__proto__'])assert.equal(format(name),'升级器返回了未识别的诊断信息，请展开原始信息核对');
  assert.match(format('automatic rollback incomplete; maintenance gate retained'),/未完成.*门禁仍保留/);
  assert.match(format('rollback succeeded but maintenance release failed'),/门禁释放失败/);
});
