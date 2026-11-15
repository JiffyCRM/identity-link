package hash

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
)

const (
	MinSecretLength = 32
	MaxEmailLength  = 320
)

type Hasher struct {
	key []byte
}

func New(secret []byte) (*Hasher, error) {
	if len(secret) < MinSecretLength {
		return nil, fmt.Errorf("email HMAC secret must be at least %d bytes", MinSecretLength)
	}
	key := make([]byte, len(secret))
	copy(key, secret)
	return &Hasher{key: key}, nil
}

func Normalize(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func Validate(email string) error {
	normalized := Normalize(email)
	if normalized == "" {
		return errors.New("email is required")
	}
	if len(normalized) > MaxEmailLength {
		return fmt.Errorf("email must be at most %d bytes", MaxEmailLength)
	}
	if strings.ContainsAny(normalized, " \t\r\n") {
		return errors.New("email must not contain whitespace")
	}
	if strings.Count(normalized, "@") != 1 {
		return errors.New("email must contain exactly one @")
	}
	local, domain, _ := strings.Cut(normalized, "@")
	if local == "" || domain == "" {
		return errors.New("email must have a non-empty local part and domain")
	}
	return nil
}

func (h *Hasher) Hash(email string) []byte {
	mac := hmac.New(sha256.New, h.key)
	mac.Write([]byte(Normalize(email)))
	return mac.Sum(nil)
}
