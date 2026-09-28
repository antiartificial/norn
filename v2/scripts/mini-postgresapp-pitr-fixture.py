#!/usr/bin/env python3
"""Opt-in synthetic Postgres.app PITR rehearsal; never opens the control DB."""

import json
import os
import pathlib
import shutil
import subprocess
import tempfile
import time

BIN = pathlib.Path(os.environ.get('NORN_TEST_POSTGRES_BIN', '/Applications/Postgres.app/Contents/Versions/latest/bin'))
PORT = str(int(os.environ.get('NORN_TEST_PG_PORT', '15551')))
if not 15000 <= int(PORT) <= 65000:
    raise SystemExit('disposable PostgreSQL port must be between 15000 and 65000')
ROOT = pathlib.Path(tempfile.mkdtemp(prefix='norn-v3-pitr-', dir='/tmp'))
SOURCE = ROOT / 'source'
BASE = ROOT / 'base'
RESTORE = ROOT / 'restore'
ARCHIVE = ROOT / 'archive'
RETAINED = ROOT / 'retained-recovery-input'
SOCKET = ROOT / 'socket'


def run(args, **kwargs):
    return subprocess.run([str(a) for a in args], check=True, text=True,
                          capture_output=True, timeout=35, **kwargs).stdout.strip()


def sql(query):
    return run([BIN / 'psql', '-XAt', '-v', 'ON_ERROR_STOP=1', '-h', SOCKET,
                '-p', PORT, '-U', 'pitrtest', '-d', 'postgres', '-c', query])


started_source = False
started_restore = False
start = time.monotonic()
try:
    os.umask(0o077)
    ARCHIVE.mkdir()
    SOCKET.mkdir()
    run([BIN / 'initdb', '-D', SOURCE, '-A', 'trust', '-U', 'pitrtest', '--no-instructions'])
    with (SOURCE / 'postgresql.conf').open('a') as config:
        config.write(f"\nport = {PORT}\nlisten_addresses = ''\n")
        config.write(f"unix_socket_directories = '{SOCKET}'\n")
        config.write("wal_level = replica\narchive_mode = on\narchive_timeout = '1s'\n")
        config.write(f"archive_command = 'test ! -f {ARCHIVE}/%f && cp %p {ARCHIVE}/%f'\n")
    run([BIN / 'pg_ctl', '-D', SOURCE, '-l', ROOT / 'source.log', '-w', 'start'])
    started_source = True
    sql('CREATE TABLE pitr_fixture (id integer primary key, marker text not null)')
    sql("INSERT INTO pitr_fixture VALUES (1, 'before-backup')")
    run([BIN / 'pg_basebackup', '-D', BASE, '-Fp', '-X', 'stream', '-c', 'fast',
         '-h', SOCKET, '-p', PORT, '-U', 'pitrtest'])
    sql("INSERT INTO pitr_fixture VALUES (2, 'after-backup')")
    target = sql('SELECT clock_timestamp()')
    time.sleep(0.05)
    sql("INSERT INTO pitr_fixture VALUES (3, 'after-target')")
    wal = sql('SELECT pg_walfile_name(pg_switch_wal())')
    deadline = time.monotonic() + 12
    while not (ARCHIVE / wal).is_file() and time.monotonic() < deadline:
        time.sleep(0.2)
    if not (ARCHIVE / wal).is_file():
        raise RuntimeError('post-backup WAL was not archived')
    run([BIN / 'pg_ctl', '-D', SOURCE, '-m', 'fast', '-w', 'stop'])
    started_source = False
    RETAINED.mkdir()
    shutil.copytree(BASE, RETAINED / 'base')
    shutil.copytree(ARCHIVE, RETAINED / 'archive')
    archived_count = len(list((RETAINED / 'archive').iterdir()))
    shutil.rmtree(SOURCE)
    shutil.rmtree(BASE)
    shutil.rmtree(ARCHIVE)
    shutil.copytree(RETAINED / 'base', RESTORE)
    (RESTORE / 'recovery.signal').touch()
    with (RESTORE / 'postgresql.conf').open('a') as config:
        config.write(f"\nrestore_command = 'cp {RETAINED / 'archive'}/%f %p'\n")
        config.write("archive_mode = off\n")
        config.write(f"recovery_target_time = '{target}'\n")
        config.write("recovery_target_action = 'promote'\n")
    try:
        run([BIN / 'pg_ctl', '-D', RESTORE, '-l', ROOT / 'restore.log', '-w', 'start'])
    except subprocess.CalledProcessError as exc:
        print(exc.stderr.strip())
        print((ROOT / 'restore.log').read_text()[-3000:])
        raise
    started_restore = True
    rows = sql('SELECT string_agg(marker, \',\' ORDER BY id) FROM pitr_fixture')
    if rows != 'before-backup,after-backup':
        raise RuntimeError(f'PITR rows differ: {rows}')
    print(json.dumps({'result': 'pass', 'postgres_version': run([BIN / 'postgres', '--version']),
                      'rows': 2, 'archived_wal_files': archived_count,
                      'source_removed_before_restore': True,
                      'elapsed_seconds': round(time.monotonic() - start, 2)}))
finally:
    stop_failed = False
    if started_restore:
        stopped = subprocess.run([str(BIN / 'pg_ctl'), '-D', str(RESTORE), '-m', 'immediate', '-w', 'stop'],
                                 capture_output=True, timeout=15)
        stop_failed = stop_failed or stopped.returncode != 0
    if started_source:
        stopped = subprocess.run([str(BIN / 'pg_ctl'), '-D', str(SOURCE), '-m', 'immediate', '-w', 'stop'],
                                 capture_output=True, timeout=15)
        stop_failed = stop_failed or stopped.returncode != 0
    if stop_failed:
        print(f'disposable PostgreSQL did not stop; inspect {ROOT}', flush=True)
    else:
        shutil.rmtree(ROOT)
