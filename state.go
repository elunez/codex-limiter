package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const persistedStateVersion = 1

type persistedState struct {
	Version   int                        `json:"version"`
	Settings  GlobalSettings             `json:"settings"`
	Overrides map[string]AccountOverride `json:"overrides"`
}

func loadState(path string, defaults GlobalSettings) (persistedState, error) {
	state := persistedState{Version: persistedStateVersion, Settings: defaults, Overrides: make(map[string]AccountOverride)}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, fmt.Errorf("read state: %w", err)
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return state, fmt.Errorf("decode state: %w", err)
	}
	settings, err := normalizeGlobalSettings(state.Settings)
	if err != nil {
		return state, fmt.Errorf("invalid persisted settings: %w", err)
	}
	state.Settings = settings
	if state.Overrides == nil {
		state.Overrides = make(map[string]AccountOverride)
	}
	for authID, override := range state.Overrides {
		if override.UseGlobal {
			delete(state.Overrides, authID)
			continue
		}
		normalized, err := normalizeAccountSettings(override.Settings)
		if err != nil {
			return state, fmt.Errorf("invalid override for %s: %w", authID, err)
		}
		override.Settings = normalized
		state.Overrides[authID] = override
	}
	state.Version = persistedStateVersion
	return state, nil
}

func writeState(path string, state persistedState) error {
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("create state file: %w", err)
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(raw); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("replace state: %w", err)
	}
	return nil
}
