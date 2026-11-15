package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const maxGetOrCreateAttempts = 3

type Postgres struct {
	pool *pgxpool.Pool
}

func NewPostgres(pool *pgxpool.Pool) *Postgres {
	return &Postgres{pool: pool}
}

func (s *Postgres) GetOrCreate(ctx context.Context, provider, subject string, emailHash []byte) (User, error) {
	for attempt := 0; attempt < maxGetOrCreateAttempts; attempt++ {
		user, err := s.GetByProvider(ctx, provider, subject)
		if err == nil {
			return user, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return User{}, err
		}

		user, err = s.FindByEmailHash(ctx, emailHash)
		if err == nil {
			_, linkErr := s.pool.Exec(ctx, `
				INSERT INTO provider_accounts (id, user_id, provider, provider_subject)
				VALUES ($1, $2, $3, $4)
			`, newID(), user.ID, provider, subject)
			if linkErr == nil {
				return user, nil
			}
			if isUniqueViolation(linkErr) {
				continue
			}
			return User{}, linkErr
		}
		if !errors.Is(err, ErrNotFound) {
			return User{}, err
		}

		user, err = s.createUser(ctx, provider, subject, emailHash)
		if err == nil {
			return user, nil
		}
		if isUniqueViolation(err) {
			continue
		}
		return User{}, err
	}

	return User{}, ErrConflict
}

func (s *Postgres) GetByProvider(ctx context.Context, provider, subject string) (User, error) {
	return s.queryUser(ctx, `
		SELECT u.id, u.created_at, u.updated_at
		FROM provider_accounts pa
		JOIN users u ON u.id = pa.user_id
		WHERE pa.provider = $1 AND pa.provider_subject = $2
	`, provider, subject)
}

func (s *Postgres) GetByUserID(ctx context.Context, id uuid.UUID) (User, error) {
	return s.queryUser(ctx, `
		SELECT id, created_at, updated_at
		FROM users
		WHERE id = $1
	`, id)
}

func (s *Postgres) FindByEmailHash(ctx context.Context, emailHash []byte) (User, error) {
	return s.queryUser(ctx, `
		SELECT u.id, u.created_at, u.updated_at
		FROM user_emails ue
		JOIN users u ON u.id = ue.user_id
		WHERE ue.email_hash = $1
	`, emailHash)
}

func (s *Postgres) LinkProvider(ctx context.Context, userID uuid.UUID, provider, subject string, emailHash []byte) (User, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return User{}, fmt.Errorf("begin link transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	user, err := scanUser(tx.QueryRow(ctx, `
		SELECT id, created_at, updated_at
		FROM users
		WHERE id = $1
	`, userID))
	if err != nil {
		return User{}, err
	}

	var storedHash []byte
	err = tx.QueryRow(ctx, "SELECT email_hash FROM user_emails WHERE user_id = $1", userID).Scan(&storedHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrEmailMismatch
	}
	if err != nil {
		return User{}, fmt.Errorf("load user email hash: %w", err)
	}
	if !bytes.Equal(storedHash, emailHash) {
		return User{}, ErrEmailMismatch
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO provider_accounts (id, user_id, provider, provider_subject)
		VALUES ($1, $2, $3, $4)
	`, newID(), userID, provider, subject)
	if err != nil {
		if isUniqueViolation(err) {
			_ = tx.Rollback(ctx)
			ownerID, ownerErr := s.providerOwner(ctx, provider, subject)
			if ownerErr != nil {
				return User{}, ownerErr
			}
			if ownerID == userID {
				return user, nil
			}
			return User{}, ErrAlreadyLinked
		}
		return User{}, fmt.Errorf("insert provider account: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return User{}, fmt.Errorf("commit link transaction: %w", err)
	}

	return user, nil
}

func (s *Postgres) UnlinkProvider(ctx context.Context, userID uuid.UUID, provider, subject string) error {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM provider_accounts
		WHERE user_id = $1 AND provider = $2 AND provider_subject = $3
	`, userID, provider, subject)
	if err != nil {
		return fmt.Errorf("delete provider account: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrProviderNotLinked
	}
	return nil
}

func (s *Postgres) ListProviders(ctx context.Context, userID uuid.UUID) ([]ProviderAccount, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)", userID).Scan(&exists); err != nil {
		return nil, fmt.Errorf("check user exists: %w", err)
	}
	if !exists {
		return nil, ErrNotFound
	}

	rows, err := s.pool.Query(ctx, `
		SELECT id, provider, provider_subject, created_at
		FROM provider_accounts
		WHERE user_id = $1
		ORDER BY created_at, id
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("query provider accounts: %w", err)
	}
	defer rows.Close()

	providers := make([]ProviderAccount, 0)
	for rows.Next() {
		var provider ProviderAccount
		if err := rows.Scan(&provider.ID, &provider.Provider, &provider.ProviderSubject, &provider.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan provider account: %w", err)
		}
		providers = append(providers, provider)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate provider accounts: %w", err)
	}

	return providers, nil
}

func (s *Postgres) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

func (s *Postgres) createUser(ctx context.Context, provider, subject string, emailHash []byte) (User, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return User{}, fmt.Errorf("begin create transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	user, err := scanUser(tx.QueryRow(ctx, `
		INSERT INTO users (id)
		VALUES ($1)
		RETURNING id, created_at, updated_at
	`, newID()))
	if err != nil {
		return User{}, err
	}

	if _, err := tx.Exec(ctx, "INSERT INTO user_emails (user_id, email_hash) VALUES ($1, $2)", user.ID, emailHash); err != nil {
		return User{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO provider_accounts (id, user_id, provider, provider_subject)
		VALUES ($1, $2, $3, $4)
	`, newID(), user.ID, provider, subject); err != nil {
		return User{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return User{}, fmt.Errorf("commit create transaction: %w", err)
	}

	return user, nil
}

func (s *Postgres) queryUser(ctx context.Context, query string, args ...any) (User, error) {
	return scanUser(s.pool.QueryRow(ctx, query, args...))
}

func (s *Postgres) providerOwner(ctx context.Context, provider, subject string) (uuid.UUID, error) {
	var ownerID uuid.UUID
	err := s.pool.QueryRow(ctx, `
		SELECT user_id
		FROM provider_accounts
		WHERE provider = $1 AND provider_subject = $2
	`, provider, subject).Scan(&ownerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrConflict
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("load provider owner: %w", err)
	}
	return ownerID, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanUser(row rowScanner) (User, error) {
	var user User
	if err := row.Scan(&user.ID, &user.CreatedAt, &user.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, ErrNotFound
		}
		return User{}, err
	}
	return user, nil
}

func newID() uuid.UUID {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.New()
	}
	return id
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
