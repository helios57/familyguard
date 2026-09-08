package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// config is what `fgctl login` writes and every other command reads.
//
// It holds a credential, so it is written 0600 and never printed. `fgctl config` shows the server
// and whether a token is present -- never the token, not even a prefix: a prefix is enough to
// identify a key in a leaked terminal recording, and there is no question a prefix answers that
// "present" does not.
type config struct {
	BaseURL string `json:"base_url"`
	Token   string `json:"token"`
}

// configDir resolves per-platform through os.UserConfigDir, which is the whole of the Windows
// support story for configuration: %AppData%\FamilyGuard on Windows, ~/.config/familyguard on
// Linux, ~/Library/Application Support/familyguard on macOS. Hand-building "~/.config" would have
// worked on this machine and silently written to the wrong place everywhere else.
func configDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locating the user config directory: %w", err)
	}
	name := "familyguard"
	if runtime.GOOS == "windows" {
		// %AppData% holds vendor-cased directory names, not lowercase unix ones.
		name = "FamilyGuard"
	}
	return filepath.Join(base, name), nil
}

func configPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// loadConfig reads the stored configuration and lets the environment override it.
//
// The environment wins so a CI job or a container can run fgctl with no file at all, and so a
// second server can be addressed without disturbing the saved one.
func loadConfig() (config, error) {
	var cfg config
	path, err := configPath()
	if err != nil {
		return cfg, err
	}
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return cfg, fmt.Errorf("%s is not valid JSON: %w", path, err)
		}
	case errors.Is(err, os.ErrNotExist):
		// Not an error. A first run, or a purely environment-driven one.
	default:
		return cfg, fmt.Errorf("reading %s: %w", path, err)
	}
	if v := strings.TrimSpace(os.Getenv("FAMILYGUARD_URL")); v != "" {
		cfg.BaseURL = v
	}
	if v := strings.TrimSpace(os.Getenv("FAMILYGUARD_TOKEN")); v != "" {
		cfg.Token = v
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	return cfg, nil
}

// saveConfig writes the file 0600, creating the directory 0700.
//
// It writes to a temporary file in the same directory and renames, so an interrupted write cannot
// leave a truncated file that reads as "logged in with an empty token" -- which would then fail as
// a 401 and send the user looking at the server.
//
// The 0600 is honoured on unix and is inert on Windows, where the file inherits the ACL of a
// per-user directory under %AppData%. That is the platform's answer rather than ours, and it is
// worth stating: this code does not make the file unreadable to another Windows account by itself.
func saveConfig(cfg config) (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating %s: %w", dir, err)
	}
	path := filepath.Join(dir, "config.json")
	encoded, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encoding the configuration: %w", err)
	}
	encoded = append(encoded, '\n')

	temp, err := os.CreateTemp(dir, "config-*.json")
	if err != nil {
		return "", fmt.Errorf("creating a temporary file in %s: %w", dir, err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName) // no-op once the rename has succeeded

	// Chmod before writing, so the credential is never briefly present in a world-readable file.
	if err := temp.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		temp.Close()
		return "", fmt.Errorf("securing %s: %w", tempName, err)
	}
	if _, err := temp.Write(encoded); err != nil {
		temp.Close()
		return "", fmt.Errorf("writing %s: %w", tempName, err)
	}
	if err := temp.Close(); err != nil {
		return "", fmt.Errorf("closing %s: %w", tempName, err)
	}
	// Windows will not rename onto an existing file; remove it first. On unix the rename is atomic
	// and this Remove is a harmless no-op on a path that is about to be replaced anyway.
	if runtime.GOOS == "windows" {
		_ = os.Remove(path)
	}
	if err := os.Rename(tempName, path); err != nil {
		return "", fmt.Errorf("installing %s: %w", path, err)
	}
	return path, nil
}
