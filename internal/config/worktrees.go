package config

// WorktreesConfig is the [worktrees] section of config.toml.
type WorktreesConfig struct {
	// Directory is the root under which tuios puts worktrees it creates.
	// Empty means default ($XDG_DATA_HOME/tuios/worktrees or TUIOS_WORKTREE_DIR).
	Directory string `toml:"directory"`
	// ProviderCommand is an optional external program that manages worktrees
	// instead of native git. When set, tuios delegates create/list/remove to it
	// using the external provider JSON contract.
	ProviderCommand string `toml:"provider_command"`
}
