"""Disposable-container idle PSS comparison; never runs against host services."""
import json,os,subprocess,tempfile,time
from pathlib import Path
binary=os.environ['CTLVPS_SINGBOX']
def measure(groups):
 with tempfile.TemporaryDirectory() as tmp:
  processes=[]
  try:
   for i,nodes in enumerate(groups):
    cfg={'log':{'disabled':True},'inbounds':[{'type':'shadowsocks','tag':f'node-{n}','listen':'::','listen_port':21000+n,'method':'aes-128-gcm','password':'isolated-fixture'} for n in nodes], 'outbounds':[{'type':'direct'}]}
    path=Path(tmp)/f'{i}.json';path.write_text(json.dumps(cfg))
    processes.append(subprocess.Popen([binary,'run','-c',str(path)],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL))
   time.sleep(2)
   pss=0
   for p in processes:
    assert p.poll() is None,'proxy exited'
    for line in Path(f'/proc/{p.pid}/smaps_rollup').read_text().splitlines():
     if line.startswith('Pss:'):pss+=int(line.split()[1])
   return pss
  finally:
   for p in processes:p.terminate()
   for p in processes:p.wait()
legacy=measure([[n] for n in range(1,7)]);shared=measure([list(range(1,7))])
print(json.dumps({'metric':'idle aggregate PSS KiB','six_processes':legacy,'one_process_six_inbounds':shared,'reduction_percent':round(100*(1-shared/legacy),1)}))
