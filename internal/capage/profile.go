package capage

import (
	"crypto/sha256"
	"crypto/x509"
	"fmt"

	"howett.net/plist"
)

// profile is an unsigned iOS configuration profile that installs cert, a
// root CA certificate. Its identifier is fixed, so installing a profile for
// a new CA replaces the old one; its UUIDs come from the certificate, so
// each CA's profile is told apart.
func profile(cert *x509.Certificate) ([]byte, error) {
	type payload struct {
		PayloadCertificateFileName string `plist:",omitempty"`
		PayloadContent             any
		PayloadDescription         string
		PayloadDisplayName         string
		PayloadIdentifier          string
		PayloadType                string
		PayloadUUID                string
		PayloadVersion             int
	}
	sum := sha256.Sum256(cert.Raw)
	return plist.MarshalIndent(payload{
		PayloadContent: []payload{{
			PayloadCertificateFileName: certFile,
			PayloadContent:             cert.Raw,
			PayloadDescription:         "Adds Laneway's root certificate, so this device trusts the Domains Laneway serves.",
			PayloadDisplayName:         cert.Subject.CommonName,
			PayloadIdentifier:          "dev.laneway.ca.root",
			PayloadType:                "com.apple.security.root",
			PayloadUUID:                uuid(sum[16:]),
			PayloadVersion:             1,
		}},
		PayloadDescription: "Laneway's root certificate, from the PC Laneway runs on.",
		PayloadDisplayName: "Laneway",
		PayloadIdentifier:  "dev.laneway.ca",
		PayloadType:        "Configuration",
		PayloadUUID:        uuid(sum[:16]),
		PayloadVersion:     1,
	}, plist.XMLFormat, "\t")
}

// uuid formats b, 16 bytes, as a version 4 UUID.
func uuid(b []byte) string {
	u := [16]byte(b)
	u[6] = u[6]&0x0f | 0x40
	u[8] = u[8]&0x3f | 0x80
	return fmt.Sprintf("%X-%X-%X-%X-%X", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
}
