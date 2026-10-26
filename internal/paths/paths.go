// Package paths decides where the daemon keeps its state and its socket, from
// the user's environment (HOME and the XDG base directory variables).
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
}

// FromEnv resolves Paths from environment variables read through getenv:
//
//   - state:  $XDG_STATE_HOME/portby, else $HOME/.local/state/portby
//   - socket: $XDG_RUNTIME_DIR/portby/portby.sock, else <state>/run/portby.sock
func FromEnv(getenv func(string) string) (Paths, error) {
	stateHome := getenv("XDG_STATE_HOME")
	if stateHome == "" {
		home := getenv("HOME")
		if home == "" {
			return Paths{}, errors.New("cannot find Portby's state directory: neither XDG_STATE_HOME nor HOME is set")
		}
		stateHome = filepath.Join(home, ".local", "state")
	}
	p := Paths{StateDir: filepath.Join(stateHome, "portby")}

	if runtimeDir := getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		p.Socket = filepath.Join(runtimeDir, "portby", "portby.sock")
	} else {
		p.Socket = filepath.Join(p.StateDir, "run", "portby.sock")
	}
	return p, nil
}

// Registry is the file the Registry is persisted to.
func (p Paths) Registry() string {
	return filepath.Join(p.StateDir, "registry.json")
}

// CertStorage is the directory the proxy keeps Portby's local CA and every
// issued certificate in.
func (p Paths) CertStorage() string {
	return filepath.Join(p.StateDir, "certs")
}

// CARootCert is Portby's local root CA certificate (PEM), the one file a
// browser or client needs to trust Portby's Domains.
func (p Paths) CARootCert() string {
	return filepath.Join(p.CertStorage(), "pki", "authorities", "local", "root.crt")
}

// Logs is the directory Process log files are written under, one directory
// per Worktree named after its Domain.
func (p Paths) Logs() string {
	return filepath.Join(p.StateDir, "logs")
}

// Setup is the file the Provisioner records Worktrees' setup states in.
func (p Paths) Setup() string {
	return filepath.Join(p.StateDir, "setup.json")
}
