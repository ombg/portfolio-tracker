package auth

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestPasswordHashRoundTrip(t *testing.T) {
	const pw = "correct horse battery"
	h, err := HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=1,p=4$") {
		t.Fatalf("unexpected hash format: %s", h)
	}
	if ok, err := VerifyPassword(pw, h); err != nil || !ok {
		t.Fatalf("correct password rejected: %v %v", ok, err)
	}
	if ok, err := VerifyPassword(pw+"x", h); err != nil || ok {
		t.Fatalf("wrong password accepted: %v %v", ok, err)
	}
	h2, _ := HashPassword(pw)
	if h == h2 {
		t.Fatal("two hashes of the same password are equal: salt is not random")
	}
}

func TestVerifyPasswordRejectsMalformedHashes(t *testing.T) {
	for _, h := range []string{
		"",
		"plaintext",
		"$2a$10$bcryptstyle",
		"$argon2i$v=19$m=65536,t=1,p=4$c2FsdHNhbHRzYWx0$a2V5a2V5a2V5a2V5a2V5a2V5",
		"$argon2id$v=19$m=999999999,t=1,p=4$c2FsdHNhbHRzYWx0$a2V5a2V5a2V5a2V5a2V5a2V5", // memory too high
		"$argon2id$v=19$m=65536,t=1,p=4$!!$a2V5a2V5a2V5a2V5a2V5a2V5",
	} {
		if ok, err := VerifyPassword("whatever", h); err == nil || ok {
			t.Errorf("hash %q: ok=%v err=%v, want an error", h, ok, err)
		}
	}
}

func TestPasswordPolicy(t *testing.T) {
	for _, tc := range []struct {
		pw string
		ok bool
	}{
		{"short", false},
		{"elevenchars", false},
		{"twelve chars", true},
		{"ééééééééééé", false}, // 11 runes, 22 bytes: counted in characters
		{"éééééééééééé", true},
		{strings.Repeat("a", MaxPasswordBytes+1), false},
		{"bad utf8 \xff\xfe\xfd!!", false},
	} {
		if err := CheckPasswordPolicy(tc.pw); (err == nil) != tc.ok {
			t.Errorf("policy(%q) = %v, want ok=%v", tc.pw, err, tc.ok)
		}
	}
}

func TestCookieSignAndParse(t *testing.T) {
	s, err := NewSigner(strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	v := s.Sign(id)
	got, err := s.Parse(v)
	if err != nil || got != id {
		t.Fatalf("Parse(Sign(id)) = %v, %v", got, err)
	}

	other, _ := NewSigner(strings.Repeat("o", 32))
	tampered := uuid.New().String() + v[36:] // another id with the original signature
	for name, value := range map[string]string{
		"other secret":     other.Sign(id),
		"swapped id":       tampered,
		"no signature":     id.String(),
		"empty":            "",
		"garbage":          "a.b",
		"nil id signed ok": s.Sign(uuid.Nil),
	} {
		if _, err := s.Parse(value); err == nil {
			t.Errorf("%s: accepted %q", name, value)
		}
	}
}

func TestSignerRejectsShortSecret(t *testing.T) {
	if _, err := NewSigner("change-me"); err == nil {
		t.Fatal("short secret accepted")
	}
}
