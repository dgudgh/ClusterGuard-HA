#!/usr/bin/env python3
"""Run meaningful gateway assertion mutations in disposable repository copies."""
import json
from pathlib import Path
import shutil
import subprocess
import tempfile
root=Path(__file__).resolve().parent.parent
files=subprocess.check_output(['git','ls-files','-z'],cwd=root).decode().split('\0')
cases=[
 ('query-pipe-deadline','adapters/mysql/runner.go','command.WaitDelay = 100 * time.Millisecond','command.WaitDelay = 0','./adapters/mysql','TestCLIQueryDeadlineBoundsInheritedOutputPipes'),
 ('lease-expiry','internal/store/gateway.go','grant.ExpiresAt = current.ExpiresAt','grant.ExpiresAt = now.Add(15 * time.Second)','./internal/store','TestGatewayGrantReservationRequiresExactLiveLeaseAndPersistsOrder'),
 ('partial-activation','internal/endpoint/gateway.go','p.inventory.ReserveGatewayGrant(lease, operation, "withdraw", false)','p.inventory.ReserveGatewayGrant(lease, operation, "activate", false)','./internal/endpoint','TestGatewayFailuresNeverAuthorizeUnknownOwner'),
 ('sql-writability','internal/endpoint/gateway.go','if primaries != 1 || !proven','if primaries < 0 || !proven && false','./internal/endpoint','TestGatewayFailuresNeverAuthorizeUnknownOwner'),
 ('agent-scope','internal/agent/gateway_reconcile.go','decision.EndpointProvider != model.EndpointProviderGateway','decision.EndpointProvider == "invalid"','./internal/agent','TestGatewayAgentNeverTouchesHostVIPAndFencesOnAuthorizationLoss'),
 ('renewal-target','internal/endpoint/gateway_client.go','old.TargetID != grant.TargetID','false','./internal/endpoint','TestGatewayHTTPRenewalNeverReactivatesUnknownState'),
 ('single-opt-in','internal/endpoint/gateway_client.go','minimum := 2','minimum := 1','./internal/endpoint','TestSingleGatewayProfileRequiresExplicitOptIn'),
 ('partial-epoch','internal/endpoint/gateway.go','g.Epoch != last.Epoch','false','./internal/endpoint','TestGatewayPartialAvailabilityNeverBootstrapsOrRebinds'),
]
base=root/'.build/gateway';base.mkdir(parents=True,exist_ok=True)
results=[]
for name,relative,before,after,package,test in cases:
 with tempfile.TemporaryDirectory(prefix='mutation-',dir=base) as directory:
  checkout=Path(directory)
  for file in files:
   if not file:continue
   target=checkout/file;target.parent.mkdir(parents=True,exist_ok=True);shutil.copy2(root/file,target)
  path=checkout/relative;text=path.read_text()
  if text.count(before)!=1:raise SystemExit('mutation not unique: '+name)
  path.write_text(text.replace(before,after))
  result=subprocess.run(['go','test',package,'-run','^'+test+'$','-count=1'],cwd=checkout,capture_output=True,text=True,timeout=120)
  output=result.stdout+result.stderr
  caught=result.returncode!=0 and '--- FAIL: '+test in output and 'build failed' not in output
  (base/('mutation-'+name+'.txt')).write_text(output)
  results.append({'name':name,'caught_by_assertion':caught});print(name+': '+str(caught),flush=True)
(base/'behavior-mutations.json').write_text(json.dumps(results,indent=2)+'\n')
if not all(x['caught_by_assertion'] for x in results):raise SystemExit(1)
