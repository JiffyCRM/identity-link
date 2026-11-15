# Operations

## Artifacts

| Artifact | Built from | Purpose |
| --- | --- | --- |
| `bin/identity-link` | `cmd/server` | gRPC server |
| `go run ./cmd/migrate` | `cmd/migrate` | Schema migration command |
| Docker image | `Dockerfile` | Alpine runtime with the server and migration binaries, running as a non-root user |

The server does not run migrations. Migrations are always an explicit deployment step.

## Configuration

Configuration comes only from environment variables.

| Variable | Required | Default | Validation |
| --- | --- | --- | --- |
| `DATABASE_URL` | yes | none | non-empty; parsed by pgx |
| `EMAIL_HMAC_SECRET` | yes | none | at least 32 bytes |
| `GRPC_PORT` | no | `50051` | integer 1-65535 |
| `LOG_LEVEL` | no | `info` | `debug`, `info`, `warn`, `error` |

Startup fails fast when configuration is missing or invalid. The secret is never printed.

## Local run

Start the provided PostgreSQL container:

```bash
docker compose up -d
```

Set the environment:

```bash
export DATABASE_URL='postgres://identity_link:identity_link@localhost:5433/identity_link?sslmode=disable'
export EMAIL_HMAC_SECRET='replace-this-with-at-least-32-random-bytes'
```

Apply migrations and start:

```bash
make migrate
make run
```

`make run` injects the version from the `VERSION` file. Verify it:

```bash
make build
./bin/identity-link --version
```

## Database provisioning

Each microservice has its own database. Minimal local setup is handled by `docker-compose.yml`. Production should separate DDL and runtime roles:

```sql
CREATE ROLE identity_link_owner LOGIN PASSWORD '<owner-password>';
CREATE DATABASE identity_link OWNER identity_link_owner;

CREATE ROLE identity_link_app LOGIN PASSWORD '<app-password>';
GRANT CONNECT ON DATABASE identity_link TO identity_link_app;
GRANT USAGE ON SCHEMA public TO identity_link_app;
ALTER DEFAULT PRIVILEGES FOR ROLE identity_link_owner IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO identity_link_app;
```

Run the grants after migrations, or rely on the default privileges to cover tables created by future migrations. If tables already exist, also run:

```sql
GRANT SELECT, INSERT, UPDATE, DELETE
    ON users, provider_accounts, user_emails
    TO identity_link_app;
```

Use the owner role for `cmd/migrate` and the app role for `cmd/server`. Keep the owner credentials out of the runtime environment.

## Deployment order

1. Provision the database and roles if they do not exist.
2. Deploy the new binary or image.
3. Run `cmd/migrate up` with the owner connection string.
4. Start `cmd/server` with the runtime connection string and other environment variables.
5. Verify the health service returns `SERVING`.
6. Verify application RPCs with `grpcurl` or an internal smoke test.

For a rolling deploy:

1. Run migrations before starting new instances when the migration is backward compatible.
2. Roll out new instances.
3. Remove old instances.

Because the server never migrates automatically, a failed migration cannot partially boot a new version. The migration runner itself is transactional per version and serialized with an advisory lock.

## Docker

Build:

```bash
make docker-build
```

The image is tagged `identity-link:<version>`. Run:

```bash
docker run --rm -p 50051:50051 \
  -e DATABASE_URL='postgres://identity_link_app:<password>@<host>:5432/identity_link?sslmode=require' \
  -e EMAIL_HMAC_SECRET='<secret-at-least-32-bytes>' \
  identity-link:<version>
```

Notes:

- The container runs as UID/GID `65532`.
- `DATABASE_URL` must be reachable from the container.
- On Linux, `host.docker.internal` requires `--add-host=host.docker.internal:host-gateway`.
- The image includes both `/usr/local/bin/identity-link` and `/usr/local/bin/identity-link-migrate`.
- Run migrations separately with `--entrypoint /usr/local/bin/identity-link-migrate ... up`; the server never migrates at startup.
- The image defaults to the server entrypoint.

## Health checks

The standard health service is registered under the empty name:

```bash
grpcurl -plaintext localhost:50051 grpc.health.v1.Health/Check
```

```json
{
  "status": "SERVING"
}
```

Behavior:

- `NOT_SERVING` before the first successful database ping.
- Ping every 5 seconds with a 2-second timeout.
- `NOT_SERVING` on ping failure.
- `NOT_SERVING` during shutdown.

