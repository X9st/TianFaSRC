#!/usr/bin/env bash
set -euo pipefail
umask 077

cd "$(dirname "$0")/.."
backup_dir="${1:?usage: scripts/restore-drill.sh BACKUP_DIR}"
if [[ ! -d "$backup_dir" ]]; then
  echo "Backup directory does not exist: $backup_dir" >&2
  exit 1
fi
backup_dir="$(cd "$backup_dir" && pwd -P)"
(
  cd "$backup_dir"
  shasum -a 256 -c manifest.sha256
)

temporary="$(mktemp -d)"
container="tianfa-restore-$$-${RANDOM}"
app_pid=""
cleanup() {
  if [[ -n "$app_pid" ]]; then
    kill "$app_pid" 2>/dev/null || true
    wait "$app_pid" 2>/dev/null || true
  fi
  docker rm -f "$container" >/dev/null 2>&1 || true
  rm -rf -- "$temporary"
}
trap cleanup EXIT

python3 scripts/backup_files.py extract "$backup_dir/data.tar" "$temporary/data"
python3 scripts/backup_files.py extract "$backup_dir/skills.tar" "$temporary/skills"
docker run --rm --name "$container" -e POSTGRES_DB=tianfa -e POSTGRES_USER=tianfa -e POSTGRES_PASSWORD=tianfa_restore -p 127.0.0.1::5432 -d postgres:16-alpine >/dev/null
ready=false
for _ in {1..40}; do
  if docker exec "$container" pg_isready -U tianfa -d tianfa >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 1
done
if [[ "$ready" != true ]]; then
  echo "Restored PostgreSQL did not become ready" >&2
  exit 1
fi
docker exec -i "$container" pg_restore --exit-on-error -U tianfa -d tianfa --no-owner --no-acl < "$backup_dir/database.dump"
port_line="$(docker port "$container" 5432/tcp | head -n 1)"
db_port="${port_line##*:}"
export TIANFA_DATABASE_URL="postgres://tianfa:tianfa_restore@127.0.0.1:${db_port}/tianfa?sslmode=disable"
export TIANFA_DATA_DIR="$temporary/data"
go build -trimpath -o "$temporary/tianfa-server" ./cmd/server
"$temporary/tianfa-server" verify-data > "$temporary/verify.json"
http_port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')"
proxy_port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')"
TIANFA_HTTP_ADDR="127.0.0.1:${http_port}" TIANFA_PROXY_ADDR="127.0.0.1:${proxy_port}" "$temporary/tianfa-server" serve-readiness > "$temporary/server.log" 2>&1 &
app_pid="$!"
ready=false
for _ in {1..30}; do
  if curl -fsS "http://127.0.0.1:${http_port}/health/ready" > "$temporary/ready.json" 2>/dev/null; then
    ready=true
    break
  fi
  sleep 1
done
if [[ "$ready" != true ]]; then
  cat "$temporary/server.log" >&2
  echo "Restored application did not become ready" >&2
  exit 1
fi
if [[ "$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:${http_port}/api/v1/tasks")" != "404" ]]; then
  echo "Restore drill unexpectedly exposed application APIs" >&2
  exit 1
fi
echo "Restore drill passed: Blob hashes, reference counts, secrets, and application readiness"
cat "$temporary/verify.json"
