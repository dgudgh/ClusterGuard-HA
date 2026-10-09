// Actual console, actual language-select events and HTTP fixture. No writes permitted.
const {createConsoleFixture}=require('./console-ui-fixture.cjs');
const {runConsoleDriver}=require('./console-cdp-harness.cjs');
async function main(){
 let count=0,failures=0;
 for(const width of [1440,390]){
 const fixture=createConsoleFixture(process.env.CONSOLE_HTML_PATH);
 // Server-origin diagnostics are evidence, not UI literals. Use English evidence here;
 // the update-message suite separately covers Chinese/English legacy records and unknown raw evidence.
 fixture.operations.forEach(o=>o.message='Native roles, replication and endpoint verified');
 for(const [i,engine] of ['oracle','sqlserver'].entries())fixture.clusters.push({resource_id:'locale-'+engine,display_name:'sample-'+engine,engine,health:{state:'healthy'}});
 const originalHook=fixture.control.hook;
 fixture.control.hook=async args=>{if(originalHook)return originalHook(args);};
 const driver=`(async()=>{
 const sleep=ms=>new Promise(r=>setTimeout(r,ms)),checks=[];
 const check=(name,ok,detail)=>checks.push({name,ok,detail});
 for(let i=0;i<300&&!state.clusterDataReady;i++)await sleep(25);
 if(!state.clusterDataReady)throw Error('console not ready');
 const select=document.getElementById('language-select');
 const change=lang=>{select.value=lang;select.dispatchEvent(new Event('change',{bubbles:true}));};
 const han=()=>{
 const found=[];const walker=document.createTreeWalker(document.body,NodeFilter.SHOW_TEXT);
 for(let n=walker.nextNode();n;n=walker.nextNode()){
 const p=n.parentElement;if(!p||p.closest('script,style,pre,.software-update-message-original,option[value="zh-CN"]'))continue;
 if(p.getClientRects().length&&/[\\u3400-\\u9fff]/.test(n.nodeValue))found.push((p.id||p.className||p.tagName)+': '+n.nodeValue.trim());
 }return found;
 };
 document.querySelector('[data-nav="settings"]').click();
 check('default remains Chinese',document.documentElement.lang==='zh-CN'&&document.querySelector('.settings-account-block').textContent.includes('账户与安全'));
 const before={cluster:state.selectedClusterId,locked:state.switchUnlocked,epoch:state.operationLockEpoch,auth:state.authEpoch};
 change('en-US');
 check('English preference immediately changes settings',document.querySelector('.settings-account-block').textContent.includes('Account and security'));
 check('English controller status',han().length===0,document.getElementById('settings-status-panel').innerText);
 check('context and authorization unchanged',JSON.stringify(before)===JSON.stringify({cluster:state.selectedClusterId,locked:state.switchUnlocked,epoch:state.operationLockEpoch,auth:state.authEpoch}));
 for(const cluster of state.clusters){
 const clusterSelect=document.getElementById('cluster-select');clusterSelect.value=cluster.resource_id;clusterSelect.dispatchEvent(new Event('change',{bubbles:true}));
 for(let i=0;i<300&&(!state.clusterDataReady||state.clusterDetail?.cluster.resource_id!==cluster.resource_id);i++)await sleep(25);
 check(cluster.engine+' selected through real selector',state.clusterDataReady&&state.clusterDetail?.cluster.resource_id===cluster.resource_id);
 for(const view of ['overview','topology','operations','nodes','metrics','operation-log','about','settings']){
 document.querySelector('[data-nav="'+view+'"]').click();await sleep(120);
 const remaining=han();check(cluster.engine+'/'+view+' English visible UI contains no Chinese',remaining.length===0,remaining);
 check(cluster.engine+'/'+view+' English layout fits viewport',document.documentElement.scrollWidth<=innerWidth+1,{width:document.documentElement.scrollWidth,viewport:innerWidth});
 }
 }
 document.getElementById('settings-change-password').click();
 const password=document.getElementById('password-modal');
 check('English password dialog',password.open&&!/[\\u3400-\\u9fff]/.test(password.innerText),password.innerText);
 // Dispatch the real preference event while the dialog is open; preserve its inputs.
 document.getElementById('new-password').value='unchanged-test-value';
 change('zh-CN');check('open dialog follows Chinese',password.innerText.includes('修改平台密码'),password.innerText);
 change('en-US');check('open dialog follows English',password.innerText.includes('Change platform password'),password.innerText);
 check('language preserves password input',document.getElementById('new-password').value==='unchanged-test-value');password.close();
 document.querySelector('[data-nav="settings"]').click();document.getElementById('settings-configuration-tab').click();await sleep(150);
 check('configuration English labels',han().length===0,han());
 // Language change is presentation only: no reads or writes to policy/configuration.
 const savedFetch=window.fetch;let languageRequests=0;window.fetch=(...args)=>{languageRequests++;return savedFetch(...args);};
 document.getElementById('cluster-policy-observations').value='7';
 change('zh-CN');check('configuration switches back to Chinese',document.getElementById('settings-configuration-panel').innerText.includes('运行参数'));
 change('en-US');check('configuration switches to English again',han().length===0,han());
 check('language switch makes no network requests',languageRequests===0,languageRequests);window.fetch=savedFetch;
 check('language preserves unsaved policy input',document.getElementById('cluster-policy-observations').value==='7');
 document.getElementById('software-update-tab').click();await sleep(150);
 check('updates follow English preference',han().length===0,han());
 document.getElementById('open-software-update-dialog').click();
 check('upload dialog follows English preference',han().length===0,han());
 document.getElementById('software-update-dialog').close();
 document.getElementById('settings-status-tab').click();
 state.controlPlane={...state.controlPlane,ready:true,role:'follower',voter_count:3,leader_known:true,uptime_seconds:360};renderControlPlaneStatus();
 document.getElementById('settings-status-tab').click();
 check('poll-driven status remains English',document.getElementById('control-plane-role').textContent==='Follower'&&document.getElementById('control-plane-uptime').textContent==='6 minutes');
 // Business data identical to dictionary words must never be translated.
 state.currentUser.display_name='管理员';if(typeof renderAccountIdentity==='function')renderAccountIdentity();else renderAuthenticatedUser();
 check('user-provided display name untouched',document.getElementById('current-user-name').textContent==='管理员');
 check('parameter values remain verbatim',typeof ui==='function'&&ui('配置文件：{0}','配置文件')==='Configuration file: 配置文件');
 change('zh-CN');check('settings restores Chinese',document.querySelector('.settings-account-block').textContent.includes('账户与安全'));
 return checks;
 })()`;
 const run=await runConsoleDriver({fixture,driver,hash:'#settings',viewport:{width,height:1100},profilePrefix:'cg-full-language-'});
 if(!run)throw Error('browser NOT RUN');if(run.pageErrors.length)throw Error(run.pageErrors.join('\n'));
 for(const check of run.outcome){count++;if(!check.ok)failures++;console.log((check.ok?'PASS':'FAIL')+' '+width+'px '+check.name+': '+JSON.stringify(check.detail));}
 if(fixture.control.mutations)throw Error('language test reached a write action');
 }
 console.log(count+' language assertions; '+failures+' failures; no writes');if(failures)process.exitCode=1;
}
if(require.main===module)main().catch(e=>{console.error(e);process.exitCode=1;});
