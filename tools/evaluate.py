#!/usr/bin/env python3
"""Bounded diagnostic evaluator. Editable local files are NOT protected authority."""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import shutil
import sys
import time
import uuid

ROOT = Path(__file__).resolve().parents[1]
REQUIRED = {
 "TestFallbackRequiresDefinitiveExhaustion", "TestHardConstraintsAndFreshness",
 "TestIndependentSmallCatalogOracle", "TestLostCreateResponseRestartAndCleanup",
 "TestIntentConflictPreventsCloudCall", "TestUnknownAbsenceNeverBlindlyCreates",
 "TestDeadlineInclusiveNoCloudEffect", "TestCapacityRejectionKeepsDeadline",
 "TestRedeliveryAndBoundedRearming", "TestAdmissionLimits",
 "TestDisabledAndExactRESTIdentity", "TestAWSCreateUsesDurableTokenAndPrivateBootstrap",
 "TestGCPOwnershipBlocksDeletion", "TestUnknownProviderOutputIsNotAbsence",
 "TestPersistenceAcrossStoreInstances", "TestForeignStateRejected",
 "TestDurableAdmissionSurvivesOperatorRestart",
 "TestCooldownRetainsUnknownSearch", "TestUnreadyVMExpiresButStartedJobDoesNot",
 "TestUnknownCreateWithStartedJobSurvivesProvisioningDeadline",
 "TestProviderBindingCannotChangeUnderExistingFleet", "TestMissingInventoryCannotConfirmCleanup",
 "TestAzureCreateUsesSecureBootstrapAndSpotDelete", "TestAzureActiveDeploymentCannotProveAbsence",
 "TestAzureForeignDiskCannotBeDeleted", "TestAzureResidualOwnedNICRetainsCleanup",
 "TestAWSAccountDriftCannotConfirmAbsence",
}

def validate_records(path):
 data=json.loads(path.read_text())
 if data.get('format_revision')!=1: raise ValueError('unsupported registry revision')
 records=data['records'];ids=[r['id'] for r in records]
 if len(ids)!=len(set(ids)): raise ValueError('duplicate record identity')
 if data['baseline_id'] not in ids: raise ValueError('missing baseline')
 for r in records:
  for k in ['id','revision','type','status','owner','sources']:
   if k not in r: raise ValueError(f'missing {k}')
  if not isinstance(r['revision'],int) or r['revision']<1: raise ValueError('invalid revision')
  if any(s not in ids for s in r['sources']): raise ValueError('dangling source')
 return len(records)

def test_manifest(lines, required=REQUIRED):
 started=set();passed=set();failed=set();skipped=set()
 for line in lines:
  event=json.loads(line)
  name=event.get('Test');key=(event.get('Package'),name)
  action=event.get('Action')
  if action=='fail':failed.add(key)
  if name and action=='run':started.add(key)
  if name and action=='pass':passed.add(key)
  if name and action=='skip':skipped.add(key)
 complete_names={n for p,n in started & passed}
 missing=required-complete_names
 complete=bool(started) and not failed and not skipped and not missing and started<=passed
 return dict(pass_=complete,started=len(started),completed=len(started & passed),failed=sorted(str(x) for x in failed),skipped=sorted(str(x) for x in skipped),missing=sorted(missing))

def digest():
 h=hashlib.sha256()
 for p in sorted(ROOT.rglob('*')):
  if not p.is_file() or any(part in {'.git','bin','evidence','work','__pycache__'} for part in p.relative_to(ROOT).parts):continue
  h.update(str(p.relative_to(ROOT)).encode()+b'\0'+p.read_bytes()+b'\0')
 return h.hexdigest()

def main():
 os.chdir(ROOT);evidence=ROOT/'evidence';evidence.mkdir(exist_ok=True)
 # flock serializes local evaluation and budget accounting. The maintainer can
 # edit these files; this protects concurrent invocations, not against the candidate.
 import fcntl
 with (evidence/'evaluation.lock').open('a') as lock:
  fcntl.flock(lock,fcntl.LOCK_EX)
  ledger=evidence/'attempts.jsonl';history=[json.loads(x) for x in ledger.read_text().splitlines()] if ledger.exists() else []
  attempts=sum(x['event']=='start' for x in history)
  consumed=sum(x.get('seconds',0) for x in history if x['event']=='finish')
  if shutil.disk_usage(ROOT).free < 2*1024**3:raise SystemExit('at least 2 GiB free disk required for evaluation')
  if attempts>=60 or consumed>=21600:raise SystemExit('development evaluation budget exhausted')
  run=evidence/str(uuid.uuid4());run.mkdir();start=time.monotonic();identity=digest()
  def append(record):
   with ledger.open('a') as f:f.write(json.dumps(record,sort_keys=True)+'\n');f.flush();os.fsync(f.fileno())
  append(dict(event='start',run=run.name,candidate=identity,time=time.time()))
  report=dict(baseline='DEV-0001',candidate_sha256=identity,evaluator_sha256=hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),protected=False,live_cloud_integration=False,commands=[])
  deadline=start+min(600,21600-consumed)
  def command(args,name):
   remaining=deadline-time.monotonic()
   if remaining<=0:raise TimeoutError('evaluation deadline exhausted')
   with (run/name).open('w') as log:
    p=subprocess.run(args,stdout=log,stderr=subprocess.STDOUT,timeout=remaining,check=False)
   report['commands'].append(dict(argv=args,exit_code=p.returncode,log=name))
   if p.returncode:raise RuntimeError(f'{name} failed (exit {p.returncode})')
   return (run/name).read_text()
  try:
   report['registry_records']=validate_records(ROOT/'outputs/records.json')
   formatting=command(['gofmt','-l','cmd','internal'],'format.log')
   if formatting.strip():raise RuntimeError('unformatted Go files')
   command([sys.executable,'-m','unittest','discover','-s','tools','-p','test_*.py'],'harness.log')
   command(['go','vet','./...'],'vet.log')
   raw=command(['go','test','-json','-race','-count=1','./...'],'tests.jsonl')
   report['tests']=test_manifest(raw.splitlines())
   if not report['tests']['pass_']:raise RuntimeError('missing, skipped, failed or incomplete required tests')
   command(['go','build','-trimpath','-o','bin/runnerscout','./cmd/runnerscout'],'build.log')
   if digest()!=identity:raise RuntimeError('candidate changed during evaluation')
   report['verdict']='pass'
  except Exception as e:
   report['verdict']='inconclusive' if isinstance(e,(TimeoutError,subprocess.TimeoutExpired)) else 'fail'
   report['reason']=str(e)
  finally:
   report['seconds']=time.monotonic()-start
   (run/'manifest.json').write_text(json.dumps(report,indent=2)+'\n')
   append(dict(event='finish',run=run.name,seconds=report['seconds'],verdict=report['verdict']))
  print(json.dumps(dict(verdict=report['verdict'],evidence=str(run.relative_to(ROOT)),tests=report.get('tests'),reason=report.get('reason'))))
  return 0 if report['verdict']=='pass' else 1

if __name__=='__main__':sys.exit(main())
