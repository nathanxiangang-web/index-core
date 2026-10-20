#!/usr/bin/env python3
"""Issue125 owned disposable PG18 runner. No production DSNs or Docker targets.
No delete/reset of host paths. Stop retains DB, source, receipts and logs.
"""
import argparse,hashlib,json,os,queue,re,secrets,shutil,subprocess,sys,tarfile,threading,time
from pathlib import Path

# Safety assertions must never be removed by -O or PYTHONOPTIMIZE. Refuse before
# argument parsing, reading any owner file or executing any Docker command.
if sys.flags.optimize:
 print('REFUSED: R4 safety requires non-optimized Python',file=sys.stderr)
 raise SystemExit(2)

p=argparse.ArgumentParser()
p.add_argument('phase',choices=['prepare','compile','smoke','capacity20','capacity50','legacy20','fault','regression','stop','status'])
p.add_argument('--workspace',required=True)
p.add_argument('--image',default='golang:1.27.1')
p.add_argument('--ci-smoke',action='store_true')
p.add_argument('--race',action='store_true')
a=p.parse_args();W=Path(a.workspace).resolve();SRC=Path(__file__).resolve().parents[2]
assert W.is_absolute() and W!=Path('/') and W.name.startswith('indexcore-r4-125-')
assert not W.is_symlink()
assert not W.is_relative_to(SRC),'workspace must be outside source'

def run(args,timeout=60,input=None):
 # Docker cold-image pull progress goes to stderr. Never combine it with the
 # stdout-only container identity used by ownership checks. Retain neither raw
 # error stream nor command arguments in exception messages.
 result=subprocess.run(args,input=input,stdout=subprocess.PIPE,stderr=subprocess.PIPE,timeout=timeout)
 if result.returncode: raise RuntimeError('owned runner command failed: '+args[0])
 return result.stdout.decode().strip()
def emit(obj): print(json.dumps(obj,sort_keys=True),flush=True)
def persist(path,obj):
 with path.open('w',encoding='utf-8') as f: json.dump(obj,f,sort_keys=True);f.flush();os.fsync(f.fileno())
 path.chmod(0o600)
def inspect(name): return json.loads(run(['docker','inspect',name]))[0]
def state(): return json.loads((W/'owner.json').read_text())
def owned(s):
 info=inspect(s['db'])
 assert info['Id']==s['container'] and info['Config']['Labels'].get('indexcore.issue')=='125','owned database identity mismatch'
 assert info['Config']['Labels'].get('indexcore.r4.nonce')==s['nonce']
 assert not info['HostConfig']['PortBindings'] and list(info['NetworkSettings']['Networks'])==[s['network']]
 assert info['HostConfig']['Memory']==768*1024*1024 and info['HostConfig']['PidsLimit']==128
 assert len(info['Mounts'])==1 and Path(info['Mounts'][0]['Source']).resolve()==W/'pgdata'
 return info
