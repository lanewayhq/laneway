package platform

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Caddy's own trust installation is switched off (install_trust: false in
// package proxy): it knows nothing of Windows, so Laneway trusts its root here,
// in every store the user's browsers read. Only the root is trusted; Caddy
// rotates the intermediate.

func (l *Linux) TrustStores() []TrustStore {
	stores := []TrustStore{linuxStore{l}}
	if l.WSL {
		stores = append(stores, windowsStore{l})
	}
	return stores
}

// caFileName is what the root is called in every store directory.
const caFileName = "laneway-local-ca.crt"

// linuxAnchors are the distributions' directories for extra trusted roots,
// and the command that rebuilds the system bundle from them.
var linuxAnchors = []struct {
	dir    string
	update []string
}{
	{"/etc/ca-certificates/trust-source/anchors", []string{"update-ca-trust", "extract"}}, // Arch (p11-kit)
	{"/etc/pki/ca-trust/source/anchors", []string{"update-ca-trust", "extract"}},          // Fedora, RHEL
	{"/usr/local/share/ca-certificates", []string{"update-ca-certificates"}},              // Debian, Ubuntu
	{"/usr/share/pki/trust/anchors", []string{"update-ca-certificates"}},                  // openSUSE
}

// linuxStore is the system CA bundle, which curl, Go, PHP and Node use, and
// which Chrome and Firefox read through p11-kit on distributions that wire
// NSS to it (Arch, Fedora).
type linuxStore struct{ l *Linux }

func (s linuxStore) Name() string { return "Linux system store" }

func (s linuxStore) anchorDir() (dir string, update []string, err error) {
	for _, a := range linuxAnchors {
		if info, err := os.Stat(s.l.sys(a.dir)); err == nil && info.IsDir() {
			return a.dir, a.update, nil
		}
	}
	return "", nil, errors.New("this distribution's CA anchor directory wasn't found; add Laneway's CA to the system store by hand")
}

func (s linuxStore) Prompt() string {
	return "adding Laneway's CA to the Linux system store. This needs sudo, so you may be asked for your password."
}

func (s linuxStore) Trusted(_ context.Context, ca *x509.Certificate) (bool, error) {
	dir, _, err := s.anchorDir()
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(s.l.sys(path.Join(dir, caFileName)))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	block, _ := pem.Decode(data)
	return block != nil && bytes.Equal(block.Bytes, ca.Raw), nil
}

func (s linuxStore) Trust(ctx context.Context, caFile string, _ *x509.Certificate) error {
	dir, update, err := s.anchorDir()
	if err != nil {
		return err
	}
	if err := s.l.sudo(ctx, "install", "-m", "0644", caFile, path.Join(dir, caFileName)); err != nil {
		return err
	}
	return s.l.sudo(ctx, update...)
}

// windowsStore is the Windows user's "Trusted Root Certification
// Authorities" store (CurrentUser\Root), which Chrome, Edge and (from its
// next start) Firefox trust. Adding to it needs no admin rights, but Windows
// always asks the user to confirm in a dialog; checking it is silent.
type windowsStore struct{ l *Linux }

func (s windowsStore) Name() string { return "Windows certificate store" }

func (s windowsStore) Prompt() string {
	return "adding Laneway's CA to the Windows certificate store (Current User). " +
		"Windows will show a Security Warning asking whether to install it: click Yes."
}

// thumbprint is how Windows names a certificate: its SHA-1, in hex.
func thumbprint(ca *x509.Certificate) string {
	sum := sha1.Sum(ca.Raw)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

func (s windowsStore) Trusted(ctx context.Context, ca *x509.Certificate) (bool, error) {
	out, err := s.l.powershell(ctx, `Test-Path Cert:\CurrentUser\Root\`+thumbprint(ca))
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "True", nil
}

func (s windowsStore) Trust(ctx context.Context, caFile string, ca *x509.Certificate) error {
	// certutil.exe needs the file on a Windows path.
	winTemp, err := s.l.powershell(ctx, "[IO.Path]::GetTempPath()")
	if err != nil {
		return fmt.Errorf("finding the Windows temp directory: %w", err)
	}
	winTemp = strings.TrimSpace(winTemp)
	linuxTemp, err := s.l.run(ctx, "wslpath", "-u", winTemp)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(caFile)
	if err != nil {
		return err
	}
	copied := filepath.Join(strings.TrimSpace(linuxTemp), caFileName)
	if err := os.WriteFile(copied, data, 0o644); err != nil {
		return err
	}
	defer os.Remove(copied)

	winFile := strings.TrimRight(winTemp, `\`) + `\` + caFileName
	if _, err := s.l.windows(ctx, "certutil.exe", "-user", "-addstore", "Root", winFile); err != nil {
		return fmt.Errorf("certutil.exe -addstore failed (was the dialog declined?): %w", err)
	}
	if trusted, err := s.Trusted(ctx, ca); err != nil || !trusted {
		return errors.New("Windows didn't add the CA; was the dialog declined? Run `lane install` again to retry")
	}
	return nil
}

// Windows programs, run from WSL through its interop.

// windowsDrive is where C: is mounted inside WSL.
const windowsDrive = "/mnt/c"

// windowsExe finds a Windows program on PATH, or in System32 when WSL's
// appendWindowsPath is off.
func (l *Linux) windowsExe(name, system32Rel string) string {
	if p, err := l.Run.LookPath(name); err == nil {
		return p
	}
	return l.sys(path.Join(windowsDrive, "Windows", "System32", system32Rel))
}

// windows runs a Windows program from a Windows directory, so it doesn't
// complain about a UNC working directory.
func (l *Linux) windows(ctx context.Context, name string, args ...string) (string, error) {
	out, err := l.Run.Run(ctx, Cmd{Name: l.windowsExe(name, name), Args: args, Dir: l.sys(windowsDrive)})
	return string(out), err
}

func (l *Linux) powershell(ctx context.Context, script string) (string, error) {
	out, err := l.Run.Run(ctx, Cmd{
		Name: l.windowsExe("powershell.exe", "WindowsPowerShell/v1.0/powershell.exe"),
		Args: []string{"-NoProfile", "-NonInteractive", "-Command", script},
		Dir:  l.sys(windowsDrive),
	})
	return string(out), err
}
