package auth

import (
	"bytes"
	"strings"
	"testing"
)

func testArgon2Hasher() Argon2Hasher {
	return Argon2Hasher{
		Params: Argon2Params{
			Memory:      64,
			Iterations:  1,
			Parallelism: 1,
			SaltLength:  16,
			KeyLength:   32,
		},
		Random: bytes.NewReader(bytes.Repeat([]byte{0x2a}, 256)),
	}
}

func TestArgon2HasherDoesNotExposePlaintextAndVerifiesPassword(t *testing.T) {
	hasher := testArgon2Hasher()
	password := "correct horse battery staple"
	encoded, err := hasher.Hash(password)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if strings.Contains(encoded, password) {
		t.Fatal("encoded password hash contains plaintext")
	}
	if !hasher.Verify(encoded, password) {
		t.Fatal("correct password was rejected")
	}
	if hasher.Verify(encoded, "wrong password") {
		t.Fatal("wrong password was accepted")
	}
}

func TestArgon2HasherRejectsMalformedAndOversizedHashes(t *testing.T) {
	hasher := testArgon2Hasher()
	for _, encoded := range []string{
		"",
		"$argon2id$v=18$m=64,t=1,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=1048577,t=1,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=64,t=100,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=64,t=1,p=1$%%%$aGFzaA",
	} {
		if hasher.Verify(encoded, "password") {
			t.Fatalf("malformed hash was accepted: %q", encoded)
		}
	}
}

func TestPasswordPolicyRejectsBootstrapAndWeakPasswords(t *testing.T) {
	for _, password := range []string{"", "admin123", "short-pass"} {
		if err := ValidateNewPassword(password); err == nil {
			t.Fatalf("password %q was accepted", password)
		}
	}
	if err := ValidateNewPassword("A-new-secure-password-123"); err != nil {
		t.Fatalf("secure password rejected: %v", err)
	}
}
