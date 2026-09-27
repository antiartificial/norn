#!/usr/bin/env python3
"""Restore only the synthetic off-host fixture into a disposable Mac cluster."""

import hashlib
import json
import os
import pathlib
import shutil
import subprocess
import sys
import tempfile
import time

if len(sys.argv) != 2:
    raise SystemExit('usage: mini-postgresapp-offhost-restore-fixture.py /absolute/synthetic.dump')
BIN = pathlib.Path(os.environ.get('NORN_TEST_POSTGRES_BIN', '/Applications/Postgres.app/Contents/Versions/latest/bin'))
DUMP = pathlib.Path(sys.argv[1])
if not DUMP.is_absolute() or not DUMP.is_file() or DUMP.stat().st_size > 1 << 20:
    raise SystemExit('fixture dump must be an absolute regular file smaller than 1 MiB')
PORT = str(int(os.environ.get('NORN_TEST_PG_PORT', '15554')))
if not 15000 <= int(PORT) <= 65000:
    raise SystemExit('disposable PostgreSQL port must be between 15000 and 65000')
ROOT = pathlib.Path(tempfile.mkdtemp(prefix='norn-v3-offhost-restore-', dir='/tmp'))
DATA = ROOT / 'data'
SOCKET = ROOT / 'socket'


def run(args):
    return subprocess.run([str(x) for x in args], check=True, capture_output=True,
                          text=True, timeout=30).stdout.strip()


started = False
start = time.monotonic()
try:
    os.umask(0o077)
    toc = run([BIN / 'pg_restore', '-l', DUMP])
    if 'offhost_fixture' not in toc:
        raise RuntimeError('dump does not contain the synthetic fixture')
    SOCKET.mkdir()
    run([BIN / 'initdb', '-D', DATA, '-A', 'trust', '-U', 'offhost', '--no-instructions'])
    with (DATA / 'postgresql.conf').open('a') as config:
        config.write(f"\nport={PORT}\nlisten_addresses=''\nunix_socket_directories='{SOCKET}'\n")
    run([BIN / 'pg_ctl', '-D', DATA, '-l', ROOT / 'server.log', '-w', 'start'])
    started = True
    run([BIN / 'pg_restore', '-e', '-O', '-x', '-h', SOCKET, '-p', PORT, '-U', 'offhost',
         '-d', 'postgres', DUMP])
    rows = run([BIN / 'psql', '-XAt', '-v', 'ON_ERROR_STOP=1', '-h', SOCKET,
                '-p', PORT, '-U', 'offhost', '-d', 'postgres', '-c',
                "SELECT string_agg(marker, ',' ORDER BY id) FROM offhost_fixture"])
    if rows != 'from-mini,second-row':
        raise RuntimeError(f'off-host rows differ: {rows}')
    print(json.dumps({'result': 'pass', 'target_postgres_version': run([BIN / 'postgres', '--version']),
                      'rows': 2, 'dump_bytes': DUMP.stat().st_size,
                      'dump_sha256': hashlib.sha256(DUMP.read_bytes()).hexdigest(),
                      'restore_elapsed_seconds': round(time.monotonic() - start, 2)}))
finally:
    stop_failed = False
    if started:
        stopped = subprocess.run([str(BIN / 'pg_ctl'), '-D', str(DATA), '-m', 'immediate', '-w', 'stop'],
                                 capture_output=True, timeout=15)
        stop_failed = stopped.returncode != 0
    if stop_failed:
        print(f'disposable PostgreSQL did not stop; inspect {ROOT}', file=sys.stderr)
    else:
        shutil.rmtree(ROOT)