def ready(s):
 for _ in range(60):
  r=subprocess.run(['docker','exec',s['db'],'pg_isready','-U','r4','-d','indexcore_r4_125_test'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
  if not r.returncode:return
  time.sleep(.5)
 raise RuntimeError('owned PG18 did not become ready')
def source_digest():
 h=hashlib.sha256()
 for f in sorted((W/'src').rglob('*')):
  if f.is_file() and f.suffix!='.md':h.update(str(f.relative_to(W/'src')).encode()+b'\0'+f.read_bytes())
 return h.hexdigest()
def envs(s):
 return {'INDEXCORE_TEST_DATABASE_URL':'postgres://r4:r4-disposable-only@'+s['db']+':5432/indexcore_r4_125_test?sslmode=disable',
 'INDEXCORE_R4_OPT_IN':'isolated-issue-125','INDEXCORE_R4_ATTESTATION':'/r4-attestation.json',
 'GOTOOLCHAIN':'local','GOMAXPROCS':'2','GOMEMLIMIT':'1500MiB','GOCACHE':'/cache','GOPROXY':'off'}
def container_args(s,name,extra):
 args=['docker','run','--rm','-i','--name',name,'--label','indexcore.issue=125','--label','indexcore.r4.nonce='+s['nonce'],'--network',s['network'],'--memory','2g','--cpus','2','--pids-limit','256','--security-opt','no-new-privileges']
 for k,v in {**envs(s),**extra}.items():args+=['-e',k+'='+v]
 for host,target,access in [(W/'src','/src','ro'),(W/'cache','/cache','rw'),(W/'modules','/go/pkg/mod','rw'),(W/'out','/out','rw'),(W/'attestation.json','/r4-attestation.json','ro')]:args+=['-v',str(host)+':'+target+':'+access]
 return args+['-w','/src',s['image']]
def clean(line):
 line=re.sub(r'postgres(?:ql)?://[^\s"\\]+','<DSN_REDACTED>',line)
 return line.replace('r4-disposable-only','<TEST_PASSWORD_REDACTED>')
def safe_exception(kind,value,tb):
 emit({'status':'FAIL_OR_BLOCKED','error_type':kind.__name__,'detail':clean(str(value))[:300]})
sys.excepthook=safe_exception
def phase_budget(phase):
 # Review5583387609/comment6288120356: only capacity50 gets120min.
 return ('120m',7200) if phase=='capacity50' else ('20m',1500)

def measure(s,command,extra,phase,timeout=1500):
 owned(s);name=s['network']+'-runner';log=W/(phase+'-'+str(time.time_ns())+'.log')
 started=time.monotonic();next_sample=started;last_notice=started;memory=[];cpu=[];q=queue.Queue();db_stops=0;db_starts=0
 proc=subprocess.Popen(container_args(s,name,extra)+command,stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True,bufsize=1)
 def reader():
  for line in proc.stdout:q.put(line)
  q.put(None)
 threading.Thread(target=reader,daemon=True).start();bytes_written=0;finished=False;status='FAIL'
 try:
  with log.open('w',encoding='utf-8') as out:
   log.chmod(0o600)
   while not finished:
    try:line=q.get(timeout=.25)
    except queue.Empty:line=''
    if line is None:finished=True
    elif line:
     text=clean(line);bytes_written+=len(text.encode());assert bytes_written<32*1024*1024,'log budget exceeded';out.write(text);out.flush()
     marker=line.strip()
     if marker=='R4_DB_STOP_READY':
      assert phase=='fault' and db_stops==0;owned(s)
      run(['docker','stop','--time','0',s['db']]);db_stops+=1
      proc.stdin.write('DB_DOWN\n');proc.stdin.flush()
     if marker=='R4_DB_RESTART_READY':
      assert phase=='fault' and db_stops==1 and db_starts==0;owned(s)
      run(['docker','start',s['db']]);ready(s);db_starts+=1
      proc.stdin.write('DB_UP\n');proc.stdin.flush()
    now=time.monotonic()
    if now-started>timeout:raise RuntimeError('phase wall-time budget exceeded')
    if now>=next_sample and proc.poll() is None:
     next_sample=now+3
     r=subprocess.run(['docker','stats','--no-stream','--format','{{json .}}',name],capture_output=True,text=True,timeout=8)
     if r.returncode==0 and r.stdout.strip():
      stats=json.loads(r.stdout.splitlines()[0]);m=re.match(r'([0-9.]+)([KMGT]?i?B)',stats['MemUsage'].split('/')[0].strip())
      if m:memory.append(float(m[1])*{'B':1,'kB':1000,'KB':1000,'KiB':1024,'MB':1000000,'MiB':1048576,'GB':10**9,'GiB':2**30}[m[2]])
      cpu.append(float(stats['CPUPerc'].strip('%')))
     if inspect(s['db'])['State']['Running']:
      disk=int(run(['docker','exec',s['db'],'du','-sb','/var/lib/postgresql']).split()[0]);assert disk<4*1024**3,'owned DB disk budget exceeded'
    if now-last_notice>=25:emit({'phase':phase,'state':'RUNNING','elapsed_s':round(now-started,1)});last_notice=now
  code=proc.wait(timeout=15);status='PASS' if code==0 else 'FAIL'
  if phase=='fault' and status=='PASS':assert db_stops==db_starts==1,'real DB restart not observed'
 except BaseException as failure:
  # Exact owned runner only, never a host PID or another service.
  cleanup_started=time.monotonic();cleanup_ok=False
  try:
   #60s inspect +60s stop +15s wait fit the separate300s cleanup ceiling;
   # this grants no additional capacity50 test execution time.
   cleanup_timeout=60 if phase=='capacity50' else None
   r=subprocess.run(['docker','inspect',name],capture_output=True,text=True,timeout=cleanup_timeout)
   if r.returncode==0:
    target=json.loads(r.stdout)[0]
    if target['Config']['Labels'].get('indexcore.r4.nonce')==s['nonce']:
     stopped=subprocess.run(['docker','stop','--time','0',name],stdout=subprocess.DEVNULL,timeout=cleanup_timeout)
     cleanup_ok=stopped.returncode==0
   else:cleanup_ok=proc.poll() is not None
   if phase=='capacity50':proc.wait(timeout=15)
  except (subprocess.TimeoutExpired,ValueError,OSError):
   cleanup_ok=False
  if phase=='capacity50':
   result={'phase':phase,'status':'FAIL_OR_BLOCKED','exit_code':proc.poll(),'seconds':round(time.monotonic()-started,3),'wall_budget_seconds':timeout,'cleanup_seconds':round(time.monotonic()-cleanup_started,3),'cleanup_confirmed':cleanup_ok,'error_type':type(failure).__name__,'detail':clean(str(failure))[:300],'source_fingerprint':source_digest(),'log':log.name,'sha256':hashlib.sha256(log.read_bytes()).hexdigest(),'container_memory_samples':len(memory),'container_peak_sampled_bytes':max(memory) if memory else None,'container_mean_sampled_bytes':sum(memory)/len(memory) if memory else None,'container_cpu_mean_pct':sum(cpu)/len(cpu) if cpu else None}
   persist(W/(phase+'-result.json'),result);emit(result)
  raise
 result={'phase':phase,'status':status,'exit_code':code,'seconds':round(time.monotonic()-started,3),'source_fingerprint':source_digest(),'log':log.name,'sha256':hashlib.sha256(log.read_bytes()).hexdigest(),'container_memory_samples':len(memory),'container_peak_sampled_bytes':max(memory) if memory else None,'container_mean_sampled_bytes':sum(memory)/len(memory) if memory else None,'container_cpu_mean_pct':sum(cpu)/len(cpu) if cpu else None,'db_stops':db_stops,'db_restarts':db_starts}
 persist(W/(phase+'-result.json'),result);emit(result)
 if status!='PASS':raise RuntimeError('phase failed; stop risky runs and inspect retained redacted log')
 return result

if a.phase=='prepare':
 assert not W.exists(),'refuse to overwrite an existing workspace'
 assert shutil.disk_usage(W.parent).free>=10*1024**3,'BLOCKED: need10GiB disk headroom'
 available=int(next(l.split()[1] for l in Path('/proc/meminfo').read_text().splitlines() if l.startswith('MemAvailable:')))*1024
 assert available>=4*1024**3,'BLOCKED: need4GiB available RAM'
 W.mkdir(mode=0o700)
 for name in ['src','cache','modules','pgdata','out']:(W/name).mkdir()
 if (SRC/'.git').exists():
  files=run(['git','-c','core.quotepath=false','ls-files'],timeout=30).splitlines()
 else:files=[str(f.relative_to(SRC)) for f in SRC.rglob('*') if f.is_file() and not any(part in ('.git','.work','__pycache__') for part in f.relative_to(SRC).parts)]
 for name in files:
  f=Path(name);assert not f.is_absolute() and '..' not in f.parts and f.name!='.env'
  target=W/'src'/f;target.parent.mkdir(parents=True,exist_ok=True);shutil.copy2(SRC/f,target)
 nonce=secrets.token_hex(32);network='indexcore-r4-125-'+nonce[:12];db=network+'-db'
 s={'nonce':nonce,'network':network,'db':db,'image':a.image};persist(W/'owner.json',s)
 version=run(['docker','run','--rm','--network','none',a.image,'go','version'],timeout=180);assert 'go1.27.1 ' in version
 run(['docker','run','--rm','--memory','512m','-v',str(W/'src')+':/src:ro','-v',str(W/'modules')+':/go/pkg/mod','-w','/src','-e','GOTOOLCHAIN=local',a.image,'go','mod','download'],timeout=240)
 run(['docker','network','create','--internal','--label','indexcore.issue=125','--label','indexcore.r4.nonce='+nonce,network])
 cid=run(['docker','create','--name',db,'--label','indexcore.issue=125','--label','indexcore.r4.nonce='+nonce,'--network',network,'--memory','768m','--cpus','1','--pids-limit','128','-e','POSTGRES_USER=r4','-e','POSTGRES_PASSWORD=r4-disposable-only','-e','POSTGRES_DB=indexcore_r4_125_test','--mount','type=bind,src='+str(W/'pgdata')+',dst=/var/lib/postgresql','postgres:18-alpine','-c','shared_buffers=64MB','-c','max_connections=40','-c','indexcore.r4_nonce='+nonce])
 assert re.fullmatch('[0-9a-f]{64}',cid),'Docker create did not return one container ID'
 s['container']=cid;persist(W/'owner.json',s);owned(s);run(['docker','start',db]);ready(s)
 metadata=run(['docker','exec',db,'psql','-U','r4','-d','indexcore_r4_125_test','-Atc',"SELECT system_identifier,(SELECT oid FROM pg_database WHERE datname=current_database()) FROM pg_control_system()"])
 system,oid=metadata.split('|');assert system.isdigit() and oid.isdigit()
 sql="CREATE SCHEMA r4_guard; CREATE TABLE r4_guard.instance(nonce text,container_id text,system_id text,db_oid oid); INSERT INTO r4_guard.instance VALUES ('"+nonce+"','"+cid+"','"+system+"',"+oid+");"
 run(['docker','exec','-i',db,'psql','-v','ON_ERROR_STOP=1','-U','r4','-d','indexcore_r4_125_test'],input=sql.encode())
 persist(W/'attestation.json',{'host':db,'container':cid,'nonce':nonce,'system':system,'oid':int(oid)})
 emit({'prepared':True,'host_ram_available_bytes':available,'disk_free_bytes':shutil.disk_usage(W).free,'go_version':version,'no_host_ports':True,'network_internal':True,'db_memory_limit_bytes':768*1024**2,'runner_memory_limit_bytes':2*1024**3,'db_disk_stop_budget_bytes':4*1024**3})
else:
 s=state();owned(s)
 if a.phase=='stop':
  run(['docker','stop','--time','15',s['db']]);emit({'stopped_owned_database':True,'artifacts_retained':True})
 elif a.phase=='status':emit({'container':s['db'],'running':owned(s)['State']['Running'],'source_fingerprint':source_digest()})
 elif a.phase=='compile':
  race='-race ' if a.race else '';suffix='-race' if a.race else ''
  measure(s,['sh','-c','go test -v -count=1 ./testutil/r4guard && go test '+race+'-c -o /out/scale'+suffix+'.test ./internal/runtime/scale && go test '+race+'-c -o /out/scan'+suffix+'.test ./internal/runtime/scan'],{},'compile'+suffix,600)
 elif a.phase=='regression':
  measure(s,['sh','-c','test -z "$(gofmt -l .)" && go vet ./... && go build ./... && go test -json -p 1 -count=1 ./... && go test -race -p 1 -count=1 ./internal/runtime/incrementalruntime ./internal/runtime/app && go test -race -p 1 -count=1 -run "^TestP0R[23]" ./internal/diagnostic ./internal/runtime/scan ./internal/store/postgres ./cmd/indexcore'],{},'regression',1500)
 elif a.phase=='legacy20':
  assert json.loads((W/'smoke-result.json').read_text())['status']=='PASS'
  measure(s,['/out/scale.test','-test.run=^TestScalePopulationAndDelta$','-test.v','-test.timeout=20m'],{'INDEXCORE_SCALE_TEST':'1','INDEXCORE_SCALE_N':'20000'},'legacy20')
 else:
  profile='fault' if a.phase=='fault' else 'capacity';n={'smoke':200 if a.ci_smoke else 2000,'capacity20':20000,'capacity50':50000}.get(a.phase,200)
  if a.phase in ['capacity20','capacity50']:
   prereq=json.loads((W/('capacity20-result.json' if a.phase=='capacity50' else 'smoke-result.json')).read_text())
   assert prereq['status']=='PASS' and prereq['source_fingerprint']==source_digest()
   assert not (W/'ci-smoke.marker').exists(),'CI smoke does not authorize heavy capacity'
  if a.ci_smoke:(W/'ci-smoke.marker').touch()
  regex='^TestP0R4(ProcessCrashes|DatabaseRestart)$' if profile=='fault' else '^TestP0R4Capacity$'
  suffix='-race' if a.race else ''
  go_timeout,wall_budget=phase_budget(a.phase)
  measure(s,['/out/scale'+suffix+'.test','-test.run='+regex,'-test.v','-test.timeout='+go_timeout],{'INDEXCORE_R4_PROFILE':profile,'INDEXCORE_R4_N':str(n)},a.phase,wall_budget)
