package pgwire

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// scramMechanism is the SASL mechanism Postgres uses for password_encryption
// = scram-sha-256 (the default since Postgres 14). Channel binding isn't
// used: Portby talks to its Service over loopback without TLS.
const scramMechanism = "SCRAM-SHA-256"

// scram is one SCRAM-SHA-256 exchange (RFC 5802, RFC 7677).
type scram struct {
	password    string
	nonce       string
	clientBare  string
	authMessage string
	salted      []byte
}

func newSCRAM(password string) *scram {
	return &scram{password: password, nonce: rand.Text()}
}

// clientFirst is the client-first-message. Postgres takes the user from the
// StartupMessage, so the SCRAM user name is empty.
func (s *scram) clientFirst() string {
	s.clientBare = "n=,r=" + s.nonce
	return "n,," + s.clientBare
}

// clientFinal answers the server-first-message with the proof.
func (s *scram) clientFinal(serverFirst string) (string, error) {
	attrs := scramAttrs(serverFirst)
	nonce, salt64, iter := attrs["r"], attrs["s"], attrs["i"]
	if !strings.HasPrefix(nonce, s.nonce) || nonce == s.nonce {
		return "", errors.New("pgwire: SCRAM: the server's nonce doesn't extend the client's")
	}
	salt, err := base64.StdEncoding.DecodeString(salt64)
	if err != nil {
		return "", fmt.Errorf("pgwire: SCRAM: bad salt: %w", err)
	}
	iterations, err := strconv.Atoi(iter)
	if err != nil || iterations < 1 {
		return "", fmt.Errorf("pgwire: SCRAM: bad iteration count %q", iter)
	}
	s.salted, err = pbkdf2.Key(sha256.New, s.password, salt, iterations, sha256.Size)
	if err != nil {
		return "", err
	}
	withoutProof := "c=biws,r=" + nonce // biws = base64("n,,")
	s.authMessage = s.clientBare + "," + serverFirst + "," + withoutProof
	clientKey := hmacSHA256(s.salted, "Client Key")
	storedKey := sha256.Sum256(clientKey)
	signature := hmacSHA256(storedKey[:], s.authMessage)
	proof := make([]byte, len(clientKey))
	for i := range clientKey {
		proof[i] = clientKey[i] ^ signature[i]
	}
	return withoutProof + ",p=" + base64.StdEncoding.EncodeToString(proof), nil
}

// verify checks the server-final-message's signature, which proves the
// server knows the password too.
func (s *scram) verify(serverFinal string) error {
	attrs := scramAttrs(serverFinal)
	if e, ok := attrs["e"]; ok {
		return fmt.Errorf("pgwire: SCRAM: %s", e)
	}
	want := hmacSHA256(hmacSHA256(s.salted, "Server Key"), s.authMessage)
	got, err := base64.StdEncoding.DecodeString(attrs["v"])
	if err != nil || !hmac.Equal(got, want) {
		return errors.New("pgwire: SCRAM: the server's signature is wrong")
	}
	return nil
}

func scramAttrs(msg string) map[string]string {
	attrs := map[string]string{}
	for _, part := range strings.Split(msg, ",") {
		if k, v, ok := strings.Cut(part, "="); ok {
			attrs[k] = v
		}
	}
	return attrs
}

func hmacSHA256(key []byte, msg string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(msg))
	return h.Sum(nil)
}
