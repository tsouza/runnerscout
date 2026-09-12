#!/usr/bin/env python3
"""Execute targeted semantic mutants in disposable copies; retain every result."""
import hashlib,json,shutil,subprocess,tempfile,uuid
from pathlib import Path

ROOT=Path(__file__).resolve().parents[1]
MUTANTS=[
 ('unknown-is-exhausted','internal/placement/placement.go','if blocked {','if false && blocked {','./internal/placement','TestFallbackRequiresDefinitiveExhaustion'),
 ('exclusive-deadline','internal/lifecycle/lifecycle.go','expired := !now.Before(a.Deadline)','expired := now.After(a.Deadline)','./internal/lifecycle','TestDeadlineInclusiveNoCloudEffect'),
 ('delete-ack-is-absence','internal/lifecycle/lifecycle.go','if a.Phase == Deleting {','if a.Phase == Deleting { a.Phase = Deleted; return save();','./internal/lifecycle','TestLostCreateResponseRestartAndCleanup'),
]
def main():
 out=ROOT/'evidence'/('qualification-'+str(uuid.uuid4()));out.mkdir(parents=True)
 result={'scope':'targeted semantic controls; not full protected harness qualification','mutants':[]}
 for name,path,original,replacement,package,test in MUTANTS:
  with tempfile.TemporaryDirectory(prefix='runnerscout-mutant-') as tmp:
   dst=Path(tmp)
   for file in ['go.mod','go.sum']:shutil.copy2(ROOT/file,dst/file)
   shutil.copytree(ROOT/'internal',dst/'internal')
   p=dst/path;s=p.read_text()
   if s.count(original)!=1:raise RuntimeError('mutant construction no longer matches: '+name)
   source=hashlib.sha256(s.encode()).hexdigest()
   # First execute the positive control for this exact witness.
   command=['go','test','-count=1','-run','^'+test+'$',package]
   good=subprocess.run(command,cwd=dst,capture_output=True,timeout=120)
   (out/(name+'-control.log')).write_bytes(good.stdout+good.stderr)
   if good.returncode:raise RuntimeError('positive control failed: '+name)
   p.write_text(s.replace(original,replacement))
   bad=subprocess.run(command,cwd=dst,capture_output=True,timeout=120)
   (out/(name+'-mutant.log')).write_bytes(bad.stdout+bad.stderr)
   # A compiler error is not detection of the intended semantic violation.
   detected=bad.returncode!=0 and ('--- FAIL: '+test).encode() in bad.stdout
   result['mutants'].append(dict(name=name,witness=test,original_sha256=source,detected=detected,exit_code=bad.returncode))
 result['verdict']='pass' if all(m['detected'] for m in result['mutants']) else 'fail'
 (out/'manifest.json').write_text(json.dumps(result,indent=2)+'\n')
 print(json.dumps(result));return 0 if result['verdict']=='pass' else 1
if __name__=='__main__':raise SystemExit(main())
