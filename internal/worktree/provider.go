// Package worktree manages git worktrees and external worktree providers.
package worktree

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Gaurav-Gosain/tuios/internal/childproc"
	"github.com/Gaurav-Gosain/tuios/internal/gitstate"
)

// How long a provider call may run before tuios ends it and its children.
// detect, resolve and list answer questions and must be quick. create and
// remove may set up or tear down a whole checkout, which in a large monorepo
// takes minutes. Variables so tests can shorten them.
var (
	providerQueryTimeout  = 30 * time.Second
	providerMutateTimeout = 20 * time.Minute
)

const (
	// detectTTL is how long a detect answer is kept. Whether a provider
	// claims a repository depends on where the repository is, which does not
	// change while it is open, so a few minutes saves a process per call.
	detectTTL = 5 * time.Minute
	// stderrTail is how much of a failed provider's stderr an error keeps.
	// A create can print a whole build log; the end is where the reason is.
	stderrTail = 2048
)

var (
	providerMu            sync.RWMutex
	customProviderCommand string

	detectMu    sync.Mutex
	detectCache = map[detectKey]detectEntry{}

	// mutateMu runs one create or remove at a time. A provider may find the
	// checkout it made by comparing a folder before and after, and two
	// creates at once would each see the other's.
	mutateMu sync.Mutex
)

type detectKey struct{ command, anchor string }

type detectEntry struct {
	handled bool
	at      time.Time
}

// SetProviderCommand sets the provider command from [worktree]
// provider_command. Empty falls back to the environment. Every call drops
// the cached detect answers, so a config change applies to the next call.
func SetProviderCommand(cmd string) {
	providerMu.Lock()
	customProviderCommand = cmd
	providerMu.Unlock()
	ResetProviderCache()
}

// ResetProviderCache drops every cached detect answer.
func ResetProviderCache() {
	detectMu.Lock()
	clear(detectCache)
	detectMu.Unlock()
}

// ProviderCommand returns the configured worktree provider command, if any.
// It checks [worktree] provider_command first, then
// TUIOS_WORKTREES_PROVIDER_COMMAND, and falls back to
// HERDR_WORKTREES_PROVIDER_COMMAND for tools set up for herdr. A leading ~/
// is the home folder.
func ProviderCommand() string {
	providerMu.RLock()
	cmd := customProviderCommand
	providerMu.RUnlock()
	if cmd == "" {
		cmd = os.Getenv("TUIOS_WORKTREES_PROVIDER_COMMAND")
	}
	if cmd == "" {
		cmd = os.Getenv("HERDR_WORKTREES_PROVIDER_COMMAND")
	}
	return expandHome(strings.TrimSpace(cmd))
}

func expandHome(p string) string {
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, rest)
		}
	}
	return p
}

// ValidateProviderCommand reports a provider command that cannot run: not
// found, a folder, or not executable. Empty is valid: no provider.
func ValidateProviderCommand(cmd string) error {
	cmd = expandHome(strings.TrimSpace(cmd))
	if cmd == "" {
		return nil
	}
	if !filepath.IsAbs(cmd) {
		return fmt.Errorf("%q is not an absolute path", cmd)
	}
	info, err := os.Stat(cmd)
	if err != nil {
		return fmt.Errorf("%q cannot be read: %w", cmd, err)
	}
	if info.IsDir() {
		return fmt.Errorf("%q is a folder", cmd)
	}
	if info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%q is not executable", cmd)
	}
	return nil
}

// Provider manages worktrees through an external program instead of git worktree.
// It follows the JSON contract used by Herdr:
//
//	<provider> detect  --repo <root>                              -> {"handled": true|false}
//	<provider> resolve --repo <root> --branch <name>             -> {"path": "..."}
//	<provider> create  --repo <root> --branch <name> --base <ref> -> {"path": "..."}
//	<provider> list    --repo <root>                              -> [{"path":"...", "branch":"...", ...}]
//	<provider> remove  --path <checkout> [--force]                -> {}
type Provider struct {
	Command string
}

// ActiveProvider returns a Provider instance if an external provider command is configured,
// or nil when worktrees should use native git worktree operations.
func ActiveProvider() *Provider {
	cmd := ProviderCommand()
	if cmd == "" {
		return nil
	}
	return &Provider{Command: cmd}
}

// ProviderFor returns the active provider and the checkout to run it from
// when the provider claims dir's repository. It returns nil when no provider
// is configured or the provider leaves the repository to git. An error means
// the provider could not answer.
func ProviderFor(dir string) (*Provider, string, error) {
	p := ActiveProvider()
	if p == nil {
		return nil, "", nil
	}
	anchor := FindAnchor(dir)
	handled, err := p.Handles(anchor)
	if err != nil {
		return nil, "", err
	}
	if !handled {
		return nil, "", nil
	}
	return p, anchor, nil
}

// run invokes the provider command with verb and args under timeout,
// capturing stdout and stderr. The provider runs in a process group of its
// own, so a timeout ends whatever it started too.
func (p *Provider) run(timeout time.Duration, verb string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	fullArgs := append([]string{verb}, args...)
	cmd := childproc.CommandContext(ctx, p.Command, fullArgs...)
	childproc.OwnGroup(cmd)
	cmd.WaitDelay = time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("worktree provider %s timed out after %s", verb, timeout)
		}
		msg := tail(strings.TrimSpace(stderr.String()), stderrTail)
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("worktree provider %s failed: %s", verb, msg)
	}
	return stdout.Bytes(), nil
}

