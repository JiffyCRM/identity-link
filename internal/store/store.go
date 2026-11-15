package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	ErrNotFound          = errors.New("not found")
	ErrEmailMismatch     = errors.New("email does not match stored email")
	ErrAlreadyLinked     = errors.New("provider account already linked to another user")
	ErrProviderNotLinked = errors.New("provider account not linked")
	ErrConflict          = errors.New("concurrent modification conflict")
)

type User struct {
	ID        uuid.UUID
	CreatedAt time.Time
	UpdatedAt time.Time
}

type ProviderAccount struct {
	ID              uuid.UUID
	Provider        string
	ProviderSubject string
	CreatedAt       time.Time
}

type Store interface {
	GetOrCreate(ctx context.Context, provider, subject string, emailHash []byte) (User, error)
	GetByProvider(ctx context.Context, provider, subject string) (User, error)
	GetByUserID(ctx context.Context, id uuid.UUID) (User, error)
	FindByEmailHash(ctx context.Context, emailHash []byte) (User, error)
	LinkProvider(ctx context.Context, userID uuid.UUID, provider, subject string, emailHash []byte) (User, error)
	UnlinkProvider(ctx context.Context, userID uuid.UUID, provider, subject string) error
	ListProviders(ctx context.Context, userID uuid.UUID) ([]ProviderAccount, error)
	Ping(ctx context.Context) error
}
