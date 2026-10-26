package mysqlwire

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
)

// The authentication plugins this client speaks.
const (
	cachingSHA2 = "caching_sha2_password" // the default since MySQL 8.0
	nativeSHA1  = "mysql_native_password" // the default before 8.0
)

// authResponse is the first answer to plugin's challenge scramble.
func authResponse(plugin, password string, scramble []byte) ([]byte, error) {
	if password == "" {
		return nil, nil
	}
	switch plugin {
	case cachingSHA2:
		return ScrambleSHA256(password, scramble), nil
	case nativeSHA1:
		// SHA1(password) XOR SHA1(scramble + SHA1(SHA1(password)))
		h1 := sha1.Sum([]byte(password))
		h2 := sha1.Sum(h1[:])
		h := sha1.New()
		h.Write(scramble)
		h.Write(h2[:])
		return xor(h1[:], h.Sum(nil)), nil
	default:
		return nil, fmt.Errorf("mysqlwire: unsupported authentication plugin %q", plugin)
	}
}

// ScrambleSHA256 is caching_sha2_password's fast-path answer to scramble:
// SHA256(password) XOR SHA256(SHA256(SHA256(password)) + scramble). The
// fake server checks it the same way.
func ScrambleSHA256(password string, scramble []byte) []byte {
	h1 := sha256.Sum256([]byte(password))
	h2 := sha256.Sum256(h1[:])
	h := sha256.New()
	h.Write(h2[:])
	h.Write(scramble)
	return xor(h1[:], h.Sum(nil))
}

// encryptPassword is caching_sha2_password's full authentication over a
// connection without TLS: the password (NUL-terminated) XOR the scramble,
// encrypted with the server's RSA public key (PEM) using OAEP.
func encryptPassword(pemKey []byte, password string, scramble []byte) ([]byte, error) {
	block, _ := pem.Decode(pemKey)
	if block == nil {
		return nil, errors.New("mysqlwire: the server sent no public key")
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("mysqlwire: the server's public key: %w", err)
	}
	pub, ok := key.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("mysqlwire: the server's public key isn't RSA")
	}
	plain := []byte(password + "\x00")
	for i := range plain {
		plain[i] ^= scramble[i%len(scramble)]
	}
	return rsa.EncryptOAEP(sha1.New(), rand.Reader, pub, plain, nil)
}

func xor(a, b []byte) []byte {
	out := make([]byte, len(a))
	for i := range a {
		out[i] = a[i] ^ b[i]
	}
	return out
}
