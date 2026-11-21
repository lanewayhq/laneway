// Package capage is the page another device installs Laneway's root CA from,
// at /ca on the Dashboard Domain. The daemon serves it over plain HTTP as well
// as HTTPS (the one exception to its redirect), so a phone can open it before
// it trusts Laneway's certificates. It offers the root certificate and an
// iOS configuration profile holding it, with short instructions per
// platform. Only the public root certificate is ever read, never its key.
package capage

import (
	"bytes"
	"crypto/x509"
	"embed"
	"encoding/pem"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"strings"
	"time"
)

// Path is where the page is: /ca, with its files under /ca/.
const Path = "/ca"

// Has reports whether path is the page or one of its files.
func Has(path string) bool {
	return path == Path || strings.HasPrefix(path, Path+"/")
}

// The files the page links, under Path.
const (
	certFile    = "laneway-root.crt"
	profileFile = "laneway.mobileconfig"
	styleFile   = "style.css"
	qrFile      = "qr.svg"
)

//go:embed static
var static embed.FS

var page = template.Must(template.ParseFS(static, "static/ca.html"))

// Config is what the page needs from the daemon.
type Config struct {
	// RootCert is the file holding Laneway's root CA certificate (PEM).
	RootCert string
	// QR is the URL the QR code at /ca/qr.svg encodes: the page's own
	// plain-HTTP URL, read on every request. "" means no QR code, as
	// while the Root Domain is localhost, which no other device reaches.
	QR func() string
}

// Handler serves the page at Path and its files under it. Paths for which
// Has is false get a 404.
func Handler(cfg Config) http.Handler {
	style, err := static.ReadFile("static/" + styleFile)
	if err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+Path+"/{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, Path, http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET "+Path, func(w http.ResponseWriter, r *http.Request) {
		cert, err := readRootCert(cfg.RootCert)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		var buf bytes.Buffer
		if err := page.Execute(&buf, pageData{
			Name:    cert.Subject.CommonName,
			Cert:    Path + "/" + certFile,
			Profile: Path + "/" + profileFile,
			Style:   Path + "/" + styleFile,
		}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		serve(w, r, "text/html; charset=utf-8", buf.Bytes())
	})
	mux.HandleFunc("GET "+Path+"/"+certFile, func(w http.ResponseWriter, r *http.Request) {
		cert, err := readRootCert(cfg.RootCert)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		download(w, certFile)
		serve(w, r, "application/x-x509-ca-cert", cert.Raw)
	})
	mux.HandleFunc("GET "+Path+"/"+profileFile, func(w http.ResponseWriter, r *http.Request) {
		cert, err := readRootCert(cfg.RootCert)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		body, err := profile(cert)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		download(w, profileFile)
		serve(w, r, "application/x-apple-aspen-config", body)
	})
	mux.HandleFunc("GET "+Path+"/"+qrFile, func(w http.ResponseWriter, r *http.Request) {
		url := ""
		if cfg.QR != nil {
			url = cfg.QR()
		}
		if url == "" {
			http.Error(w, "no QR code: no other device reaches this Root Domain", http.StatusNotFound)
			return
		}
		body, err := qrSVG(url)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		serve(w, r, "image/svg+xml", body)
	})
	mux.HandleFunc("GET "+Path+"/"+styleFile, func(w http.ResponseWriter, r *http.Request) {
		serve(w, r, "text/css; charset=utf-8", style)
	})
	return headers(mux)
}

type pageData struct {
	Name                 string // the root certificate's name, as a device lists it
	Cert, Profile, Style string
}

// serve writes body as the whole response.
func serve(w http.ResponseWriter, r *http.Request, contentType string, body []byte) {
	w.Header().Set("Content-Type", contentType)
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(body))
}

// download makes the response a file the browser saves as name.
func download(w http.ResponseWriter, name string) {
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
}

// headers are the Dashboard's: no caching, only the page's own files, no
// framing, no sniffing.
func headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

// readRootCert reads Laneway's root CA certificate from file, a PEM file
// holding only that certificate.
func readRootCert(file string) (*x509.Certificate, error) {
	data, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("Laneway's CA doesn't exist yet")
	}
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("%s holds no PEM certificate", file)
	}
	return x509.ParseCertificate(block.Bytes)
}
