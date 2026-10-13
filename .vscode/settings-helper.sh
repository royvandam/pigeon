#!/usr/bin/env bash

set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "Usage: $0 --get-go-path | --get-gopls-path | --get-goimports-path | --get-env-path" >&2
  exit 1
fi

case "$1" in
  --get-go-path)
    query='command -v go'
    ;;
  --get-gopls-path)
    query='command -v gopls'
    ;;
  --get-goimports-path)
    query='command -v goimports'
    ;;
  --get-env-path)
    query='printf "%s\n" "$PATH"'
    ;;
  *)
    echo "Unknown option: $1" >&2
    exit 1
    ;;
esac

workspace_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"

# Always resolve this project's shell, even when called from another Nix shell.
exec nix-shell "$workspace_dir/shell.nix" --run "$query"
