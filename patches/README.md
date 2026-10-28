# tuios patches

Local patches maintained over upstream [`Gaurav-Gosain/tuios`](https://github.com/Gaurav-Gosain/tuios).

This directory tracks `.patch` files so custom capabilities (such as monorepo sparse-checkout integration and external tooling parity) can be version-controlled, audited, and re-rolled onto new upstream releases over time.

---

## Applied patches

| Patch | Summary | Details |
| --- | --- | --- |
| `0001-worktree-external-provider-command.patch` | External worktree provider command | Adds `[worktrees] provider_command` and `TUIOS_WORKTREES_PROVIDER_COMMAND` / `HERDR_WORKTREES_PROVIDER_COMMAND`. Delegates worktree lifecycle (`detect`, `resolve`, `create`, `list`, `remove`) to an external command instead of native `git worktree`, with automatic fallback for unmanaged repositories. |

---

## Patch details

### `0001-worktree-external-provider-command.patch`

- **Purpose**: Native `git worktree add` bypasses sparse checkout and tooling hooks in large monorepos. In these environments, worktrees must be provisioned via specialized workspace tools to set up sparse checkouts, shadow environments, and toolchains.
- **Parity with Herdr**: Implements the identical 5-verb JSON contract used by Herdr's worktree provider patch:
  - `detect --repo <root>`: Returns `{"handled": true|false}`. Only claims repositories recognized by the provider; all other repositories cleanly fall back to native git worktrees.
  - `resolve --repo <root> --branch <name>`: Returns `{"path": "<checkout_path>"}`.
  - `create --repo <root> --branch <name> --base <ref>`: Provisions the workspace and returns `{"path": "<created_path>"}`.
  - `list --repo <root>`: Returns `[{"path":"...", "branch":"...", "detached":bool}]`.
  - `remove --path <checkout> [--force]`: Removes the workspace through the provider's cleanup logic.
- **Configuration**:
  - `~/.config/tuios/config.toml`:
    ```toml
    [worktrees]
    provider_command = "/path/to/provider-command"
    ```
  - Environment variables (checked in precedence order):
    1. `TUIOS_WORKTREES_PROVIDER_COMMAND`
    2. `HERDR_WORKTREES_PROVIDER_COMMAND` (for seamless parity with Herdr wrappers)

---

## How to work with patches

### Applying patches to a fresh upstream checkout

To apply all patches in sequence onto a pristine checkout:

```sh
./patches/apply.sh
```

To test whether the patches apply without modifying files:

```sh
git apply --check patches/*.patch
```

### Re-rolling onto new upstream releases

When upstream adds new commits:

1. **Fetch upstream changes**:
   ```sh
   git fetch upstream
   ```
2. **Rebase the feature branch onto `upstream/main`**:
   ```sh
   ./scripts/rebase.sh upstream/main
   ```
   *(Note: `scripts/rebase.sh` automatically resolves skill archive conflicts if present).*
3. **Re-export the patch stack**:
   ```sh
   ./patches/reroll.sh
   ```
4. **Verify checks and tests**:
   ```sh
   go test ./internal/worktree/... ./internal/session/... -run Provider
   ./scripts/check.sh --changed upstream/main
   ```
5. **Reinstall local build**:
   ```sh
   ./scripts/install.sh pure --prefix ~/go/bin --keep-server
   ```

### Adding a new patch

1. Create a new branch off `feat/worktree-provider` (or rebase onto it).
2. Commit your changes with conventional commit messages (`feat(...)`, `fix(...)`).
3. Generate the numbered patch file:
   ```sh
   git format-patch -1 <commit-sha> -o patches/
   ```
4. Update `patches/README.md` and `patches/apply.sh` to include the new patch.
