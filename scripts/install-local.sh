#!/usr/bin/env bash
set -euo pipefail

project_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
prefix=${PREFIX:-"$HOME/.local"}
temporary_binary=$(mktemp)
trap 'rm -f "$temporary_binary"' EXIT

cd "$project_dir"
go build -o "$temporary_binary" ./cmd/gosiptea
install -Dm755 "$temporary_binary" "$prefix/bin/gosiptea"
install -Dm644 packaging/gosiptea.desktop "$prefix/share/applications/gosiptea.desktop"

if command -v update-desktop-database >/dev/null 2>&1; then
  update-desktop-database "$prefix/share/applications" >/dev/null 2>&1 || true
fi

printf 'Installed %s and the GoSipTea desktop entry.\n' "$prefix/bin/gosiptea"
