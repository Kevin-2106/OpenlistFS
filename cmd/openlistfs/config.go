package main

import (
	cfgpkg "openlistfs/internal/config"
)

// loadConfig reads OPENLISTFS_* from the environment; a .env file in the
// working directory (KEY=VALUE lines) provides defaults for unset variables.
func loadConfig() (cfgpkg.Config, error) { return cfgpkg.Load() }
