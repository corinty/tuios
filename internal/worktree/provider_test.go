package worktree

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Gaurav-Gosain/tuios/internal/testutil"
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
	createdPath := fakeCheckout(t)
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

// writeScript writes an executable bash script and returns its path.
func writeScript(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("provider scripts need bash")
	}
	path := filepath.Join(t.TempDir(), "provider.sh")
	if err := os.WriteFile(path, []byte("#!/usr/bin/env bash\nset -euo pipefail\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// fakeCheckout makes a folder that looks like a git checkout to checkCheckout.
func fakeCheckout(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /nowhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestProviderDetectIsCached(t *testing.T) {
	count := filepath.Join(t.TempDir(), "count")
	script := writeScript(t, fmt.Sprintf(`echo x >> %q
printf '{"handled":true}\n'
`, count))
	SetProviderCommand(script)
	defer SetProviderCommand("")

	calls := func() int {
		b, _ := os.ReadFile(count)
		return strings.Count(string(b), "x")
	}
	p := ActiveProvider()
	for range 3 {
		if handled, err := p.Handles("/repo"); err != nil || !handled {
			t.Fatalf("Handles = %v, %v; want true, nil", handled, err)
		}
	}
	if got := calls(); got != 1 {
		t.Errorf("detect ran %d times for three asks, want 1", got)
	}
	if _, err := p.Handles("/other"); err != nil {
		t.Fatal(err)
	}
	if got := calls(); got != 2 {
		t.Errorf("detect ran %d times after a second repository, want 2", got)
	}
	SetProviderCommand(script)
	if _, err := p.Handles("/repo"); err != nil {
		t.Fatal(err)
	}
	if got := calls(); got != 3 {
		t.Errorf("detect ran %d times after the config was set again, want 3", got)
	}
}

func TestProviderDetectErrorIsNotCached(t *testing.T) {
	flag := filepath.Join(t.TempDir(), "ok")
	script := writeScript(t, fmt.Sprintf(`[ -e %q ] || { echo "not ready" >&2; exit 1; }
printf '{"handled":true}\n'
`, flag))
	SetProviderCommand(script)
	defer SetProviderCommand("")

	p := ActiveProvider()
	if _, err := p.Handles("/repo"); err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("Handles error = %v, want the provider's stderr", err)
	}
	if err := os.WriteFile(flag, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if handled, err := p.Handles("/repo"); err != nil || !handled {
		t.Fatalf("Handles after the provider recovered = %v, %v; want true, nil", handled, err)
	}
}

func TestProviderTimeoutEndsTheCall(t *testing.T) {
	script := writeScript(t, "sleep 30\n")
	SetProviderCommand(script)
	defer SetProviderCommand("")
	old := providerQueryTimeout
	providerQueryTimeout = 200 * time.Millisecond
	defer func() { providerQueryTimeout = old }()

	start := time.Now()
	_, err := ActiveProvider().Handles("/repo")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("Handles error = %v, want a timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Handles took %s, want it ended near the timeout", elapsed)
	}
}

func TestProviderCreateRejectsWhatIsNotACheckout(t *testing.T) {
	plain := t.TempDir()
	cases := map[string]string{
		"empty":    "",
		"relative": "trees/x/src",
		"missing":  filepath.Join(t.TempDir(), "gone"),
		"no git":   plain,
	}
	for name, out := range cases {
		t.Run(name, func(t *testing.T) {
			script := writeScript(t, fmt.Sprintf("printf '{\"path\":\"%%s\"}\\n' %q\n", out))
			p := &Provider{Command: script}
			if path, err := p.Create("/repo", "b", ""); err == nil {
				t.Fatalf("Create = %q, nil; want an error for %q", path, out)
			}
		})
	}
	good := fakeCheckout(t)
	script := writeScript(t, fmt.Sprintf("printf '{\"path\":\"%%s\"}\\n' %q\n", good+"/"))
	path, err := (&Provider{Command: script}).Create("/repo", "b", "")
	if err != nil || path != good {
		t.Fatalf("Create = %q, %v; want %q, nil", path, err, good)
	}
}

func TestProviderCreatesOneAtATime(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "lock")
	out := fakeCheckout(t)
	script := writeScript(t, fmt.Sprintf(`mkdir %q 2>/dev/null || { echo "two creates at once" >&2; exit 1; }
sleep 0.2
rmdir %q
printf '{"path":"%%s"}\n' %q
`, lock, lock, out))
	p := &Provider{Command: script}

	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for i := range 3 {
		wg.Go(func() {
			if _, err := p.Create("/repo", fmt.Sprintf("b%d", i), ""); err != nil {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestListFallsBackToGitWhenTheProviderFails(t *testing.T) {
	repo := testutil.GitRepo(t)
	script := writeScript(t, `case "$1" in
  detect) printf '{"handled":true}\n' ;;
  *) echo "broken" >&2; exit 1 ;;
esac
`)
	SetProviderCommand(script)
	defer SetProviderCommand("")

	entries, err := List(repo)
	if err != nil {
		t.Fatalf("List = %v, want git's list", err)
	}
	if len(entries) == 0 || entries[0].Path == "" {
		t.Fatalf("List = %+v, want the main checkout from git", entries)
	}
}

func TestRemoveStopsWhenTheProviderCannotAnswer(t *testing.T) {
	script := writeScript(t, `echo "broken" >&2; exit 1
`)
	SetProviderCommand(script)
	defer SetProviderCommand("")

	if err := Remove(t.TempDir(), t.TempDir(), false); err == nil || !strings.Contains(err.Error(), "broken") {
		t.Fatalf("Remove error = %v, want the provider's error, not a git removal", err)
	}
}

func TestProviderCommandExpandsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	SetProviderCommand("~/bin/provider")
	defer SetProviderCommand("")
	if got, want := ProviderCommand(), filepath.Join(home, "bin", "provider"); got != want {
		t.Errorf("ProviderCommand = %q, want %q", got, want)
	}
}

func TestValidateProviderCommand(t *testing.T) {
	exe := writeScript(t, "exit 0\n")
	plain := filepath.Join(t.TempDir(), "plain")
	if err := os.WriteFile(plain, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	cases := map[string]bool{
		"":                                 true,
		exe:                                true,
		"provider":                         false,
		filepath.Join(t.TempDir(), "gone"): false,
		t.TempDir():                        false,
		plain:                              false,
	}
	for cmd, ok := range cases {
		if err := ValidateProviderCommand(cmd); (err == nil) != ok {
			t.Errorf("ValidateProviderCommand(%q) = %v, want ok=%v", cmd, err, ok)
		}
	}
}

func TestTailKeepsTheEnd(t *testing.T) {
	s := strings.Repeat("line\n", 1000) + "the reason"
	got := tail(s, 64)
	if !strings.HasSuffix(got, "the reason") || len(got) > 64+len("…") {
		t.Errorf("tail = %q, want the last 64 bytes ending in the reason", got)
	}
}
