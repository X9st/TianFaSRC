#!/bin/sh
set -eu

project_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
source_dir="$project_root/web/out"
target_dir="$project_root/internal/webui/dist"

if [ ! -f "$source_dir/index.html" ]; then
  echo "web/out/index.html 不存在，请先运行 npm run build" >&2
  exit 1
fi

mkdir -p "$target_dir"
find "$target_dir" -mindepth 1 -maxdepth 1 -exec rm -rf {} +
cp -R "$source_dir"/. "$target_dir"/