// tail keeps the last n bytes of s, cut at a line start when it can be.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	if i := strings.IndexByte(s, '\n'); i >= 0 && i < len(s)-1 {
		s = s[i+1:]
	}
	return "…" + s
}

// decode reads the provider's JSON answer to verb into v.
func decode(verb string, out []byte, v any) error {
	if err := json.Unmarshal(bytes.TrimSpace(out), v); err != nil {
		return fmt.Errorf("worktree provider %s printed something other than JSON: %w", verb, err)
	}
	return nil
}

// Handles reports whether the provider manages worktrees for the repository
// anchor. Answers are cached for detectTTL; errors are not.
func (p *Provider) Handles(repoRoot string) (bool, error) {
	key := detectKey{p.Command, repoRoot}
	detectMu.Lock()
	if e, ok := detectCache[key]; ok && time.Since(e.at) < detectTTL {
		detectMu.Unlock()
		return e.handled, nil
	}
	detectMu.Unlock()

	out, err := p.run(providerQueryTimeout, "detect", "--repo", repoRoot)
	if err != nil {
		return false, err
	}
	var res struct {
		Handled bool `json:"handled"`
	}
	if err := decode("detect", out, &res); err != nil {
		return false, err
	}
	detectMu.Lock()
	detectCache[key] = detectEntry{handled: res.Handled, at: time.Now()}
	detectMu.Unlock()
	return res.Handled, nil
}

// Resolve asks the provider where a worktree for branch would land.
func (p *Provider) Resolve(repoRoot, branch string) (string, error) {
	out, err := p.run(providerQueryTimeout, "resolve", "--repo", repoRoot, "--branch", branch)
	if err != nil {
		return "", err
	}
	var res struct {
		Path string `json:"path"`
	}
	if err := decode("resolve", out, &res); err != nil {
		return "", err
	}
	if res.Path == "" {
		return "", errors.New("worktree provider resolve returned an empty path")
	}
	return res.Path, nil
}

// Create creates a worktree for branch from base, returning the materialized
// checkout path. Only one create or remove runs at a time. The path must be
// an absolute folder that holds a git checkout, so a provider that says it
// made something it did not is caught here rather than when a pane opens.
func (p *Provider) Create(repoRoot, branch, base string) (string, error) {
	args := []string{"--repo", repoRoot, "--branch", branch}
	if base != "" {
		args = append(args, "--base", base)
	}
	mutateMu.Lock()
	out, err := p.run(providerMutateTimeout, "create", args...)
	mutateMu.Unlock()
	if err != nil {
		return "", err
	}
	var res struct {
		Path string `json:"path"`
	}
	if err := decode("create", out, &res); err != nil {
		return "", err
	}
	if err := checkCheckout(res.Path); err != nil {
		return "", fmt.Errorf("worktree provider create: %w", err)
	}
	return filepath.Clean(res.Path), nil
}

// checkCheckout reports a path that is not an absolute folder holding a git
// checkout.
func checkCheckout(path string) error {
	if path == "" {
		return errors.New("returned an empty path")
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("returned %q, which is not an absolute path", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("returned %q, which is not there: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("returned %q, which is not a folder", path)
	}
	if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
		return fmt.Errorf("returned %q, which is not a git checkout", path)
	}
	return nil
}

// List lists the worktrees known to the provider for the repository.
func (p *Provider) List(repoRoot string) ([]Entry, error) {
	out, err := p.run(providerQueryTimeout, "list", "--repo", repoRoot)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Path     string `json:"path"`
		Branch   string `json:"branch"`
		Bare     bool   `json:"bare"`
		Detached bool   `json:"detached"`
		Prunable bool   `json:"prunable"`
	}
	if err := decode("list", out, &raw); err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(raw))
	for _, r := range raw {
		if r.Path == "" {
			continue
		}
		entries = append(entries, Entry{
			Path:     r.Path,
			Branch:   r.Branch,
			Bare:     r.Bare,
			Detached: r.Detached,
			Prunable: r.Prunable,
			Linked:   true,
		})
	}
	return entries, nil
}

// Remove removes the worktree at path. Only one create or remove runs at a
// time.
func (p *Provider) Remove(path string, force bool) error {
	args := []string{"--path", path}
	if force {
		args = append(args, "--force")
	}
	mutateMu.Lock()
	defer mutateMu.Unlock()
	_, err := p.run(providerMutateTimeout, "remove", args...)
	return err
}

// FindAnchor returns a working checkout directory for dir.
// If dir is inside a worktree checkout or main checkout, its checkout root is returned.
// If dir is a bare repository (commonDir), it inspects existing worktrees
// to find an existing checkout that can serve as the command anchor.
func FindAnchor(dir string) string {
	dir = filepath.Clean(dir)
	if _, _, root, ok := gitstate.Locate(dir); ok && root != "" {
		if _, err := os.Stat(filepath.Join(root, ".git")); err == nil {
			return root
		}
	}
	// Bare repo fallback: inspect linked worktrees to find an anchor checkout.
	if out, err := run(dir, "worktree", "list", "--porcelain"); err == nil {
		entries := parseWorktreeList(out)
		for _, e := range entries {
			if e.Linked && !e.Bare && e.Path != "" {
				if _, err := os.Stat(e.Path); err == nil {
					return e.Path
				}
			}
		}
	}
	return dir
}
