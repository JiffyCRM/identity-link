# Identity Link Service

Minimal Go + gRPC + Postgres service that owns the mapping between external identity providers and an internal opaque `user_id`.

Emails are accepted only as plaintext inputs to `GetOrCreate`, `FindByEmail`, and `LinkProvider`. They are normalized and stored as `HMAC-SHA256` hashes. No RPC returns an email or an email hash. Other services must call this service to create users or check whether an email exists.

## Version

The release version is tracked in one file: [`VERSION`](VERSION). Builds inject it into the binary through linker flags; the server exposes it with `--version` and logs it at startup.

```bash
make version
make build
./bin/identity-link --version
```

See [docs/versioning.md](docs/versioning.md) for the bump procedure and semantic version policy.

## Documentation

Full documentation is inside this service:

| Document | Contents |
| --- | --- |
| [Documentation index](docs/README.md) | Map of all documents and sources of truth |
| [Architecture](docs/architecture.md) | Components, request flows, concurrency model, design decisions |
| [API reference](docs/api.md) | Every RPC, validation rules, status codes, grpcurl examples |
| [Data model](docs/data-model.md) | Tables, constraints, hashing, transactions, migrations |
| [Security](docs/security.md) | Threat model, guarantees, secret handling, checklist |
| [Operations](docs/operations.md) | Configuration, database provisioning, deployment, health, troubleshooting |
| [Development](docs/development.md) | Local setup, tests, proto generation, migrations, extension workflow |
| [Versioning](docs/versioning.md) | `VERSION`, bump steps, compatibility policy, image tags |

## Key guarantees

- Provider identities map to opaque internal UUIDs; provider data is never exposed through the user ID.
- Emails are stored only as raw `HMAC-SHA256` digests in a dedicated database.
- The proto has no email or email-hash response fields.
- Concurrent `GetOrCreate` calls converge on exactly one user for one provider identity or one email.
- `LinkProvider` refuses to attach a provider when the supplied email does not match the user's stored hash.
- Multi-row writes are transactional; uniqueness is enforced by PostgreSQL constraints.

## Layout

```text
api/proto/identity/v1/identity.proto   gRPC contract
api/identity/v1/                       generated Go stubs
cmd/server/                            gRPC server
cmd/migrate/                           migration runner (up/down)
internal/config/                       environment configuration
internal/hash/                         email normalization + HMAC
internal/store/                        pgx raw-SQL store and interface
internal/service/                      gRPC handlers, validation, error mapping
internal/server/                       gRPC wiring, health, interceptors
internal/migrate/                      embedded SQL migration runner
internal/version/                      version variable injected at build time
migrations/                            SQL migrations
docs/                                  complete service documentation
VERSION                                single source of the service version
```

## Requirements

- Go 1.26.11+
- Postgres 16+ (or Docker)
- `protoc` (only needed to regenerate the API)
- Docker (for the local Postgres container and testcontainers integration tests)

## Configuration

| Variable | Required | Default | Notes |
| --- | --- | --- | --- |
| `DATABASE_URL` | yes | - | Dedicated database for this service |
| `EMAIL_HMAC_SECRET` | yes | - | At least 32 bytes; never logged |
| `GRPC_PORT` | no | `50051` | gRPC listen port |
| `LOG_LEVEL` | no | `info` | `debug`, `info`, `warn`, `error` |

Each microservice uses its own database. This service expects a dedicated `identity_link` database and role; it does not share tables or a schema with other services.

## Run locally

Start Postgres (container name `identity-link-postgres`, host port `5433`):

```bash
docker compose up -d
```

Dev secrets are managed in Infisical. The repository is linked to the `jiffycrm` project through `.infisical.yaml`.

```bash
infisical login
infisical init     # first time only; select jiffycrm
```

The Infisical `dev` environment must contain `DATABASE_URL` and `EMAIL_HMAC_SECRET`. Then apply migrations and start the server:

```bash
infisical secrets-manager run --env=dev -- make migrate
infisical secrets-manager run --env=dev -- make run
```

Without Infisical, set the variables manually:

```bash
export DATABASE_URL='postgres://identity_link:identity_link@localhost:5433/identity_link?sslmode=disable'
export EMAIL_HMAC_SECRET='replace-this-with-at-least-32-random-bytes'
make migrate
make run
```

Generate a suitable secret with `openssl rand -base64 48`.

Roll back the latest migration with:

```bash
infisical secrets-manager run --env=dev -- go run ./cmd/migrate down
```

## API summary

| RPC | Purpose |
| --- | --- |
| `GetOrCreate` | Resolve or create a user from provider identity + email |
| `GetByProvider` | Resolve a user by `(provider, provider_subject)` |
| `GetByUserID` | Resolve a user by internal UUID |
| `FindByEmail` | Resolve a user by email hash; `NotFound` if unknown |
| `LinkProvider` | Attach a provider to a user after verifying the email |
| `UnlinkProvider` | Remove a provider link |
| `ListProviders` | List a user's provider links |