Kubernetes can use a gRPC health probe or an exec probe with `grpcurl`. Use readiness to gate traffic on `SERVING`. Liveness can simply check that the process is running, since the server already fails fast at startup when the database is unreachable.

## Logs

Format: JSON via `log/slog` to stdout.

Startup:

```json
{"level":"INFO","msg":"identity-link gRPC server started","port":50051,"version":"<version>"}
```

Each RPC:

```json
{"level":"INFO","msg":"gRPC request","method":"/identity.v1.IdentityService/GetOrCreate","code":"OK","duration_ms":6}
```

Interesting fields for monitoring:

- `code` distribution, especially `Internal`, `Aborted`, and `FailedPrecondition`.
- `duration_ms` latency.
- Repeated `database health check failed` warnings.
- Startup failures and panic logs.

No payloads, emails, hashes, or secrets are logged.

## Resource usage and scaling

The service is stateless. Scale horizontally behind a load balancer or service mesh.

- Each instance maintains a `pgxpool` with the library's default pool sizing based on available CPUs.
- Total connections across instances must stay within the PostgreSQL connection limit.
- For large instance counts, set a pool size per instance or add PgBouncer.
- Writes contend on two unique indexes; normal CRM identity volume is not expected to create meaningful contention.
- After contention retries are exhausted, callers receive `Aborted` and should retry.

## Backup and restore

Back up the `identity_link` database, including:

- `users`
- `provider_accounts`
- `user_emails`
- `schema_migrations`

Backups are sensitive because they contain email hashes. Encrypt backups, restrict restore access, and test restores.

Critical: the `EMAIL_HMAC_SECRET` must survive with the data. A restore without the original secret cannot resolve emails. Store the secret in the same disaster-recovery scope as the database, but not inside the database backup.

## Secret rotation

There is no transparent rotation. Changing the secret invalidates all existing email hashes. See [Security](security.md). Do not rotate casually. A future multi-key or re-hashing flow is required for safe rotation.

## Failure modes

| Failure | Behavior |
| --- | --- |
| Missing or invalid env var | Process exits before listening |
| HMAC secret shorter than 32 bytes | Process exits before listening |
| Database unreachable at startup | Process exits after a 10-second ping timeout |
| Database drops after startup | RPCs fail; health becomes `NOT_SERVING` |
| Migration failure | Current migration transaction rolls back; process is unaffected because it never migrated |
| Migration lock held by another runner | Runner waits |
| gRPC port in use | Process exits with a listen error |
| Panic in a unary handler | Recovered; caller receives `Internal`; process continues |
| `SIGTERM` / `SIGINT` | Graceful stop up to 10 seconds, then forced stop |

## Troubleshooting

| Symptom | Checks |
| --- | --- |
| `DATABASE_URL is required` | Environment variable not set in the process |
| `EMAIL_HMAC_SECRET is required` | Missing secret or shorter than 32 bytes |
| `ping database` error | Wrong host/port, credentials, database name, TLS mode, network policy |
| `relation "users" does not exist` | Migrations were not applied |
| `permission denied for table ...` | Runtime role lacks grants; apply the provisioning SQL |
| `no pending migrations` on migrate | Expected when schema is current |
| `missing down migration for version N` | Add the matching `.down.sql` file |
| Health stays `NOT_SERVING` | Database ping failing; inspect logs for `database health check failed` |
| All email lookups fail after a secret change | Hash mismatch caused by changed HMAC key; restore the old secret |
| `Aborted` under load | Unique-constraint retry budget exhausted; retry the call |
| `FailedPrecondition` from `LinkProvider` | Supplied email does not match the target user's stored hash |
| `AlreadyExists` from `LinkProvider` | Provider identity is already attached to another user |

## Release checklist

1. Bump `VERSION` (see [Versioning](versioning.md)).
2. Update documentation if behavior or API changed.
3. Run `make test`.
4. Run migrations up/down/up on a disposable database.
5. Build `make build` and verify `./bin/identity-link --version`.
6. Build the image with `make docker-build`.
7. Run a staging smoke test with `grpcurl`: health, `GetOrCreate`, `FindByEmail`, `ListProviders`.
8. Confirm logs contain no payloads or secrets.
9. Deploy migrations, then the new image.
10. Tag the release in source control.
