package hash

import (
	"bytes"
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "trims and lowercases", input: "  User@Example.COM  ", want: "user@example.com"},
		{name: "empty", input: "   ", want: ""},
		{name: "already normalized", input: "user@example.com", want: "user@example.com"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Normalize(test.input); got != test.want {
				t.Fatalf("Normalize(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}

func TestHashIsDeterministicAndNormalized(t *testing.T) {
	hasher, err := New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	first := hasher.Hash(" User@Example.com ")
	second := hasher.Hash("user@example.com")

	if len(first) != 32 {
		t.Fatalf("hash length = %d, want 32", len(first))
	}
	if !bytes.Equal(first, second) {
		t.Fatal("hash did not normalize email before hashing")
	}
}

func TestHashDependsOnSecret(t *testing.T) {
	first, err := New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	second, err := New([]byte("fedcba9876543210fedcba9876543210"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if bytes.Equal(first.Hash("user@example.com"), second.Hash("user@example.com")) {
		t.Fatal("hashes matched across different secrets")
	}
}

func TestNewRejectsShortSecret(t *testing.T) {
	if _, err := New([]byte("too-short")); err == nil {
		t.Fatal("New() accepted a secret shorter than 32 bytes")
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		email   string
		wantErr bool
	}{
		{name: "valid", email: "user@example.com"},
		{name: "valid with surrounding whitespace", email: " User@Example.com "},
		{name: "empty", email: "", wantErr: true},
		{name: "blank", email: "   ", wantErr: true},
		{name: "missing at", email: "userexample.com", wantErr: true},
		{name: "multiple at", email: "a@b@example.com", wantErr: true},
		{name: "missing local part", email: "@example.com", wantErr: true},
		{name: "missing domain", email: "user@", wantErr: true},
		{name: "inner whitespace", email: "us er@example.com", wantErr: true},
		{name: "too long", email: strings.Repeat("a", 315) + "@example.com", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := Validate(test.email)
			if test.wantErr && err == nil {
				t.Fatalf("Validate(%q) = nil, want error", test.email)
			}
			if !test.wantErr && err != nil {
				t.Fatalf("Validate(%q) error = %v, want nil", test.email, err)
			}
		})
	}
}
