package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"

	"github.com/google/uuid"
)

// CookieName is the session cookie. Its value is "<session id>.<HMAC-SHA256 of the id>".
const CookieName = "pt_session"

// MinSecretBytes is the shortest accepted SESSION_SECRET.
const MinSecretBytes = 32

var ErrBadCookie = errors.New("invalid session cookie")

// Signer signs and checks session cookie values with SESSION_SECRET.
type Signer struct{ key []byte }

func NewSigner(secret string) (*Signer, error) {
	if len(secret) < MinSecretBytes {
		return nil, errors.New("SESSION_SECRET must be at least 32 bytes (generate one with: openssl rand -base64 48)")
	}
	return &Signer{key: []byte(secret)}, nil
}

func (s *Signer) Sign(id uuid.UUID) string {
	return id.String() + "." + base64.RawURLEncoding.EncodeToString(s.mac(id.String()))
}

// Parse returns the session id of a correctly signed cookie value.
func (s *Signer) Parse(value string) (uuid.UUID, error) {
	idPart, sigPart, ok := strings.Cut(value, ".")
	if !ok {
		return uuid.Nil, ErrBadCookie
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigPart)
	if err != nil || !hmac.Equal(sig, s.mac(idPart)) {
		return uuid.Nil, ErrBadCookie
	}
	id, err := uuid.Parse(idPart)
	if err != nil || id == uuid.Nil {
		return uuid.Nil, ErrBadCookie
	}
	return id, nil
}

func (s *Signer) mac(msg string) []byte {
	h := hmac.New(sha256.New, s.key)
	h.Write([]byte("pt_session:" + msg))
	return h.Sum(nil)
}
