#!/usr/bin/env python3
"""Cloud-free API qualification with no real credentials and no Docker socket mounts."""
import hashlib,json,os,socket,subprocess,time,urllib.request,urllib.error,uuid
from pathlib import Path
from evaluate import test_manifest
ROOT=Path(__file__).resolve().parents[1]
LOCAL_HTTP=urllib.request.build_opener(urllib.request.ProxyHandler({}))
IMAGES={'moto':'motoserver/moto@sha256:91fd602a21f49cf9eb82fdf474015a3c131d40104c8297ea6a2ca920708ae32c','aws':'ministackorg/ministack@sha256:706b2b83c6be7e4f4dbb6a0dc28ffdebb500c6c80b64cf7938f45040fb2158e8','azure':'floci/floci-az@sha256:0c673d49bb75b502ea0750f1c1347777483ffc33945539e1d9254438cb441a03'}
def command(args,**kw):return subprocess.run(args,check=True,text=True,capture_output=True,timeout=180,**kw).stdout.strip()
def main():
 os.chdir(ROOT);token=uuid.uuid4().hex[:12];network='runnerscout-emulators-'+token;names=[]
 out=ROOT/'evidence'/network;out.mkdir(parents=True);report={'scope':'emulated cloud APIs, not real cloud VM execution','images':{},'checks':{},'required_paths':['aws_adapter','azure_vm_rest_smoke'],'aws_mode':'Moto API state transitions with explicit client-token and interface-tag extensions; no guest execution','aws_extension_sha256':hashlib.sha256((ROOT/'tools/fixtures/moto_server.py').read_bytes()).hexdigest()}
 try:
  for key,image in IMAGES.items():
   command(['docker','pull',image]);digest=json.loads(command(['docker','image','inspect',image]))[0]['RepoDigests'][0];report['images'][key]=digest
  command(['docker','network','create','--internal',network])
  for key,port in [('aws',4566),('azure',4577),('moto',5000)]:
   name=network+'-'+key;names.append(name)
   command(['docker','run','-d','--name',name,'--network',network,'--network-alias',key,'--cpus','1','--memory','512m',*(['-e','MOTO_ACCOUNT_ID=000000000000','--mount',f'type=bind,src={ROOT / "tools/fixtures/moto_server.py"},dst=/fixture/moto_server.py,readonly','--entrypoint','python'] if key=='moto' else []),report['images'][key],*(['/fixture/moto_server.py'] if key=='moto' else [])])
  azure_ip=json.loads(command(['docker','inspect',names[1]]))[0]['NetworkSettings']['Networks'][network]['IPAddress'];azure='http://'+azure_ip+':4577'
  for _ in range(60):
   try:
    LOCAL_HTTP.open(azure+'/health',timeout=1).close();break
   except (OSError,urllib.error.URLError):time.sleep(1)
  else:raise RuntimeError('Azure emulator did not become ready')
  aws_ip=json.loads(command(['docker','inspect',names[0]]))[0]['NetworkSettings']['Networks'][network]['IPAddress']
  for _ in range(60):
   try:
    socket.create_connection((aws_ip,4566),timeout=1).close();break
   except OSError:time.sleep(1)
  else:raise RuntimeError('AWS emulator did not become ready')
  moto_ip=json.loads(command(['docker','inspect',names[2]]))[0]['NetworkSettings']['Networks'][network]['IPAddress']
  for _ in range(60):
   try:
    socket.create_connection((moto_ip,5000),timeout=1).close();break
   except OSError:time.sleep(1)
  else:raise RuntimeError('Moto emulator did not become ready')
  env=os.environ.copy();env['RUNNERSCOUT_EMULATOR_NETWORK']=network;env['RUNNERSCOUT_MINISTACK_ENDPOINT']='http://'+json.loads(command(['docker','inspect',names[0]]))[0]['NetworkSettings']['Networks'][network]['IPAddress']+':4566';env['RUNNERSCOUT_AWS_ENDPOINT']='http://'+moto_ip+':5000'
  test=subprocess.run(['go','test','-tags','emulators','-run','Test(MotoAWSLostResponseAndCleanup|MinistackAWSUnsupportedImageRetainsObligation)','-count=1','-json','./internal/provider'],env=env,text=True,capture_output=True,timeout=180)
  (out/'aws-adapter.jsonl').write_text(test.stdout);(out/'aws-adapter-stderr.log').write_text(test.stderr)
  required={('github.com/tsouza/runnerscout/internal/provider',name) for name in ['TestMotoAWSLostResponseAndCleanup','TestMinistackAWSUnsupportedImageRetainsObligation']}
  report['aws_tests']=test_manifest(test.stdout.splitlines(),required)
  aws_pass=test.returncode==0 and report['aws_tests']['pass_']
  report['checks']['aws_adapter']='pass' if aws_pass else 'fail'
  # Floci Azure's own az-vm test is best-effort and may skip. Exercise its REST
  # control plane explicitly, without representing this as full adapter coverage.
  subscription='00000000-0000-0000-0000-000000000001';group='runnerscout-'+token;vm='rs-'+token
  base=azure+'/subscriptions/'+subscription+'/resourceGroups/'+group
  def request(method,path,body=None):
   data=json.dumps(body).encode() if body is not None else None
   req=urllib.request.Request(path,data=data,method=method,headers={'Content-Type':'application/json','Authorization':'Bearer emulator-only'})
   with LOCAL_HTTP.open(req,timeout=15) as response:
    payload=response.read();return json.loads(payload) if payload else None
  request('PUT',base+'?api-version=2021-04-01',{'location':'eastus'})
  endpoint=base+'/providers/Microsoft.Compute/virtualMachines/'+vm+'?api-version=2024-07-01'
  request('PUT',endpoint,{'location':'eastus','tags':{'runnerscout-owner':'emulator-test'},'properties':{'hardwareProfile':{'vmSize':'Standard_D2s_v5'},'storageProfile':{'imageReference':{'publisher':'Canonical','offer':'ubuntu-24_04-lts','sku':'server','version':'24.04.202409050'}},'osProfile':{'computerName':vm,'adminUsername':'runner'}}})
  observed=request('GET',endpoint)
  if observed.get('name')!=vm:raise RuntimeError('Azure emulator VM identity mismatch')
  request('DELETE',endpoint)
  try:request('GET',endpoint)
  except urllib.error.HTTPError as e:
   if e.code!=404:raise
  else:raise RuntimeError('Azure emulator VM deletion not observed')
  request('DELETE',base+'?api-version=2021-04-01')
  report['checks']['azure_vm_rest_smoke']='pass'
  report['checks']['azure_full_adapter']='unqualified: ARM deployment/disk composition not exercised'
  report['checks']['gcp_compute']='unavailable: Floci GCP does not implement standalone Compute Engine instances'
  report['verdict']='pass' if aws_pass else 'fail'
 except Exception as e:report['verdict']='fail';report['reason']=str(e)
 finally:
  cleanup=[]
  for name in names:
   try:
    logs=subprocess.run(['docker','logs',name],capture_output=True,timeout=10);(out/(name+'.log')).write_bytes(logs.stdout+logs.stderr)
    command(['docker','rm','-f',name])
   except Exception as e:cleanup.append(str(e))
  try:command(['docker','network','rm',network])
  except Exception as e:cleanup.append(str(e))
  report['cleanup_errors']=cleanup
  if cleanup:report['verdict']='fail'
  (out/'manifest.json').write_text(json.dumps(report,indent=2)+'\n')
 print(json.dumps(report));return 0 if report['verdict']=='pass' else 1
if __name__=='__main__':raise SystemExit(main())
