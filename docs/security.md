# Security

## Security goals

1. Email addresses are not stored or returned in plaintext.
2. A database dump alone cannot be used to confirm whether a given email exists without the HMAC secret.
3. External provider identities cannot be attached to a user with a different email through `LinkProvider`.
4. Concurrent requests cannot create duplicate users or duplicate provider mappings.
5. Invalid or maliciously shaped input is rejected before reaching SQL.
6. The service has access only to its own dedicated database.

## Scope and trust boundaries

Trusted:

- Backend callers that have already completed provider authentication.
- The service process and its configuration secret.
- The database instance and its backup infrastructure.

Untrusted:

- All request payloads.
- The network path, unless the operator adds TLS/mTLS.
- Anyone with read access to logs or the database, unless separately authorized.

The service does not authenticate callers. It assumes it runs inside a private backend network. This is the most important residual security limitation.

## Threat model

| Threat | Mitigation | Residual risk |
| --- | --- | --- |
| Database dump reveals emails | Only HMAC-SHA256 hashes are stored | Secret compromise plus DB dump enables offline guessing |
| Logs leak emails | Request payloads are never logged; logging interceptors record method, code, and duration only | Store errors may contain Postgres constraint details such as a hash or provider subject, never plaintext email |
| API response leaks email | Proto has no email fields; `User` contains only ID and timestamps | None by contract |
| Cross-account provider linking | `LinkProvider` verifies the supplied email hash against the stored hash | A malicious trusted caller could still link a provider it controls to a user whose email it knows; caller authentication is out of scope |
| Duplicate users under concurrency | Unique constraints plus bounded retry | Persistent contention returns `Aborted`; callers must retry |
| Email enumeration | `FindByEmail` intentionally reveals existence | Any caller with API access can enumerate known emails; callers must be trusted and authorized |
| SQL injection | All queries use pgx parameter binding; no dynamic SQL from input | None known |
| Oversized or malformed input | Length limits, provider pattern, email validation, UUID parsing | No rate limiting; large volumes of valid-looking requests are possible |
| Secret exposure in logs or errors | Secret is never logged; config `String()` redacts it; client errors are generic | Container inspect, process environment, and crash dumps can expose environment variables unless the platform protects them |
| Panic crashes the process | Unary recovery interceptor converts panics to `Internal` | Panics in background goroutines are not recovered |
| Reflection exposes schema | Useful for development | Disable or network-restrict reflection in production if required by policy |
| Replay or impersonation on the wire | None in v1 | Add TLS/mTLS and caller authentication before exposing beyond a trusted network |
| Timing side channels | Hash comparison uses `bytes.Equal` after an HMAC comparison against a stored digest | LinkProvider hash comparison is not constant-time; this is unlikely to be exploitable over gRPC but can be hardened |
| UUID time disclosure | UUIDv7 embeds a timestamp | Accepted; IDs are not secrets |

## Email protection

### Hashing

- Algorithm: `HMAC-SHA256`
- Key: `EMAIL_HMAC_SECRET`, minimum 32 bytes, required at startup
- Input: `strings.ToLower(strings.TrimSpace(email))`
- Stored form: raw 32-byte digest in `BYTEA`
- Uniqueness: database unique constraint on `email_hash`

### Why HMAC

A plain hash of a normalized email is trivially reversible by dictionary attack. HMAC requires the secret. The secret must be generated randomly, for example:

```bash
openssl rand -base64 48
```

### Secret handling requirements

- Store the secret in a secret manager or orchestrator secret, not in git.
- Do not bake it into container images.
- Restrict read access to the service process and deployment tooling.
- Do not log it. `Config.String()` redacts it.
- Keep the secret available for the lifetime of the email data.

### Rotation limitation

The service supports exactly one HMAC key. Rotating `EMAIL_HMAC_SECRET` changes every email hash. Because plaintext emails are not stored, existing hashes cannot be re-derived. Practical options:

1. Treat the secret as long-lived and protect it accordingly.
2. Introduce a future multi-key verification design and a data re-hashing flow driven by callers.
3. Accept loss of `FindByEmail` for existing users and rebuild mappings from callers, which is not implemented.

There is no safe in-place rotation in v1.

## Input validation

Validation lives in `internal/hash` and `internal/service`:

| Input | Rule |
| --- | --- |
| `provider` | Trimmed and lowercased; 1-64 bytes; `^[a-z0-9][a-z0-9._-]*$` |
| `provider_subject` | Trimmed; 1-255 bytes; case preserved |
| `email` | Trimmed and lowercased; at most 320 bytes; one `@`; non-empty local/domain; no whitespace |
| `user_id` | Parsed as UUID |

No request is allowed to reach SQL with an invalid identifier or empty required field. Validation errors return `InvalidArgument` with a generic explanation.

## Data access control

- The service uses its own PostgreSQL database and role.
- Runtime credentials should have DML rights only on `users`, `provider_accounts`, and `user_emails`.
- Migration credentials are separate and have DDL rights.
- No other service should read these tables directly. Cross-service access goes through gRPC.
- Backups contain email hashes and must be encrypted and access-controlled.

See [Data model](data-model.md) for provisioning SQL.

## Logging and observability hygiene

Logged:

- Startup with port and version
- Each RPC's method, status code, and duration
- Panics with method and panic value
- Database health check failures
- Unexpected store errors

Never logged:

- Request or response payloads
- Plaintext emails
- The HMAC secret
- Configuration values

Postgres errors can include constraint values such as an email hash or provider subject. These are not plaintext emails, but treat error logs as sensitive.

There are no audit logs, metrics, or traces in v1.

## Transport and caller authentication

Not implemented in v1:

- TLS
- mTLS
- Token or API-key authentication of callers
- Authorization per RPC
- Rate limiting

Deployment requirements until these exist:

- Bind the service to a private network or loopback.
- Restrict access with network policy or firewall rules.
- Do not expose `GRPC_PORT` to the public internet.
- Consider a service mesh or sidecar for mTLS.

## Security checklist

| Requirement | Implementation |
| --- | --- |
| `EMAIL_HMAC_SECRET` required and at least 32 bytes | `internal/config/config.go`, `internal/hash/email.go` |
| Email normalized (trim + lowercase) before hashing | `internal/hash/email.go` |
| No RPC returns email or email hash | `api/proto/identity/v1/identity.proto` |
| Dedicated database and least-privilege role guidance | `docker-compose.yml`, this document, [Data model](data-model.md) |
| Multi-row writes use transactions | `internal/store/postgres.go` (`createUser`, `LinkProvider`) |
| `GetOrCreate` idempotent under concurrency | Unique constraints, bounded retry, concurrency tests in `internal/store/postgres_test.go` |
| Input validation before SQL | `internal/service/identity.go`, `internal/hash/email.go` |
| Plaintext email discarded after hashing | Hash computed in handler; no store interface accepts plaintext |
| Secret never logged | Config redaction; no secret in log fields |
| Internal error details not returned to callers | `mapStoreError` returns generic `Internal` messages |

## Recommended deployment hardening

1. Add TLS/mTLS or run behind a service mesh.
2. Restrict network access to known callers.
3. Store the HMAC secret in a secret manager with audit access.
4. Encrypt backups and test restore procedures.
5. Disable reflection if not needed.
6. Add rate limiting at the network or proxy layer.
7. Monitor abnormal `NotFound` volumes from `FindByEmail` as a possible enumeration signal.
8. Add an audit log before using this service for high-risk account operations.
