package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	DefaultAdminUsername = "admin"
	// DefaultBootstrapPassword is the documented first-login password. A
	// deployment that configures bootstrap_admin_password_env explicitly still
	// gets it; a deployment that does not now receives a generated credential in
	// a root-only file beside the metadata instead. It is never persisted as
	// plaintext, and the first login must replace it before any platform data can
	// be read or changed.
	DefaultBootstrapPassword = "admin123"

	maximumPasswordLength = 1024
	maximumArgon2Memory   = 256 * 1024
	maximumArgon2Time     = 10
	maximumArgon2Threads  = 16
)

var ErrPasswordPolicy = errors.New("new password does not meet platform policy")

type Argon2Params struct {
	Memory      uint32
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

type Argon2Hasher struct {
	Params Argon2Params
	Random io.Reader
}

func DefaultArgon2Hasher(random io.Reader) Argon2Hasher {
	if random == nil {
		random = rand.Reader
	}
	return Argon2Hasher{
		Params: Argon2Params{
			Memory:      64 * 1024,
			Iterations:  3,
			Parallelism: 2,
			SaltLength:  16,
			KeyLength:   32,
		},
		Random: random,
	}
}

func validArgon2Params(params Argon2Params) bool {
	return params.Memory >= 8 && params.Memory <= maximumArgon2Memory &&
		params.Iterations >= 1 && params.Iterations <= maximumArgon2Time &&
		params.Parallelism >= 1 && params.Parallelism <= maximumArgon2Threads &&
		params.SaltLength >= 8 && params.SaltLength <= 64 &&
		params.KeyLength >= 16 && params.KeyLength <= 64
}

func (hasher Argon2Hasher) Hash(password string) (string, error) {
	if password == "" || len(password) > maximumPasswordLength {
		return "", fmt.Errorf("password is invalid")
	}
	if !validArgon2Params(hasher.Params) {
		return "", fmt.Errorf("argon2 parameters are invalid")
	}
	random := hasher.Random
	if random == nil {
		random = rand.Reader
	}
	salt := make([]byte, hasher.Params.SaltLength)
	if _, err := io.ReadFull(random, salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	key := argon2.IDKey(
		[]byte(password), salt, hasher.Params.Iterations, hasher.Params.Memory,
		hasher.Params.Parallelism, hasher.Params.KeyLength,
	)
	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, hasher.Params.Memory, hasher.Params.Iterations, hasher.Params.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key),
	), nil
}

func parseArgon2Hash(encoded string) (Argon2Params, []byte, []byte, bool) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return Argon2Params{}, nil, nil, false
	}
	versionPart, found := strings.CutPrefix(parts[2], "v=")
	if !found {
		return Argon2Params{}, nil, nil, false
	}
	version, err := strconv.Atoi(versionPart)
	if err != nil || version != argon2.Version {
		return Argon2Params{}, nil, nil, false
	}
	parameters := strings.Split(parts[3], ",")
	if len(parameters) != 3 {
		return Argon2Params{}, nil, nil, false
	}
	values := make(map[string]uint64, 3)
	for _, parameter := range parameters {
		name, raw, found := strings.Cut(parameter, "=")
		if !found || values[name] != 0 {
			return Argon2Params{}, nil, nil, false
		}
		value, err := strconv.ParseUint(raw, 10, 32)
		if err != nil || value == 0 {
			return Argon2Params{}, nil, nil, false
		}
		values[name] = value
	}
	if len(values) != 3 || values["m"] == 0 || values["t"] == 0 || values["p"] == 0 || values["p"] > 255 {
		return Argon2Params{}, nil, nil, false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return Argon2Params{}, nil, nil, false
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return Argon2Params{}, nil, nil, false
	}
	params := Argon2Params{
		Memory:      uint32(values["m"]),
		Iterations:  uint32(values["t"]),
		Parallelism: uint8(values["p"]),
		SaltLength:  uint32(len(salt)),
		KeyLength:   uint32(len(key)),
	}
	if !validArgon2Params(params) {
		return Argon2Params{}, nil, nil, false
	}
	return params, salt, key, true
}

func (hasher Argon2Hasher) Verify(encoded, password string) bool {
	if password == "" || len(password) > maximumPasswordLength {
		return false
	}
	params, salt, expected, ok := parseArgon2Hash(encoded)
	if !ok {
		return false
	}
	actual := argon2.IDKey(
		[]byte(password), salt, params.Iterations, params.Memory, params.Parallelism, params.KeyLength,
	)
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

func ValidateNewPassword(password string) error {
	if len(password) < 12 || len(password) > maximumPasswordLength {
		return ErrPasswordPolicy
	}
	return nil
}

// ValidateBootstrapPassword accepts the documented first-login password and
// otherwise enforces the normal password policy for site-supplied overrides.
func ValidateBootstrapPassword(password string) error {
	if password == DefaultBootstrapPassword {
		return nil
	}
	return ValidateNewPassword(password)
}
