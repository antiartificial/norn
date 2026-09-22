#!/usr/bin/env bash
set -euo pipefail

# Static safety contract for the managed Postgres grant handoff. The URI must
# travel only on stdin to an owner-only remote file, never in a remote command
# string, Docker environment variable, or psql argument.

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
grant_script="${script_dir}/managed-db-grant"
deploy_script="${script_dir}/deploy-app"

grep -Fq 'install -m 0600 /dev/stdin ${remote_service_file}' "${grant_script}"
grep -Fq '<"${local_service_file}"' "${grant_script}"
grep -Fq -- '--mount type=bind,src=${remote_service_file}' "${grant_script}"
grep -Fq -- '-e PGSERVICEFILE=/run/secrets/pg_service.conf' "${grant_script}"
! grep -Fq -- "-e U='\${admin_uri}'" "${grant_script}"
! grep -Fq 'ALTER DATABASE norn_test OWNER TO norn_test;' "${grant_script}"
grep -Fq '"${lab}" managed-db-grant' "${deploy_script}"

bash -n "${grant_script}" "${deploy_script}"
echo "managed DB grant uses an owner-only stdin handoff and least-privilege schema grant"
