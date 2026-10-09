#!/usr/bin/env python3
import json, os, stat, subprocess, tempfile, unittest
from pathlib import Path

SCRIPT = Path(__file__).with_name('mini-m7-isolated-control-runner')
FIXTURE_SPEC = SCRIPT.parent.parent / 'infra' / 'mobility-fixture' / 'infraspec.yaml'

class RunnerTest(unittest.TestCase):
    def test_checked_in_fixture_uses_its_global_unique_logical_database_name(self):
        spec = FIXTURE_SPEC.read_text()
        self.assertIn('  - name: mobility-primary\n', spec)
        self.assertNotIn('  - name: primary\n', spec)

    def harness(self, root):
        for name in ('root', 'apps', 'secrets'):
            (root / name).mkdir(); (root / name).chmod(0o700)
        fixture = root / 'apps' / 'v3-mobility-fixture'; fixture.mkdir()
        (fixture / 'infraspec.yaml').write_text('name: v3-mobility-fixture\ndatabases:\n  - name: mobility-primary\n')
        primary = {'NORN_DATABASE_URL':'postgresql://norn@localhost/norn_v2','NORN_API_TOKEN':'p'*32,'NORN_AUDIT_SIGNING_KEY':'a'*32}
        candidate = {'NORN_M7_DATABASE_URL':'postgresql://m7@localhost/norn_m7_control','NORN_M7_API_TOKEN':'t'*32,'NORN_M7_AUDIT_SIGNING_KEY':'k'*32,'NORN_M7_NOMAD_ADDR':'http://127.0.0.1:4646','NORN_M7_CONSUL_ADDR':'http://127.0.0.1:8500'}
        for name, value in (('primary.json',primary),('candidate.json',candidate)):
            (root/name).write_text(json.dumps(value)); (root/name).chmod(0o600)
        sops=root/'sops'; sops.write_text('#!/bin/sh\nset -eu\n[ "$1" = -d ] && [ "$2" = --output-type ] && [ "$3" = json ]\ncat "$4"\n'); sops.chmod(0o700)
        env=os.environ.copy(); env.update({'NORN_M7_ISOLATED_CONTROL_RUNNER':'1','NORN_M7_CONTROL_ROOT':str(root/'root'),'NORN_M7_CONTROL_APPS_DIR':str(root/'apps'),'NORN_M7_CONTROL_SECRET_DIR':str(root/'secrets'),'NORN_M7_CONTROL_ENV_FILE':str(root/'candidate.json'),'NORN_M7_PRIMARY_ENV_FILE':str(root/'primary.json'),'NORN_M7_CONTROL_PORT':'18810','NORN_M7_CONTROL_PROFILE':'mini-m7','NORN_SOPS_BIN':str(sops)})
        return env, candidate

    def test_renders_passive_private_env_and_never_exports_primary_values(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp); env, candidate=self.harness(root); output=root/'root'/'runtime.env'
            result=subprocess.run([str(SCRIPT),'render-env',str(output)],env=env,text=True,capture_output=True)
            self.assertEqual(result.returncode,0,result.stderr)
            text=output.read_text(); lines=text.splitlines()
            self.assertEqual(len(lines), 19)
            self.assertTrue(all('=' in line for line in lines))
            self.assertIn('NORN_STARTUP_MODE=passive',text); self.assertIn('NORN_SCHEMA_MODE=check',text); self.assertIn('NORN_SKIP_OPERATION_WORKER=true',text); self.assertIn('NORN_SKIP_NOMAD_WATCHER=true',text); self.assertIn(candidate['NORN_M7_DATABASE_URL'],text); self.assertNotIn('p'*32,text); self.assertNotIn('a'*32,text)
            sourced=subprocess.run(['/bin/bash','-c','set -a; . "$1"; printf "%s\\n%s\\n%s" "$NORN_DATABASE_URL" "$NORN_DATABASE_PROFILE" "$NORN_SKIP_OPERATION_WORKER"','_',str(output)],text=True,capture_output=True)
            self.assertEqual(sourced.returncode,0,sourced.stderr)
            self.assertEqual(sourced.stdout.splitlines(),[candidate['NORN_M7_DATABASE_URL'],'mini-m7','true'])
            self.assertEqual(stat.S_IMODE(output.stat().st_mode),0o600)

    def test_rejects_reused_primary_token_before_writing_runtime_env(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp); env, candidate=self.harness(root); candidate['NORN_M7_API_TOKEN']='p'*32; (root/'candidate.json').write_text(json.dumps(candidate)); output=root/'root'/'runtime.env'
            result=subprocess.run([str(SCRIPT),'render-env',str(output)],env=env,text=True,capture_output=True)
            self.assertNotEqual(result.returncode,0); self.assertIn('reuses the primary API token',result.stderr); self.assertFalse(output.exists())

    def test_rejects_shared_or_legacy_logical_fixture_catalog(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp); env,_=self.harness(root); (root/'apps'/'other').mkdir(); output=root/'root'/'runtime.env'
            result=subprocess.run([str(SCRIPT),'render-env',str(output)],env=env,text=True,capture_output=True)
            self.assertNotEqual(result.returncode,0); self.assertIn('only v3-mobility-fixture',result.stderr); self.assertFalse(output.exists())

    def test_rejects_equivalent_primary_endpoint_and_database_despite_dsn_spelling(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp); env, candidate=self.harness(root); output=root/'root'/'runtime.env'
            primary={'NORN_DATABASE_URL':'postgresql://norn@LOCALHOST:5432/norn_m7_control?sslmode=require','NORN_API_TOKEN':'p'*32,'NORN_AUDIT_SIGNING_KEY':'a'*32}
            candidate['NORN_M7_DATABASE_URL']='postgresql://m7@localhost/norn_m7_control?sslmode=disable'
            (root/'primary.json').write_text(json.dumps(primary))
            (root/'candidate.json').write_text(json.dumps(candidate))
            result=subprocess.run([str(SCRIPT),'render-env',str(output)],env=env,text=True,capture_output=True)
            self.assertNotEqual(result.returncode,0)
            self.assertIn('reuses the primary endpoint and database', result.stderr)
            self.assertFalse(output.exists())

    def test_read_only_probe_parses_tabular_psql_identity_and_writes_sanitized_receipt(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp); env, _=self.harness(root)
            psql=root/'psql'; psql.write_text('#!/bin/sh\nprintf "norn_m7_control\\t127.0.0.1\\t5432\\n"\n'); psql.chmod(0o700)
            env['NORN_M7_PSQL_BIN']=str(psql)
            result=subprocess.run([str(SCRIPT),'probe-database'],env=env,text=True,capture_output=True)
            self.assertEqual(result.returncode,0,result.stderr)
            receipt=json.loads((root/'root'/'control-db-identity-probe.json').read_text())
            self.assertEqual(receipt,{'schema':'norn.m7-control-database-identity/v1','database':'norn_m7_control','server':'127.0.0.1','port':'5432'})
            self.assertFalse((root/'root'/'runtime-probe.env').exists())

if __name__ == '__main__': unittest.main()
