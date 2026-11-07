package main

import (
	"context"
	"errors"
	"os"

	"github.com/saileshramesh/terraform-drift-detector/internal/config"
	"github.com/saileshramesh/terraform-drift-detector/internal/store"
)

const (
	defaultConfigFile = "drift.yaml"
	defaultDatabase   = "tfdrift.db"
)

type globalFlags struct {
	configPath string
	dbPath     string
}

// loadedConfig remembers whether settings came from a file, so an absent
// file never wipes config-managed workspaces from the database.
type loadedConfig struct {
	*config.Config
	fromFile bool
}

func (g *globalFlags) loadConfig() (loadedConfig, error) {
	path := g.configPath
	if path == "" {
		if _, err := os.Stat(defaultConfigFile); err == nil {
			path = defaultConfigFile
		}
	}
	if path == "" {
		return loadedConfig{Config: &config.Config{}}, nil
	}
	cfg, err := config.Load(path)
	if err != nil {
		return loadedConfig{}, err
	}
	return loadedConfig{Config: cfg, fromFile: true}, nil
}

// openStore opens the database chosen by --db, then the config file, then
// fallback. It returns nil when none is set (fallback ""). Workspaces from
// the config file are synced into the database.
func (g *globalFlags) openStore(ctx context.Context, cfg loadedConfig, fallback string) (*store.Store, error) {
	path := g.dbPath
	if path == "" {
		path = cfg.Database
	}
	if path == "" {
		path = fallback
	}
	if path == "" {
		return nil, nil
	}
	st, err := store.Open(path)
	if err != nil {
		return nil, err
	}
	if cfg.fromFile {
		if err := st.SyncConfigWorkspaces(ctx, cfg.AllWorkspaces()); err != nil {
			st.Close()
			return nil, err
		}
	}
	return st, nil
}

// findWorkspace looks in the config file first, then the database.
func findWorkspace(ctx context.Context, cfg loadedConfig, st *store.Store, name string) (config.Workspace, error) {
	if ws, ok := cfg.Workspace(name); ok {
		return ws, nil
	}
	if st == nil {
		return config.Workspace{}, errors.New("workspace " + name + " is not in the config file (pass --db to look it up in a database)")
	}
	ws, err := st.GetWorkspace(ctx, name)
	if err != nil {
		return config.Workspace{}, err
	}
	return *ws, nil
}
