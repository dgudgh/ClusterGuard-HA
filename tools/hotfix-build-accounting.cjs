// A retained port's Git identity can enter a merge after its identical runtime
// change already reached the field baseline. Prove that fact explicitly; never
// relabel that historical change as a newly delivered fix.
'use strict';
const {execFileSync} = require('node:child_process');
const components = require('../scripts/hotfix-component-map.cjs');

function account(root, base, build, fixes, history = []) {
  const git = (...args) => execFileSync('git', ['-C', root, ...args], {encoding:'utf8'}).trim();
  const full = ref => git('rev-parse', `${ref}^{commit}`);
  const ancestor = (a,b) => { try {git('merge-base','--is-ancestor',a,b);return true;} catch{return false;} };
  base=full(base);build=full(build);
  const resolver=components.resolver(root);
  const files=commit => git('diff','--name-only',`${commit}^..${commit}`).split('\n').filter(f=>resolver.isProductionPath(f));
  const runtimeFiles=commit => files(commit).filter(f=>!f.endsWith('_test.go'));
  const patch=commit => {
    const paths=runtimeFiles(commit);
    if(!paths.length) throw Error('baseline history has no runtime change');
    const diff=git('diff','--no-ext-diff','--no-renames','--unified=0',`${commit}^`,commit,'--',...paths);
    return execFileSync('git',['patch-id','--stable'],{cwd:root,input:diff,encoding:'utf8'}).trim().split(/\s+/)[0];
  };
  if(!Array.isArray(history)) throw Error('baseline_history must be an array');
  const retained=new Set();
  for(const item of history) {
    if(!item || Object.keys(item).sort().join(',')!=='commit,original') throw Error('invalid baseline history fields');
    const commit=full(item.commit), original=full(item.original);
    if(retained.has(commit) || !ancestor(original,base) || !ancestor(commit,build) || ancestor(base,commit) || ancestor(commit,base)) throw Error('invalid baseline history ancestry');
    if(git('rev-list','--parents','-n','1',commit).split(/\s+/).length!==2 || git('rev-list','--parents','-n','1',original).split(/\s+/).length!==2) throw Error('baseline history must name individual patches');
    if(patch(commit)!==patch(original)) throw Error('baseline history runtime patch differs');
    retained.add(commit);
  }
  const declared=new Set(fixes.map(full));
  const undeclared=git('log','--format=%H',`${base}..${build}`).split('\n').filter(Boolean)
    .filter(commit=>!declared.has(commit)&&!retained.has(commit)&&files(commit).length);
  return {undeclared,baseline_history:history};
}
module.exports={account};
if(require.main===module) {
  const fs=require('node:fs');
  const spec=JSON.parse(fs.readFileSync(process.argv[2],'utf8'));
  const result=account(process.argv[3],spec.base_commit,process.argv[4]||spec.build_commit,spec.fix_commits,spec.baseline_history);
  if(result.undeclared.length) throw Error('CG_UNDECLARED_PRODUCTION_CHANGE: '+result.undeclared.join(', '));
  console.log('production accounting passed');
}
