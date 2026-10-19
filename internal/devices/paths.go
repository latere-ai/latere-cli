// SPDX-FileCopyrightText: 2026 Latere AI
// SPDX-License-Identifier: MIT

// Package devices is the engine of `latere devices` (spec 016): what this
// computer keeps to serve as one of its owner's environments. It holds the
// host policy file, the environment key in the keychain, the activity log,
// the approvals of each session, the lock that keeps one engine per
// computer, the control socket a desktop application drives, and the loop
// that runs Cella's embedded worker on its local driver.
//
// The engine is the one implementation of all of it. `latere devices
// connect` runs it with a terminal client in the same process, and `latere
// devices serve` runs it alone for a client that speaks to the control
// socket.
package devices

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/latere-ai/latere-cli/internal/api"
	"github.com/latere-ai/latere-cli/internal/config"
)

// Paths are where the engine keeps its files, and what it denies to every
// session on top of the local driver's own deny table.
type Paths struct {
	// Home is the person's home directory.
	Home string
	// Config is the CLI's configuration directory, ~/.config/latere: the
	// login token, the host policy file and the key fallback.
	Config string
	// Token is the login token file, which LATERE_AUTH_TOKEN_FILE may place
	// outside Config.
	Token string
	// State is $XDG_STATE_HOME/latere: the activity logs, the lock and the
	// control socket.
	State string
	// Cache is $XDG_CACHE_HOME/latere, or the operating system's cache
	// directory: the sandboxes' scratch directories.
	Cache string
	// KeysFile, when set, is the file the environment key is kept in
	// instead of the keychain: LATERE_DEVICE_KEYS_FILE, for a host whose
	// keychain cannot answer without a person at it.
	KeysFile string
}

// EnvKeysFile names the file the environment key is kept in instead of the
// keychain.
const EnvKeysFile = "LATERE_DEVICE_KEYS_FILE"

// DefaultPaths resolves the paths from the environment: XDG_CONFIG_HOME,
// XDG_STATE_HOME and XDG_CACHE_HOME when set, the platform's defaults
// otherwise.
func DefaultPaths() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, err
	}
	p := Paths{Home: home, Config: config.Dir(), Token: api.AuthTokenPath(), KeysFile: os.Getenv(EnvKeysFile)}
	if p.Config == "" {
		return Paths{}, errors.New("the configuration directory cannot be resolved")
	}
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		state = filepath.Join(home, ".local", "state")
	}
	p.State = filepath.Join(state, "latere")
	cache := os.Getenv("XDG_CACHE_HOME")
	if cache == "" {
		if cache, err = os.UserCacheDir(); err != nil {
			return Paths{}, err
		}
	}
	p.Cache = filepath.Join(cache, "latere")
	return p, nil
}

// PolicyFile is the host policy file, device.yaml beside the login token.
func (p Paths) PolicyFile() string { return filepath.Join(p.Config, "device.yaml") }

// KeyFile is where the environment key is kept when no keychain answers,
// or always when KeysFile names a file.
func (p Paths) KeyFile() string {
	if p.KeysFile != "" {
		return p.KeysFile
	}
	return filepath.Join(p.Config, "device-keys.json")
}

// Keys is the store of the environment key: the keychain with the file as
// its fallback, or the file alone when KeysFile names one.
func (p Paths) Keys() *Keys {
	if p.KeysFile != "" {
		return &Keys{Keychain: noKeychain{}, File: KeyFile{Path: p.KeysFile}}
	}
	return OpenKeys(p.KeyFile())
}

// Devices is the engine's own state directory, mode 0700: the lock, the
// control socket, and one directory of activity per environment.
func (p Paths) Devices() string { return filepath.Join(p.State, "devices") }

// Socket is the control socket.
func (p Paths) Socket() string { return filepath.Join(p.Devices(), "control.sock") }

// LockFile is what one engine holds while it serves this computer.
func (p Paths) LockFile() string { return filepath.Join(p.Devices(), "lock") }

// OwnerFile records whose device this computer serves, for an engine that
// takes its tokens from a client: in the state directory, which no session
// reaches, and apart from the host policy file, which every version reads.
func (p Paths) OwnerFile() string { return filepath.Join(p.Devices(), "owner.json") }

// ActivityDir holds one environment's activity log.
func (p Paths) ActivityDir(environment string) string {
	return filepath.Join(p.Devices(), environment)
}

// DataDir holds one environment's sandboxes, the local driver's data
// directory.
func (p Paths) DataDir(environment string) string {
	return filepath.Join(p.Cache, "devices", environment)
}

// Deny is what the engine denies every session on top of the local
// driver's always-deny table: its own configuration (the login token, the
// host policy, the key fallback) and state (the activity logs, the lock,
// the control socket), the operating system's keychains, and the browsers'
// profiles. The cache directory is not denied, since the sandboxes run
// there.
func (p Paths) Deny(goos string) []string {
	deny := []string{p.Config, p.State}
	for _, own := range []string{p.Token, p.KeysFile} {
		if own != "" && filepath.Dir(own) != p.Config {
			deny = append(deny, own)
		}
	}
	home := func(elem ...string) string { return filepath.Join(append([]string{p.Home}, elem...)...) }
	switch goos {
	case "darwin":
		support := home("Library", "Application Support")
		deny = append(deny,
			home("Library", "Keychains"),
			filepath.Join(support, "Google", "Chrome"),
			filepath.Join(support, "Chromium"),
			filepath.Join(support, "Firefox"),
			filepath.Join(support, "Microsoft Edge"),
			filepath.Join(support, "BraveSoftware"),
			filepath.Join(support, "Arc"),
			home("Library", "Safari"),
		)
	case "linux":
		deny = append(deny,
			home(".local", "share", "keyrings"),
			home(".mozilla"),
			home(".config", "google-chrome"),
			home(".config", "chromium"),
			home(".config", "microsoft-edge"),
			home(".config", "BraveSoftware"),
		)
	}
	return deny
}
