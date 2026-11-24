// Package auth implements login, sessions and password hashing (D-1, D-32, D-33).
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters (RFC 9106 second recommended option: 64 MiB, 1 pass, 4 lanes).
const (
	argonMemoryKiB = 64 * 1024
	argonTime      = 1
	argonThreads   = 4
	argonKeyLen    = 32
	argonSaltLen   = 16
)

// Password policy (D-33).
const (
	MinPasswordRunes = 12
	MaxPasswordBytes = 1024
)

var ErrWeakPassword = fmt.Errorf("password must be at least %d characters and at most %d bytes", MinPasswordRunes, MaxPasswordBytes)

// hashSlots caps concurrent argon2 runs so a burst of logins cannot exhaust memory.
var hashSlots = make(chan struct{}, 4)

func withHashSlot[T any](f func() T) T {
	hashSlots <- struct{}{}
	defer func() { <-hashSlots }()
	return f()
}

// CheckPasswordPolicy reports whether pw is acceptable for a new password.
func CheckPasswordPolicy(pw string) error {
	if len(pw) > MaxPasswordBytes || utf8.RuneCountInString(pw) < MinPasswordRunes || !utf8.ValidString(pw) {
		return ErrWeakPassword
	}
	return nil
}

// HashPassword returns a PHC-format argon2id hash: $argon2id$v=19$m=..,t=..,p=..$salt$key
func HashPassword(pw string) (string, error) {
	if len(pw) > MaxPasswordBytes {
		return "", ErrWeakPassword
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("read salt: %w", err)
	}
	key := withHashSlot(func() []byte {
		return argon2.IDKey([]byte(pw), salt, argonTime, argonMemoryKiB, argonThreads, argonKeyLen)
	})
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemoryKiB, argonTime, argonThreads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// VerifyPassword reports whether pw matches the PHC-format hash. Parameters are read from
// the hash, so hashes made with older parameters keep working.
func VerifyPassword(pw, encoded string) (bool, error) {
	if len(pw) > MaxPasswordBytes {
		return false, nil
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, errors.New("unsupported password hash format")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errors.New("unsupported argon2 version")
	}
	var mem, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &t, &p); err != nil || mem == 0 || t == 0 || p == 0 {
		return false, errors.New("invalid argon2 parameters")
	}
	if mem > 1024*1024 || t > 16 { // refuse hashes that would cost more than 1 GiB or 16 passes
		return false, errors.New("argon2 parameters out of range")
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return false, errors.New("invalid salt")
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil || len(want) < 16 {
		return false, errors.New("invalid key")
	}
	got := withHashSlot(func() []byte {
		return argon2.IDKey([]byte(pw), salt, t, mem, p, uint32(len(want)))
	})
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// dummyHash is verified against when the email is unknown, so a failed login takes the
// same time whether or not the account exists.
var dummyHash = sync.OnceValue(func() string {
	h, err := HashPassword("not-a-real-password-for-timing-only")
	if err != nil {
		panic(err)
	}
	return h
})
