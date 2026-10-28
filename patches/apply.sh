#!/usr/bin/env bash
# Applies the patch stack onto the current git tree.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PATCH_DIR="$ROOT/patches"

for p in "$PATCH_DIR"/*.patch; do
  [ -f "$p" ] || continue
  echo "==> Applying $(basename "$p")..."
  git -C "$ROOT" apply "$p"
done
echo "==> All patches applied."
