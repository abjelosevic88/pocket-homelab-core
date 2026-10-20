#!/usr/bin/env python3
"""Synchronize/check the reviewed SDK snapshot in independently buildable apps."""
import argparse, hashlib, json, re, shutil
from pathlib import Path

p=argparse.ArgumentParser()
p.add_argument('--check',action='store_true')
p.add_argument('--apps-root',type=Path,default=Path(__file__).resolve().parents[2])
a=p.parse_args()
root=Path(__file__).resolve().parents[1]
files={str(f.relative_to(root)):f.read_bytes() for f in sorted((root/'sdk').glob('*.go')) if not f.name.endswith('_test.go')}
files['go.mod']=b'module github.com/abjelosevic88/pocket-homelab-core\n\ngo 1.26.5\n'
files['LICENSE']=(root/'LICENSE').read_bytes()
lock={'source':'https://github.com/abjelosevic88/pocket-homelab-core','contract':'platform-v1','sha256':{k:hashlib.sha256(v).hexdigest() for k,v in files.items()}}
files['sdk.lock.json']=(json.dumps(lock,indent=2)+'\n').encode()
files['SHA256SUMS']=''.join(hashlib.sha256(body).hexdigest()+'  '+name+'\n' for name,body in files.items()).encode()
names='bookshelf clips collaborator cost-split day documents garage habit-tracker inventory invoicing kitchen money notes podcasts subtitles tasks travel'.split()
apps=[a.apps_root / ('pocket-homelab-'+name) for name in names]
failed=[]
for app in apps:
 target=app/'third_party/pocket-core'
 for name,body in files.items():
  dest=target/name
  if a.check:
   if not dest.is_file() or dest.read_bytes()!=body:failed.append(str(dest))
  else:
   dest.parent.mkdir(parents=True,exist_ok=True);dest.write_bytes(body)
 if not a.check:
  mod=app/'go.mod';s=mod.read_text();s=re.sub(r'(github.com/abjelosevic88/pocket-homelab-core) v[^\s]+',r'\1 v0.18.5',s)
  if 'replace github.com/abjelosevic88/pocket-homelab-core' not in s:s+='\n// Reviewed SDK snapshot; see third_party/pocket-core/sdk.lock.json.\nreplace github.com/abjelosevic88/pocket-homelab-core => ./third_party/pocket-core\n'
  mod.write_text(s)
  docker=app/'Dockerfile';s=docker.read_text();line='COPY third_party/pocket-core ./third_party/pocket-core\n'
  if line not in s:s=s.replace('COPY go.mod go.sum ./\n','COPY go.mod go.sum ./\n'+line)
  verify='RUN cd third_party/pocket-core && sha256sum -c SHA256SUMS\n'
  if verify not in s:s=s.replace(line,line+verify)
  docker.write_text(s)
if failed:raise SystemExit('SDK drift:\n'+'\n'.join(failed))
print(f'{"Checked" if a.check else "Synchronized"} {len(apps)} apps against {len(lock["sha256"])} canonical files')
