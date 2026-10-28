#!/usr/bin/env bash
# Re-exports patch files from the feature commits.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PATCH_DIR="$ROOT/patches"

mkdir -p "$PATCH_DIR"
git -C "$ROOT" format-patch -1 feat/worktree-provider --stdout > "$PATCH_DIR/0001-worktree-external-provider-command.patch"
echo "Exported $PATCH_DIR/0001-worktree-external-provider-command.patch"
