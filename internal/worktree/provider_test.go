package worktree

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func writeMockProvider(t *testing.T, handledRepo, createdPath string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("mock provider shell script requires unix")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "provider.sh")
	content := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
verb="${1:-}"
shift || true
repo=""
branch=""
base=""
path=""
force=0

while [ $# -gt 0 ]; do
  case "$1" in
    --repo) repo="$2"; shift 2 ;;
    --branch) branch="$2"; shift 2 ;;
    --base) base="$2"; shift 2 ;;
    --path) path="$2"; shift 2 ;;
    --force) force=1; shift ;;
    *) shift ;;
  esac
done

case "$verb" in
  detect)
    if [ "$repo" = "%s" ]; then
      printf '{"handled":true}\n'
    else
      printf '{"handled":false}\n'
    fi
    ;;
  resolve)
    printf '{"path":"%s"}\n'
    ;;
  create)
    printf '{"path":"%s"}\n'
    ;;
  list)
    printf '[{"path":"%s","branch":"main","detached":false}]\n'
    ;;
  remove)
    printf '{}\n'
    ;;
  *)
    echo "unknown verb $verb" >&2
    exit 2
    ;;
esac
`, handledRepo, createdPath, createdPath, handledRepo)

	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func TestProviderCommandEnv(t *testing.T) {
	SetProviderCommand("")
	defer SetProviderCommand("")

	t.Setenv("TUIOS_WORKTREES_PROVIDER_COMMAND", "")
	t.Setenv("HERDR_WORKTREES_PROVIDER_COMMAND", "")
	if got := ProviderCommand(); got != "" {
		t.Errorf("ProviderCommand() = %q, want empty", got)
	}

	t.Setenv("HERDR_WORKTREES_PROVIDER_COMMAND", "/path/to/herdr-provider")
	if got := ProviderCommand(); got != "/path/to/herdr-provider" {
		t.Errorf("ProviderCommand() = %q, want herdr fallback", got)
	}

	t.Setenv("TUIOS_WORKTREES_PROVIDER_COMMAND", "/path/to/tuios-provider")
	if got := ProviderCommand(); got != "/path/to/tuios-provider" {
		t.Errorf("ProviderCommand() = %q, want tuios override", got)
	}

	SetProviderCommand("/path/to/custom-provider")
	if got := ProviderCommand(); got != "/path/to/custom-provider" {
		t.Errorf("ProviderCommand() = %q, want custom provider", got)
	}
}

func TestProviderLifecycle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}
	handledRepo := "/tmp/monorepo/workspaces/root"
	createdPath := "/tmp/monorepo/workspaces/test-branch"
	script := writeMockProvider(t, handledRepo, createdPath)

	SetProviderCommand(script)
	defer SetProviderCommand("")

	p := ActiveProvider()
	if p == nil {
		t.Fatal("ActiveProvider() is nil, want configured provider")
	}

	// Test Handles
	handled, err := p.Handles(handledRepo)
	if err != nil {
		t.Fatalf("Handles(%q) failed: %v", handledRepo, err)
	}
	if !handled {
		t.Errorf("Handles(%q) = false, want true", handledRepo)
	}

	otherRepo := "/tmp/other/repo"
	handled, err = p.Handles(otherRepo)
	if err != nil {
		t.Fatalf("Handles(%q) failed: %v", otherRepo, err)
	}
	if handled {
		t.Errorf("Handles(%q) = true, want false", otherRepo)
	}

	// Test Resolve
	resPath, err := p.Resolve(handledRepo, "test-branch")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if resPath != createdPath {
		t.Errorf("Resolve = %q, want %q", resPath, createdPath)
	}

	// Test Create
	newPath, err := p.Create(handledRepo, "test-branch", "HEAD")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if newPath != createdPath {
		t.Errorf("Create = %q, want %q", newPath, createdPath)
	}

	// Test List
	entries, err := p.List(handledRepo)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(entries) != 1 || entries[0].Path != handledRepo {
		t.Errorf("List = %+v, want single entry for %s", entries, handledRepo)
	}

	// Test Remove
	if err := p.Remove(createdPath, false); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}
}

func TestProviderIntegrationWithListAndRemove(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}
	handledRepo := "/tmp/monorepo/workspaces/root"
	createdPath := "/tmp/monorepo/workspaces/test-branch"
	script := writeMockProvider(t, handledRepo, createdPath)

	SetProviderCommand(script)
	defer SetProviderCommand("")

	// List delegates to provider when provider handles the directory
	entries, err := List(handledRepo)
	if err != nil {
		t.Fatalf("List(%q) failed: %v", handledRepo, err)
	}
	if len(entries) != 1 || entries[0].Path != handledRepo {
		t.Errorf("List(%q) = %+v, want provider entries", handledRepo, entries)
	}

	// Remove delegates to provider when provider handles the path
	if err := Remove(handledRepo, createdPath, true); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}
}
