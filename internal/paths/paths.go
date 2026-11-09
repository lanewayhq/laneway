// Package paths decides where the daemon keeps its state, its socket and the
// global config, from the user's environment (HOME and the XDG base directory
// variables).
package paths

import (
	"errors"
	"path/filepath"
)

// Paths are the on-disk locations shared by the daemon and the CLI.
type Paths struct {
	// StateDir holds persistent state such as the Registry.
	StateDir string
	// Socket is the Unix socket the daemon serves its API on.
	Socket string
	// ConfigDir holds the global config file.
	ConfigDir string
}

// FromEnv resolves Paths from environment variables read through getenv:
//
//   - state:  $XDG_STATE_HOME/laneway, else $HOME/.local/state/laneway
//   - socket: $XDG_RUNTIME_DIR/laneway/laneway.sock, else <state>/run/laneway.sock
//   - config: $XDG_CONFIG_HOME/laneway, else $HOME/.config/laneway
func FromEnv(getenv func(string) string) (Paths, error) {
	home := getenv("HOME")
	stateHome := getenv("XDG_STATE_HOME")
	if stateHome == "" {
		if home == "" {
			return Paths{}, errors.New("cannot find Laneway's state directory: neither XDG_STATE_HOME nor HOME is set")
		}
		stateHome = filepath.Join(home, ".local", "state")
	}
	p := Paths{StateDir: filepath.Join(stateHome, "laneway")}
	if configHome := getenv("XDG_CONFIG_HOME"); configHome != "" {
		p.ConfigDir = filepath.Join(configHome, "laneway")
	} else if home != "" {
		p.ConfigDir = filepath.Join(home, ".config", "laneway")
	}

	if runtimeDir := getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		p.Socket = filepath.Join(runtimeDir, "laneway", "laneway.sock")
	} else {
		p.Socket = filepath.Join(p.StateDir, "run", "laneway.sock")
	}
	return p, nil
}

// Registry is the file the Registry is persisted to.
func (p Paths) Registry() string {
	return filepath.Join(p.StateDir, "registry.json")
}

// CertStorage is the directory the proxy keeps Laneway's local CA and every
// issued certificate in.
func (p Paths) CertStorage() string {
	return filepath.Join(p.StateDir, "certs")
}

// CARootCert is Laneway's local root CA certificate (PEM), the one file a
// browser or client needs to trust Laneway's Domains.
func (p Paths) CARootCert() string {
	return filepath.Join(p.CertStorage(), "pki", "authorities", "local", "root.crt")
}

// Logs is the directory Process log files are written under, one directory
// per Worktree named after its Domain.
func (p Paths) Logs() string {
	return filepath.Join(p.StateDir, "logs")
}

// GlobalConfig is the global config file, with settings for every Project
// (see config.Global). It is "" if neither XDG_CONFIG_HOME nor HOME is set.
func (p Paths) GlobalConfig() string {
	if p.ConfigDir == "" {
		return ""
	}
	return filepath.Join(p.ConfigDir, "config.toml")
}

// Setup is the file the Preparer records Worktrees' states in (Ready,
// Failed).
func (p Paths) Setup() string {
	return filepath.Join(p.StateDir, "setup.json")
}

// Plugins is the directory installed Plugins are kept in, one directory
// each, named after the Plugin.
func (p Paths) Plugins() string {
	return filepath.Join(p.StateDir, "plugins")
}

// PluginCache is the directory Plugins' compiled WASM modules are kept in,
// so a module is compiled once, not every time Laneway starts.
func (p Paths) PluginCache() string {
	return filepath.Join(p.StateDir, "plugin-cache")
}
