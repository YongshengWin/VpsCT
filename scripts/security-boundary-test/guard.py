"""Offline fault injection for the independently scheduled root firewall guard."""
import json,pathlib,shutil,subprocess,time
P=pathlib.Path

def verify_guard(run,connect,epoch):
    assert P('/.dockerenv').exists()
    before=epoch()
    run('nft','add','table','inet','guard_preserve_fixture')
    run('nft','add','counter','inet','guard_preserve_fixture','keep')
    untouched=run('nft','-j','list','table','inet','guard_preserve_fixture')
    def rule_count():
        r=subprocess.run(['nft','-j','list','table','inet','ctlvps_egress'],capture_output=True,text=True)
        if r.returncode:return 0
        return sum('rule' in e for e in json.loads(r.stdout)['nftables'])
    count=rule_count();assert count>0
    assert run('systemctl','is-active','ctlvps-proxy-guard.timer')=='active'
    for args in [('delete','table','inet','ctlvps_egress'),('flush','chain','inet','ctlvps_egress','output')]:
        run('nft',*args)
        deadline=time.monotonic()+15
        while rule_count()!=count:
            if time.monotonic()>deadline:raise AssertionError('independent timer did not repair firewall')
            time.sleep(.2)
        connect();assert epoch()==before
    assert run('nft','-j','list','table','inet','guard_preserve_fixture')==untouched
    # Deterministic failure to load rules, inside this disposable container only.
    run('systemctl','stop','ctlvps-proxy-guard.timer','ctlvps-proxy-guard.service')
    binary=P(shutil.which('nft')).resolve();saved=binary.with_name('nft-fixture-original')
    assert not saved.exists();binary.rename(saved)
    try:
        binary.write_text('#!/bin/sh\nif [ "$1" = "-f" ]; then exit 1; fi\nexec '+str(saved)+' "$@"\n');binary.chmod(0o755)
        run('nft','flush','chain','inet','ctlvps_egress','output')
        # A frozen process cannot handle SIGTERM; failure must still stop it.
        run('systemctl','kill','--kill-who=all','--signal=SIGSTOP','ctlvps-singbox.service')
        p=subprocess.run(['systemctl','start','ctlvps-proxy-guard.service'],capture_output=True)
        assert p.returncode!=0
        assert not P('/run/ctlvps-proxy/egress-ready').exists()
        assert run('systemctl','show','ctlvps-singbox.service','-p','ActiveState','--value')=='inactive'
        assert 'ctlvps-singbox.service' in json.loads(P('/run/ctlvps-proxy/policy.json').read_text())['paused']
    finally:
        binary.unlink();saved.rename(binary)
    run('systemctl','reset-failed','ctlvps-proxy-guard.service')
    run('systemctl','start','ctlvps-proxy-guard.service')
    assert P('/run/ctlvps-proxy/egress-ready').exists()
    connect();assert epoch()!=before
    assert not json.loads(P('/run/ctlvps-proxy/policy.json').read_text()).get('paused')
    # A manually stopped proxy must not be restarted by a healthy guard check.
    run('systemctl','stop','ctlvps-singbox.service')
    run('systemctl','start','ctlvps-proxy-guard.service')
    assert run('systemctl','show','ctlvps-singbox.service','-p','ActiveState','--value')=='inactive'
    run('systemctl','start','ctlvps-singbox.service');connect()
    # Losing both volatile files must not be confused with an idle fresh install.
    policy=P('/run/ctlvps-proxy/policy.json');saved_policy=policy.read_bytes()
    policy.unlink();P('/run/ctlvps-proxy/egress-ready').unlink()
    p=subprocess.run(['systemctl','start','ctlvps-proxy-guard.service'],capture_output=True)
    assert p.returncode!=0
    assert run('systemctl','show','ctlvps-singbox.service','-p','ActiveState','--value')=='inactive'
    policy.write_bytes(saved_policy);policy.chmod(0o600)
    run('systemctl','reset-failed','ctlvps-proxy-guard.service')
    run('systemctl','start','ctlvps-proxy-guard.service')
    run('systemctl','start','ctlvps-singbox.service');connect()
    run('systemctl','start','ctlvps-proxy-guard.timer')
    assert run('nft','-j','list','table','inet','guard_preserve_fixture')==untouched
    run('nft','delete','table','inet','guard_preserve_fixture')
    print('GUARD PASS: independent timer repairs deleted/empty rules without restart; repair failure revokes readiness and pauses proxies; recovery resumes only guard-paused services; unrelated counters unchanged.',flush=True)
