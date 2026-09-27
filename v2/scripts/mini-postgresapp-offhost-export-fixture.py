#!/usr/bin/env python3
"""Emit a synthetic Postgres.app custom dump on stdout for off-host restore."""

import os
import pathlib
import shutil
import subprocess
import sys
import tempfile

if sys.stdout.isatty():
    raise SystemExit('redirect the synthetic dump to a private file')
BIN = pathlib.Path(os.environ.get('NORN_TEST_POSTGRES_BIN', '/Applications/Postgres.app/Contents/Versions/latest/bin'))
PORT = str(int(os.environ.get('NORN_TEST_PG_PORT', '15553')))
if not 15000 <= int(PORT) <= 65000:
    raise SystemExit('disposable PostgreSQL port must be between 15000 and 65000')
ROOT = pathlib.Path(tempfile.mkdtemp(prefix='norn-v3-offhost-logical-', dir='/tmp'))
DATA = ROOT / 'data'
SOCKET = ROOT / 'socket'


def run(args):
    return subprocess.run([str(x) for x in args], check=True, capture_output=True, timeout=30).stdout


started = False
try:
    os.umask(0o077)
    SOCKET.mkdir()
    run([BIN / 'initdb', '-D', DATA, '-A', 'trust', '-U', 'pitrtest', '--no-instructions'])
    with (DATA / 'postgresql.conf').open('a') as config:
        config.write(f"\nport={PORT}\nlisten_addresses=''\nunix_socket_directories='{SOCKET}'\n")
    run([BIN / 'pg_ctl', '-D', DATA, '-l', ROOT / 'server.log', '-w', 'start'])
    started = True
    run([BIN / 'psql', '-XAt', '-v', 'ON_ERROR_STOP=1', '-h', SOCKET, '-p', PORT,
         '-U', 'pitrtest', '-d', 'postgres', '-c',
         "CREATE TABLE offhost_fixture(id integer primary key, marker text not null);"])
    run([BIN / 'psql', '-XAt', '-v', 'ON_ERROR_STOP=1', '-h', SOCKET, '-p', PORT,
         '-U', 'pitrtest', '-d', 'postgres', '-c',
         "INSERT INTO offhost_fixture VALUES (1, 'from-mini'), (2, 'second-row');"])
    dump = run([BIN / 'pg_dump', '-Fc', '-h', SOCKET, '-p', PORT, '-U', 'pitrtest',
                '-d', 'postgres', '-t', 'offhost_fixture'])
    sys.stdout.buffer.write(dump)
    sys.stdout.buffer.flush()
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
