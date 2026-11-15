package migrate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/JiffyCRM/identity-link/migrations"
)

const advisoryLockKey int64 = 0x6964656e74697479

var (
	upFilePattern   = regexp.MustCompile(`^(\d+)_.*\.up\.sql$`)
	downFilePattern = regexp.MustCompile(`^(\d+)_.*\.down\.sql$`)
)

type fileSet struct {
	up       map[int64]string
	down     map[int64]string
	versions []int64
}

func loadFiles() (*fileSet, error) {
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}

	files := &fileSet{
		up:   make(map[int64]string),
		down: make(map[int64]string),
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if match := upFilePattern.FindStringSubmatch(name); match != nil {
			version, err := strconv.ParseInt(match[1], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("parse migration version %q: %w", name, err)
			}
			files.up[version] = name
			continue
		}
		if match := downFilePattern.FindStringSubmatch(name); match != nil {
			version, err := strconv.ParseInt(match[1], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("parse migration version %q: %w", name, err)
			}
			files.down[version] = name
		}
	}

	if len(files.up) == 0 {
		return nil, errors.New("no up migrations found")
	}

	for version := range files.up {
		files.versions = append(files.versions, version)
	}
	sort.Slice(files.versions, func(i, j int) bool {
		return files.versions[i] < files.versions[j]
	})

	return files, nil
}

func RunUp(ctx context.Context, pool *pgxpool.Pool) ([]int64, error) {
	files, err := loadFiles()
	if err != nil {
		return nil, err
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire database connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", advisoryLockKey); err != nil {
		return nil, fmt.Errorf("acquire migration lock: %w", err)
	}
	defer unlock(conn, ctx)

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    BIGINT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`); err != nil {
		return nil, fmt.Errorf("create schema_migrations table: %w", err)
	}

	appliedVersions, err := loadAppliedVersions(ctx, conn)
	if err != nil {
		return nil, err
	}

	var applied []int64
	for _, version := range files.versions {
		if appliedVersions[version] {
			continue
		}

		body, err := migrations.FS.ReadFile(files.up[version])
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", files.up[version], err)
		}

		tx, err := conn.Begin(ctx)
		if err != nil {
			return nil, fmt.Errorf("begin migration %d: %w", version, err)
		}

		if _, err := tx.Exec(ctx, string(body)); err != nil {
			_ = tx.Rollback(ctx)
			return nil, fmt.Errorf("apply migration %d: %w", version, err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", version); err != nil {
			_ = tx.Rollback(ctx)
			return nil, fmt.Errorf("record migration %d: %w", version, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit migration %d: %w", version, err)
		}

		applied = append(applied, version)
	}

	return applied, nil
}

func RunDown(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	files, err := loadFiles()
	if err != nil {
		return 0, err
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return 0, fmt.Errorf("acquire database connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", advisoryLockKey); err != nil {
		return 0, fmt.Errorf("acquire migration lock: %w", err)
	}
	defer unlock(conn, ctx)

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    BIGINT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`); err != nil {
		return 0, fmt.Errorf("create schema_migrations table: %w", err)
	}

	var latest int64
	err = conn.QueryRow(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&latest)
	if err != nil {
		return 0, fmt.Errorf("find latest migration: %w", err)
	}
	if latest == 0 {
		return 0, nil
	}

	downFile, ok := files.down[latest]
	if !ok {
		return 0, fmt.Errorf("missing down migration for version %d", latest)
	}

	body, err := migrations.FS.ReadFile(downFile)
	if err != nil {
		return 0, fmt.Errorf("read migration %s: %w", downFile, err)
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin rollback %d: %w", latest, err)
	}
	if _, err := tx.Exec(ctx, string(body)); err != nil {
		_ = tx.Rollback(ctx)
		return 0, fmt.Errorf("roll back migration %d: %w", latest, err)
	}
	if _, err := tx.Exec(ctx, "DELETE FROM schema_migrations WHERE version = $1", latest); err != nil {
		_ = tx.Rollback(ctx)
		return 0, fmt.Errorf("remove migration record %d: %w", latest, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit rollback %d: %w", latest, err)
	}

	return latest, nil
}

func unlock(conn *pgxpool.Conn, ctx context.Context) {
	unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, _ = conn.Exec(unlockCtx, "SELECT pg_advisory_unlock($1)", advisoryLockKey)
}

func loadAppliedVersions(ctx context.Context, conn *pgxpool.Conn) (map[int64]bool, error) {
	rows, err := conn.Query(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return nil, fmt.Errorf("load applied migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[int64]bool)
	for rows.Next() {
		var version int64
		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("scan applied migration: %w", err)
		}
		applied[version] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate applied migrations: %w", err)
	}
	return applied, nil
}
