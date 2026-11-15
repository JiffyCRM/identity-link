# Development

## Prerequisites

- Go 1.26.11+
- Docker with Compose for local PostgreSQL and testcontainers
- `protoc` only when regenerating the API
- `grpcurl` for manual smoke tests

## First-time setup

```bash
cd identity-link
docker compose up -d

export DATABASE_URL='postgres://identity_link:identity_link@localhost:5433/identity_link?sslmode=disable'
export EMAIL_HMAC_SECRET='0123456789abcdef0123456789abcdef'

make migrate
make run
```

In another terminal:

```bash
grpcurl -plaintext localhost:50051 grpc.health.v1.Health/Check
```

## Repository layout

```text
api/proto/identity/v1/identity.proto   Source of truth for the wire contract
api/identity/v1/                       Generated Go stubs (checked in)
cmd/server/                            Server entrypoint
cmd/migrate/                           Migration entrypoint
internal/config/                       Environment loading and validation
internal/hash/                         Email normalization, validation, HMAC
internal/store/                        Store interface, models, pgx implementation
internal/service/                      gRPC handlers and error mapping
internal/server/                       gRPC server construction, health loop, interceptors
internal/migrate/                      Embedded migration engine
internal/version/                      Version variable populated by ldflags
migrations/                            SQL schema and embedded migration files
scripts/generate.sh                    Proto generation
docs/                                  This documentation
```

Package rules:

- `internal/store` must not import gRPC or service packages.
- `internal/service` must not import pgx.
- Plaintext email must not appear in the store interface.
- SQL must use parameter binding. No string interpolation of request values.
- New exported capabilities should be added to the proto first, then generated, then implemented.

## Daily commands

| Command | Purpose |
| --- | --- |
| `make generate` | Regenerate protobuf stubs |
| `make migrate` | Apply migrations using `DATABASE_URL` |
| `make test` | Run all tests |
| `make run` | Run the server with the version injected |
| `make build` | Build `bin/identity-link` with the version injected |
| `make docker-build` | Build `identity-link:<version>` |
| `make version` | Print the current version from `VERSION` |

## Proto generation

Edit `api/proto/identity/v1/identity.proto`, then:

```bash
make generate
```

The script:

1. Verifies `protoc` is present.
2. Installs pinned `protoc-gen-go` and `protoc-gen-go-grpc` versions into `GOBIN`.
3. Generates `api/identity/v1/identity.pb.go` and `identity_grpc.pb.go`.

Generated files are committed. Do not edit them by hand.

## Tests

### Unit tests

```bash
go test ./internal/hash ./internal/service ./internal/config
```

Coverage:

- Email normalization and validation
- HMAC determinism and secret separation
- Config validation
- Handler validation, normalization, status mapping, and internal error redaction

### Integration tests

```bash
go test ./internal/store
```

The test starts `postgres:16-alpine` with testcontainers and runs migrations. It skips when Docker is unavailable.

To use an existing disposable database instead:

```bash
TEST_DATABASE_URL='postgres://user:pass@host:5432/disposable_db?sslmode=disable' \
  go test ./internal/store -v
```

Warning: the tests truncate `user_emails`, `provider_accounts`, and `users` between subtests. Never point `TEST_DATABASE_URL` at a database with real data.

Store tests cover:

- New user creation
- Provider idempotency
- Email-based linking to an existing user
- Provider identity winning over a different email
- Email lookup hits and misses
- Provider and user ID lookups
- Link idempotency and email mismatch
- Cross-user link conflict
- Unlink and last-provider unlink
- `ListProviders` behavior
- 20-goroutine contention on one identity
- 20-goroutine contention on one email with different providers

### Race detector

```bash
go test -race ./internal/hash ./internal/service ./internal/config
```

The store concurrency tests can also run under `-race` when Docker is available:

```bash
go test -race ./internal/store
```

### End-to-end smoke test

```bash
grpcurl -plaintext -d '{
  "provider": "google",
  "provider_subject": "manual-test",
  "email": "manual@example.com"
}' localhost:50051 identity.v1.IdentityService/GetOrCreate
```

Then verify `FindByEmail`, `ListProviders`, and `UnlinkProvider` with the returned ID. See [API](api.md).

## Adding a migration

1. Pick the next numeric version: `migrations/002_<name>.up.sql`.
2. Add `migrations/002_<name>.down.sql`.
3. Keep both files transactional. Avoid statements that cannot run inside a transaction unless you also change the runner.
4. Update `docs/data-model.md` and any affected behavior documentation.
5. Test:

```bash
DATABASE_URL=... go run ./cmd/migrate up
DATABASE_URL=... go run ./cmd/migrate down
DATABASE_URL=... go run ./cmd/migrate up
```

Migrations are embedded with `//go:embed`; rebuild the binary after adding files.

## Adding an RPC or field

1. Edit the proto.
2. Run `make generate`.
3. Add handler logic in `internal/service/identity.go`.
4. Add a store method in `internal/store/store.go` and implement it in `internal/store/postgres.go`.
5. Add validation and error mapping.
6. Add unit tests for validation/status codes and integration tests for persistence.
7. Update `docs/api.md`, this file if workflow changed, and the README summary if needed.
8. Decide whether the change is a version bump. See [Versioning](versioning.md).

Never add email or email-hash fields to response messages.

## Debugging

Useful commands:

```bash
grpcurl -plaintext localhost:50051 list
grpcurl -plaintext localhost:50051 describe identity.v1.IdentityService
grpcurl -plaintext localhost:50051 describe identity.v1.GetOrCreateRequest
```

Database inspection without exposing plaintext:

```sql
SELECT id, created_at, updated_at FROM users ORDER BY created_at DESC LIMIT 10;
SELECT user_id, provider, provider_subject, created_at FROM provider_accounts ORDER BY created_at DESC LIMIT 10;
SELECT user_id, encode(email_hash, 'hex') AS email_hash_hex, created_at FROM user_emails LIMIT 10;
```

Do not add temporary logging of request payloads. If a bug requires inspecting an email, reproduce it with synthetic test data in a disposable database.

Common issues:

| Issue | Resolution |
| --- | --- |
| Integration tests skip | Docker socket not accessible; start Docker or use `TEST_DATABASE_URL` |
| `protoc: command not found` | Install `protoc` before `make generate` |
| Generated code does not compile | Re-run `make generate`; ensure generated files match the proto |
| Migrations not found | Rebuild after adding SQL files; they are embedded at compile time |
| Version prints `dev` | `go run ./cmd/server` without ldflags; use `make run` or `make build` |

## Code conventions

- Format with `gofmt`; the repository expects no unformatted files.
- Run `go vet ./...` before committing.
- Wrap errors with context when crossing package boundaries.
- Return sentinel store errors for expected conditions.
- Keep gRPC status mapping centralized in `internal/service`.
- Use standard library logging via `slog`; JSON output in the server.
- Prefer explicit structs over maps.
- Do not add comments that restate code; document contracts and non-obvious behavior.

## Pre-change checklist

1. Does this change expose email data? It must not.
2. Does it add a multi-row write? Wrap it in a transaction.
3. Does it add a uniqueness rule? Add a database constraint and a concurrency test.
4. Does it change the proto? Regenerate and assess compatibility.
5. Does it change the schema? Add up and down migrations.
6. Does it change behavior? Update the relevant document in `docs/`.
7. Does it change the released contract? Bump `VERSION`.