All responses contain only IDs and timestamps. Full request/response details are in [docs/api.md](docs/api.md).

## Behavior notes

- Providers are trimmed and lowercased; `provider_subject` is trimmed but case-preserved.
- When a `(provider, provider_subject)` pair already exists, it wins over the supplied email: `GetOrCreate` returns the linked user without changing its stored email.
- `LinkProvider` only succeeds when the supplied email hashes to the user's stored email hash.
- `UnlinkProvider` may remove a user's last provider. The user is not deleted and can still be resolved by `FindByEmail`; an unknown provider link returns `NotFound`.

## gRPC examples

Reflection and the standard gRPC health service are enabled.

```bash
grpcurl -plaintext localhost:50051 list
grpcurl -plaintext localhost:50051 grpc.health.v1.Health/Check
```

Create or resolve a user:

```bash
grpcurl -plaintext -d '{
  "provider": "google",
  "provider_subject": "google-123",
  "email": "User@Example.com"
}' localhost:50051 identity.v1.IdentityService/GetOrCreate
```

A different provider with the same email links to the same internal user:

```bash
grpcurl -plaintext -d '{
  "provider": "github",
  "provider_subject": "github-456",
  "email": "user@example.com"
}' localhost:50051 identity.v1.IdentityService/GetOrCreate
```

Lookups:

```bash
grpcurl -plaintext -d '{
  "provider": "google",
  "provider_subject": "google-123"
}' localhost:50051 identity.v1.IdentityService/GetByProvider

grpcurl -plaintext -d '{"user_id": "<user-id>"}' \
  localhost:50051 identity.v1.IdentityService/GetByUserID

grpcurl -plaintext -d '{"email": "user@example.com"}' \
  localhost:50051 identity.v1.IdentityService/FindByEmail
```

`FindByEmail` returns `NotFound` when the email is unknown.

Explicit linking requires the supplied email to match the user's stored hash:

```bash
grpcurl -plaintext -d '{
  "user_id": "<user-id>",
  "provider": "github",
  "provider_subject": "github-456",
  "email": "user@example.com"
}' localhost:50051 identity.v1.IdentityService/LinkProvider

grpcurl -plaintext -d '{"user_id": "<user-id>"}' \
  localhost:50051 identity.v1.IdentityService/ListProviders

grpcurl -plaintext -d '{
  "user_id": "<user-id>",
  "provider": "github",
  "provider_subject": "github-456"
}' localhost:50051 identity.v1.IdentityService/UnlinkProvider
```

## Tests

```bash
make test
```

Unit tests cover email normalization, HMAC behavior, configuration validation, input validation, and gRPC error mapping. Store integration and concurrency tests use `testcontainers-go` with `postgres:16-alpine` and are skipped automatically when Docker is unavailable.

To run the integration tests against an existing Postgres, set `TEST_DATABASE_URL` to a disposable database. The tests truncate `user_emails`, `provider_accounts`, and `users` between subtests. See [docs/development.md](docs/development.md) for the full test matrix.

## Regenerate the API

```bash
make generate
```

The script installs pinned `protoc-gen-go` and `protoc-gen-go-grpc` versions and writes generated code to `api/identity/v1/`.

## Docker

```bash
make docker-build
```

The image contains the server and migration binaries. Run migrations with:

```bash
docker run --rm --entrypoint /usr/local/bin/identity-link-migrate \
  -e DATABASE_URL='postgres://identity_link:identity_link@host.docker.internal:5433/identity_link?sslmode=disable' \
  identity-link:<version> up
```

Start the server with:

```bash
docker run --rm -p 50051:50051 \
  -e DATABASE_URL='postgres://identity_link:identity_link@host.docker.internal:5433/identity_link?sslmode=disable' \
  -e EMAIL_HMAC_SECRET='replace-this-with-at-least-32-random-bytes' \
  identity-link:<version>
```

Migrations are not run automatically by the server. Run the migration command before starting the service.

`DATABASE_URL` must be reachable from inside the container. On Linux, add `--add-host=host.docker.internal:host-gateway` if using the compose Postgres from a container. Deployment details are in [docs/operations.md](docs/operations.md).

## Security

`EMAIL_HMAC_SECRET` is mandatory and must be at least 32 bytes; startup fails otherwise. Emails are normalized before hashing and are never stored in plaintext. The service does not implement TLS, caller authentication, or HMAC key rotation in v1. See [docs/security.md](docs/security.md) before deploying outside a trusted network.

## Out of scope for v1

- OAuth/OIDC flows (the caller completes these)
- Sessions, JWTs, cookies
- Multi-tenancy, invitations, multiple emails per user
- Soft deletes
- Metrics and tracing
- UI changes
