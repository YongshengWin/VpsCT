#!/usr/bin/env python3
"""CI signing: protected environment keys only, never root private key."""
import json, os, pathlib, subprocess, tempfile
version=os.environ['RELEASE_VERSION']
# Use the same strict tag grammar as packaging before constructing paths.
import re
if not re.fullmatch(r'v\d+\.\d+\.\d+(?:-[A-Za-z0-9][A-Za-z0-9.-]*)?',version):raise SystemExit('invalid release tag')
release=pathlib.Path('release')/version
with tempfile.TemporaryDirectory() as directory:
    keys=pathlib.Path(directory)/'keys';keys.mkdir(mode=0o700)
    root=os.environ.get('TUF_ROOT','');json.loads(root)
    (keys/'root.json').write_text(root)
    policy=os.environ.get('TUF_SECURITY_POLICY','')
    parsed=json.loads(policy)
    if parsed.get('schema')!=1 or parsed.get('product')!='VpsCT' or not isinstance(parsed.get('revoked'),list) or not isinstance(parsed.get('min_epoch'),dict):
        raise SystemExit('explicit cumulative security policy required')
    (keys/'security-policy.json').write_text(policy)
    for role in ('targets','snapshot','timestamp'):
        value=os.environ.get('TUF_'+role.upper()+'_KEY','')
        if not re.fullmatch('[a-fA-F0-9]{128}',value):raise SystemExit('missing or invalid protected signing key')
        p=keys/(role+'.key');p.write_text(value);p.chmod(0o600)
    generation=int(os.environ['METADATA_RUN'])*100+int(os.environ['METADATA_ATTEMPT'])
    subprocess.run(['go','run','./cmd/ctlvps-sign','--keys',str(keys),'--out',str(release/'metadata'),'--manifest',str(release/'signing-manifest.json'),'--metadata-version',str(generation)],check=True)
    # Flat attachments are suitable for draft inspection. Publishing a live
    # metadata repository is an independent, explicitly approved operation.
    import tarfile
    with tarfile.open(release/'tuf-metadata.tar.gz','w:gz') as archive:archive.add(release/'metadata',arcname='metadata')
    import shutil
    shutil.rmtree(release/'metadata')
