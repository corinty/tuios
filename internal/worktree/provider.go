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

	"github.com/Gaurav-Gosain/tuios/internal/childproc"
	"github.com/Gaurav-Gosain/tuios/internal/gitstate"
)

var customProviderCommand string

// SetProviderCommand overrides the command line of the external worktree provider.
// It is intended for testing and runtime configuration hooks.
func SetProviderCommand(cmd string) {
	customProviderCommand = cmd
}

// ProviderCommand returns the configured worktree provider command, if any.
// It checks customProviderCommand first, then TUIOS_WORKTREES_PROVIDER_COMMAND,
// and falls back to HERDR_WORKTREES_PROVIDER_COMMAND for compatibility with tools
// configured for herdr.
func ProviderCommand() string {
	if customProviderCommand != "" {
		return customProviderCommand
	}
	if cmd := os.Getenv("TUIOS_WORKTREES_PROVIDER_COMMAND"); cmd != "" {
		return cmd
	}
	if cmd := os.Getenv("HERDR_WORKTREES_PROVIDER_COMMAND"); cmd != "" {
		return cmd
	}
	return ""
}

// Provider manages worktrees through an external program instead of git worktree.
// It follows the JSON-RPC contract used by Herdr:
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
	cmd := strings.TrimSpace(ProviderCommand())
	if cmd == "" {
		return nil
	}
	return &Provider{Command: cmd}
}

// run invokes the provider command with verb and args, capturing stdout and stderr.
func (p *Provider) run(ctx context.Context, verb string, args ...string) ([]byte, error) {
	fullArgs := append([]string{verb}, args...)
	cmd := childproc.CommandContext(ctx, p.Command, fullArgs...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("worktree provider %s failed: %s", verb, msg)
	}
	return stdout.Bytes(), nil
}

// Handles reports whether the provider manages worktrees for the repository anchor.
func (p *Provider) Handles(repoRoot string) (bool, error) {
	out, err := p.run(context.Background(), "detect", "--repo", repoRoot)
	if err != nil {
		return false, err
	}
	var res struct {
		Handled bool `json:"handled"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return false, fmt.Errorf("invalid json from worktree provider detect: %w", err)
	}
	return res.Handled, nil
}

// Resolve asks the provider where a worktree for branch would land.
func (p *Provider) Resolve(repoRoot, branch string) (string, error) {
	out, err := p.run(context.Background(), "resolve", "--repo", repoRoot, "--branch", branch)
	if err != nil {
		return "", err
	}
	var res struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return "", fmt.Errorf("invalid json from worktree provider resolve: %w", err)
	}
	if res.Path == "" {
		return "", errors.New("worktree provider resolve returned empty path")
	}
	return res.Path, nil
}

// Create creates a worktree for branch from base, returning the materialized checkout path.
func (p *Provider) Create(repoRoot, branch, base string) (string, error) {
	args := []string{"--repo", repoRoot, "--branch", branch}
	if base != "" {
		args = append(args, "--base", base)
	}
	out, err := p.run(context.Background(), "create", args...)
	if err != nil {
		return "", err
	}
	var res struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return "", fmt.Errorf("invalid json from worktree provider create: %w", err)
	}
	if res.Path == "" {
		return "", errors.New("worktree provider create returned empty path")
	}
	return res.Path, nil
}

// List lists the worktrees known to the provider for the repository.
func (p *Provider) List(repoRoot string) ([]Entry, error) {
	out, err := p.run(context.Background(), "list", "--repo", repoRoot)
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
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("invalid json from worktree provider list: %w", err)
	}
	entries := make([]Entry, 0, len(raw))
	for _, r := range raw {
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

// Remove removes the worktree at path.
func (p *Provider) Remove(path string, force bool) error {
	args := []string{"--path", path}
	if force {
		args = append(args, "--force")
	}
	_, err := p.run(context.Background(), "remove", args...)
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
