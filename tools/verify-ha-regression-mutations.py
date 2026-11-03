#!/usr/bin/env python3
"""Run behavioral no-bite controls and safety mutations in disposable source copies.
Never connects to field databases or changes the working tree.
"""
import pathlib, subprocess, tempfile, shutil, json, sys
root=pathlib.Path(__file__).resolve().parents[1]
targets=[('./internal/api','TestAgentDecisionWaitsForInheritedCommittedLease'),('./internal/coordination','TestOwnershipRenewalReadsAppliedStateBeforeObserving'),('./internal/consensus','TestAuthorizationReadWaitsForRealRaftFSMAndCallerDeadline'),('./internal/discovery','TestPostgreSQLSourceLossKeepsOnlyVerifiedBoundIdentity'),('./internal/platformupdate','TestHistoryProjection')]
mutations=[
 ("clock replaces unrelated operation","internal/platformupdate/manager.go","latest.historyAfter[result.OperationID]","true",4),
 ('API skips read barrier','internal/api/agent_reconcile.go','reader.SynchronizeRead(request.Context())','func() error { _ = reader; return nil }()',0),
 ('keeper skips read barrier','internal/coordination/ownership_keeper.go','reader.SynchronizeRead(ctx)','func() error { _ = reader; return nil }()',1),
 ('quorum without FSM apply','internal/consensus/raft.go','node.raft.Barrier(node.applyTimeout)','node.raft.VerifyLeader()',2),
 ('changed source config accepted','adapters/postgresql/source_loss.go','old.EngineMetadata["verified_upstream_config"] != fingerprint','false',3),
 ('undrained WAL accepted','adapters/postgresql/source_loss.go','received != replayed','received < replayed',3),
 ('wrapper time overrides original terminal','internal/platformupdate/history.go','job.FinishedAt = event.UpdatedAt','job.FinishedAt = job.UpdatedAt',4),
 ('contradictory terminal replaces history','internal/platformupdate/history.go','previous.Status != job.Status','false',4),
]
paths=subprocess.check_output(['git','ls-files','-z','--cached','--others','--exclude-standard'],cwd=root).decode().split('\0')
with tempfile.TemporaryDirectory(prefix='cg-ha-mutations-') as d:
 tmp=pathlib.Path(d)
 for name in set(paths):
  if not name or name.startswith(('.git/','release/','.build/')):continue
  f=root/name
  if f.is_file():
   (tmp/name).parent.mkdir(parents=True,exist_ok=True);shutil.copyfile(f,tmp/name)
 def run(index):
  pkg,test=targets[index]
  r=subprocess.run(['go','test',pkg,'-run',test,'-count=1','-timeout=45s'],cwd=tmp,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True,timeout=60)
  return r.returncode,r.stdout
 for i,(pkg,test) in enumerate(targets):
  code,output=run(i)
  if code:print(output);raise SystemExit('unmutated behavioral control failed: '+test)
 print('PASS: 5 unmutated behavioral controls',flush=True)
 for label,name,old,new,index in mutations:
  f=tmp/name;original=f.read_text();assert old in original,(label,'mutation point missing');f.write_text(original.replace(old,new,1))
  try:code,output=run(index)
  finally:f.write_text(original)
  if code==0:raise SystemExit('SURVIVED: '+label)
  if '[build failed]' in output or 'undefined:' in output or 'syntax error' in output:print(output);raise SystemExit('mutation failed compilation instead of behavior: '+label)
  if '--- FAIL:' not in output:print(output);raise SystemExit('mutation did not reach a behavioral assertion: '+label)
  print('CAUGHT: '+label,flush=True)
 f=tmp/'internal/platformupdate/history.go';f.write_text(f.read_text()+'\n// unrelated no-bite control\n')
 code,output=run(4)
 if code:print(output);raise SystemExit('comment-only control false positive')
 print('PASS: comment-only no-bite control; all 8 behavioral safety mutations caught',flush=True)
