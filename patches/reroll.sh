#!/usr/bin/env bash
# Re-exports patch files from the feature commits.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PATCH_DIR="$ROOT/patches"
BRANCH="${1:-feat/worktree-provider}"

# Find the feature commit by subject, so the commit that tracks patches/ on
# top of it is never exported in its place.
sha="$(git -C "$ROOT" log "$BRANCH" -1 --format=%H \
  --grep='^feat(worktree): allow an external provider command to manage worktrees$')"
[ -n "$sha" ] || { echo "reroll.sh: feature commit not found on $BRANCH" >&2; exit 1; }

mkdir -p "$PATCH_DIR"
git -C "$ROOT" format-patch -1 "$sha" --stdout > "$PATCH_DIR/0001-worktree-external-provider-command.patch"
echo "Exported $PATCH_DIR/0001-worktree-external-provider-command.patch from ${sha:0:12}"
