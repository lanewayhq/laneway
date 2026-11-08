package platform

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testCA makes a self-signed root and writes it as PEM to a temp file.
func testCA(t *testing.T) (*x509.Certificate, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Laneway Local CA - Test Root"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "root.crt")
	if err := os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	return cert, file
}

func storeNamed(t *testing.T, l *Linux, prefix string) TrustStore {
	t.Helper()
	for _, s := range l.TrustStores() {
		if strings.HasPrefix(s.Name(), prefix) {
			return s
		}
	}
	t.Fatalf("no %q trust store", prefix)
	return nil
}

func TestLinuxTrustStoresOnlyIncludeWindowsOnWSL(t *testing.T) {
	m := newMachine(t)
	if n := len(m.linux().TrustStores()); n != 1 {
		t.Errorf("Linux has %d trust stores, want 1", n)
	}
	if n := len(m.wsl().TrustStores()); n != 2 {
		t.Errorf("WSL2 has %d trust stores, want 2", n)
	}
}

func TestLinuxStoreAddsTheCAAsAnAnchor(t *testing.T) {
	tests := []struct{ distro, dir, update string }{
		{"Arch", "/etc/ca-certificates/trust-source/anchors", "sudo update-ca-trust extract"},
		{"Fedora", "/etc/pki/ca-trust/source/anchors", "sudo update-ca-trust extract"},
		{"Debian", "/usr/local/share/ca-certificates", "sudo update-ca-certificates"},
	}
	for _, tt := range tests {
		t.Run(tt.distro, func(t *testing.T) {
			ca, caFile := testCA(t)
			m := newMachine(t).dir(tt.dir)
			store := storeNamed(t, m.linux(), "Linux")

			if trusted, err := store.Trusted(t.Context(), ca); err != nil || trusted {
				t.Fatalf("Trusted before = %v, %v; want false", trusted, err)
			}
			if err := store.Trust(t.Context(), caFile, ca); err != nil {
				t.Fatal(err)
			}

			want := []string{"sudo install -m 0644 " + caFile + " " + tt.dir + "/laneway-local-ca.crt", tt.update}
			if got := m.run.lines(); strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Fatalf("ran\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}

			// What `sudo install` would have done.
			data, _ := os.ReadFile(caFile)
			m.file(tt.dir+"/laneway-local-ca.crt", string(data))
			if trusted, err := store.Trusted(t.Context(), ca); err != nil || !trusted {
				t.Fatalf("Trusted after = %v, %v; want true", trusted, err)
			}
			other, _ := testCA(t)
			if trusted, _ := store.Trusted(t.Context(), other); trusted {
				t.Fatal("a different CA counts as trusted")
			}
		})
	}
}

func TestWindowsStoreChecksTheCurrentUserRootStoreByThumbprint(t *testing.T) {
	ca, _ := testCA(t)
	m := newMachine(t)
	present := false
	m.run.respond = func(c Cmd) (string, error) {
		if strings.Contains(cmdline(c), `Test-Path Cert:\CurrentUser\Root\`+wantThumbprint(ca)) {
			if present {
				return "True\r\n", nil
			}
			return "False\r\n", nil
		}
		return "", nil
	}
	store := storeNamed(t, m.wsl(), "Windows")

	if trusted, err := store.Trusted(t.Context(), ca); err != nil || trusted {
		t.Fatalf("Trusted = %v, %v; want false", trusted, err)
	}
	present = true
	if trusted, err := store.Trusted(t.Context(), ca); err != nil || !trusted {
		t.Fatalf("Trusted = %v, %v; want true", trusted, err)
	}
}

func TestWindowsStoreImportsTheCAWithCertutilFromAWindowsPath(t *testing.T) {
	ca, caFile := testCA(t)
	m := newMachine(t)
	winTemp := t.TempDir() // where C:\Users\me\AppData\Local\Temp\ is mounted
	var imported []byte
	m.run.respond = func(c Cmd) (string, error) {
		line := cmdline(c)
		switch {
		case strings.Contains(line, "GetTempPath"):
			return `C:\Users\me\AppData\Local\Temp\` + "\r\n", nil
		case line == `wslpath -u C:\Users\me\AppData\Local\Temp\`:
			return winTemp + "\n", nil
		case strings.HasPrefix(line, "certutil.exe"):
			imported, _ = os.ReadFile(filepath.Join(winTemp, "laneway-local-ca.crt"))
		case strings.Contains(line, "Test-Path"):
			return "True\r\n", nil
		}
		return "", nil
	}
	store := storeNamed(t, m.wsl(), "Windows")
	if !strings.Contains(store.Prompt(), "Yes") {
		t.Errorf("prompt %q should tell the user to confirm Windows' dialog", store.Prompt())
	}

	if err := store.Trust(t.Context(), caFile, ca); err != nil {
		t.Fatal(err)
	}

	var certutil string
	for _, line := range m.run.lines() {
		if strings.HasPrefix(line, "certutil.exe") {
			certutil = line
		}
	}
	if want := `certutil.exe -user -addstore Root C:\Users\me\AppData\Local\Temp\laneway-local-ca.crt`; certutil != want {
		t.Fatalf("ran %q, want %q", certutil, want)
	}
	if want, _ := os.ReadFile(caFile); string(imported) != string(want) {
		t.Fatalf("certutil saw %q, want the CA's PEM", imported)
	}
	if _, err := os.Stat(filepath.Join(winTemp, "laneway-local-ca.crt")); !os.IsNotExist(err) {
		t.Errorf("the copy in the Windows temp dir was left behind (%v)", err)
	}
}

func TestWindowsStoreReportsADeclinedDialog(t *testing.T) {
	ca, caFile := testCA(t)
	m := newMachine(t)
	winTemp := t.TempDir()
	m.run.respond = func(c Cmd) (string, error) {
		line := cmdline(c)
		switch {
		case strings.Contains(line, "GetTempPath"):
			return `C:\Temp\` + "\r\n", nil
		case strings.HasPrefix(line, "wslpath"):
			return winTemp + "\n", nil
		case strings.Contains(line, "Test-Path"):
			return "False\r\n", nil
		}
		return "", nil
	}

	err := storeNamed(t, m.wsl(), "Windows").Trust(t.Context(), caFile, ca)

	if err == nil || !strings.Contains(err.Error(), "declined") {
		t.Fatalf("got %v, want an error saying the dialog may have been declined", err)
	}
}

// wantThumbprint is how Windows names a certificate: the SHA-1 of its DER
// bytes, in upper-case hex.
func wantThumbprint(ca *x509.Certificate) string {
	sum := sha1.Sum(ca.Raw)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}
