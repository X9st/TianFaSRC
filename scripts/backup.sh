#!/usr/bin/env bash
set -euo pipefail
umask 077

cd "$(dirname "$0")/.."
destination="${1:-backups/tianfa-$(date -u +%Y%m%dT%H%M%SZ)}"
compose_config="$(docker compose config --format json)"
data_path="$(python3 -c 'import json,sys; c=json.load(sys.stdin); print(next(v["source"] for v in c["services"]["app"]["volumes"] if v.get("type")=="bind" and v.get("target")=="/var/lib/tianfa"))' <<< "$compose_config")"
skills_path="$(python3 -c 'import json,sys; c=json.load(sys.stdin); print(next(v["source"] for v in c["services"]["app"]["volumes"] if v.get("type")=="bind" and v.get("target")=="/app/skills"))' <<< "$compose_config")"

if [[ -e "$destination" || -L "$destination" ]]; then
  echo "Backup destination already exists: $destination" >&2
  exit 1
fi
if [[ ! -d "$data_path" ]]; then
  echo "Data directory does not exist: $data_path" >&2
  exit 1
fi
mkdir -p "$(dirname "$destination")"
temporary="$(mktemp -d "${destination}.partial.XXXXXX")"
app_was_running=false
cleanup() {
  if [[ "$app_was_running" == true ]]; then
    docker compose start app >/dev/null
  fi
  if [[ -n "$temporary" && -d "$temporary" ]]; then
    rm -rf -- "$temporary"
  fi
}
trap cleanup EXIT

if docker compose ps --status running --services | grep -qx app; then
  app_was_running=true
  docker compose stop app >/dev/null
fi
postgres_ready=false
for _ in {1..30}; do
  if docker compose exec -T postgres pg_isready -U tianfa -d tianfa >/dev/null 2>&1; then
    postgres_ready=true
    break
  fi
  sleep 1
done
if [[ "$postgres_ready" != true ]]; then
  echo "PostgreSQL did not become ready for backup" >&2
  exit 1
fi
docker compose exec -T postgres pg_dump -U tianfa -d tianfa -Fc --no-owner --no-acl > "$temporary/database.dump"
python3 scripts/backup_files.py pack "$data_path" "$temporary/data.tar"
if [[ -d "$skills_path" ]]; then
  python3 scripts/backup_files.py pack "$skills_path" "$temporary/skills.tar"
else
  empty_skills="$(mktemp -d)"
  python3 scripts/backup_files.py pack "$empty_skills" "$temporary/skills.tar"
  rmdir "$empty_skills"
fi
(
  cd "$temporary"
  shasum -a 256 database.dump data.tar skills.tar > manifest.sha256
)
chmod 0700 "$temporary"
chmod 0600 "$temporary"/*
mv -- "$temporary" "$destination"
temporary=""
echo "Backup created: $destination"
