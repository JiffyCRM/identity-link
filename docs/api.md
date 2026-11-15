# API Reference

## Transport

- Protocol: gRPC over HTTP/2
- Proto package: `identity.v1`
- Go package: `github.com/JiffyCRM/identity-link/api/identity/v1`
- Proto file: `api/proto/identity/v1/identity.proto`
- Default port: `50051` (`GRPC_PORT`)
- Server reflection: enabled
- Health: standard `grpc.health.v1.Health`, empty service name

Plaintext transport is used by default. TLS/mTLS is not implemented in v1; see [Security](security.md).

## Common conventions

All requests are unary. All timestamps are `google.protobuf.Timestamp` values in UTC.

### Normalization and validation

| Field | Normalization | Validation |
| --- | --- | --- |
| `provider` | Trim whitespace, lowercase | 1-64 bytes, matching `^[a-z0-9][a-z0-9._-]*$` |
| `provider_subject` | Trim whitespace, preserve case | 1-255 bytes |
| `email` | Trim whitespace, lowercase | At most 320 bytes normalized, exactly one `@`, non-empty local part and domain, no whitespace |
| `user_id` | Trim whitespace | Must parse as a UUID |

Invalid inputs return `InvalidArgument`. The service never echoes the offending value in the status message.

### Response data guarantee

`User` responses contain only:

- `id` — internal opaque user ID (UUID string)
- `created_at`
- `updated_at`

`ProviderAccount` responses contain only:

- `id`
- `provider`
- `provider_subject`
- `created_at`

There are no email or email-hash fields in any response message. The proto does not define them.

### gRPC status codes

| Code | Meaning in this service |
| --- | --- |
| `OK` | Success |
| `InvalidArgument` | Failed input validation or malformed UUID |
| `NotFound` | User, provider link, or email lookup did not match |
| `FailedPrecondition` | `LinkProvider` email does not match the user's stored email hash |
| `AlreadyExists` | Provider identity is linked to a different user |
| `Aborted` | Concurrent conflict persisted after retries; retry the call |
| `Internal` | Unexpected failure; details are logged, not returned |

## Messages

### User

| Field | Type | Description |
| --- | --- | --- |
| `id` | `string` | Internal user UUID |
| `created_at` | `google.protobuf.Timestamp` | User creation time |
| `updated_at` | `google.protobuf.Timestamp` | Last user update time |

### ProviderAccount

| Field | Type | Description |
| --- | --- | --- |
| `id` | `string` | Provider account row UUID |
| `provider` | `string` | Normalized provider name, for example `google` |
| `provider_subject` | `string` | Provider's subject identifier |
| `created_at` | `google.protobuf.Timestamp` | Link creation time |

## RPCs

### GetOrCreate

Creates or resolves the user for an external provider identity. This is the entry point for an OAuth callback.

Request: `GetOrCreateRequest`

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `provider` | `string` | yes | Normalized as described above |
| `provider_subject` | `string` | yes | Provider's stable subject ID |
| `email` | `string` | yes | Hashed internally; never stored or returned |

Response: `User`

Behavior:

1. If `(provider, provider_subject)` exists, return its user. The supplied email is ignored in this case.
2. Otherwise, if the email hash exists, attach the provider to that user and return it.
3. Otherwise, create user + email hash + provider link in one transaction.
4. Unique-constraint conflicts are retried up to three times.

Errors: `InvalidArgument`, `Aborted`, `Internal`.

Example:

```bash
grpcurl -plaintext -d '{
  "provider": "google",
  "provider_subject": "google-123",
  "email": "User@Example.com"
}' localhost:50051 identity.v1.IdentityService/GetOrCreate
```

```json
{
  "id": "<user-id>",
  "createdAt": "2026-01-01T00:00:00Z",
  "updatedAt": "2026-01-01T00:00:00Z"
}
```

### GetByProvider

Resolves a user from an external provider identity.

Request: `GetByProviderRequest`

| Field | Type | Required |
| --- | --- | --- |
| `provider` | `string` | yes |
| `provider_subject` | `string` | yes |

Response: `User`

Errors: `InvalidArgument`, `NotFound`, `Internal`.

```bash
grpcurl -plaintext -d '{
  "provider": "google",
  "provider_subject": "google-123"
}' localhost:50051 identity.v1.IdentityService/GetByProvider
```

### GetByUserID

Resolves a user by internal ID.

Request: `GetByUserIDRequest`

| Field | Type | Required |
| --- | --- | --- |
| `user_id` | `string` | yes |

Response: `User`

Errors: `InvalidArgument`, `NotFound`, `Internal`.

```bash
grpcurl -plaintext -d '{"user_id": "<user-id>"}' \
  localhost:50051 identity.v1.IdentityService/GetByUserID
```

### FindByEmail

Checks whether an email is known and resolves its user. Intended for invitation claiming and membership flows.

Request: `FindByEmailRequest`

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `email` | `string` | yes | Hashed internally; never returned |

Response: `User`

Errors: `InvalidArgument`, `NotFound`, `Internal`.

`NotFound` means the normalized email hash is not present.

```bash
grpcurl -plaintext -d '{"email": "user@example.com"}' \
  localhost:50051 identity.v1.IdentityService/FindByEmail
```

### LinkProvider

Explicitly attaches a provider identity to a known user.

Request: `LinkProviderRequest`

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `user_id` | `string` | yes | Target internal user |
| `provider` | `string` | yes | Normalized as described above |
| `provider_subject` | `string` | yes | Provider subject |
| `email` | `string` | yes | Must hash to the user's stored email hash |

Response: `User`

Behavior:

- Verifies the supplied email hash against the user's stored hash before inserting.
- Repeating the same link for the same user is idempotent and returns success.
- If the provider identity belongs to another user, returns `AlreadyExists`.

Errors: `InvalidArgument`, `NotFound`, `FailedPrecondition`, `AlreadyExists`, `Aborted`, `Internal`.

```bash
grpcurl -plaintext -d '{
  "user_id": "<user-id>",
  "provider": "github",
  "provider_subject": "github-456",
  "email": "user@example.com"
}' localhost:50051 identity.v1.IdentityService/LinkProvider
```

### UnlinkProvider

Removes a provider identity from a user.

Request: `UnlinkProviderRequest`

| Field | Type | Required |
| --- | --- | --- |
| `user_id` | `string` | yes |
| `provider` | `string` | yes |
| `provider_subject` | `string` | yes |

Response: `google.protobuf.Empty`

Behavior:

- Deletes the exact `(user_id, provider, provider_subject)` row.
- Removing the user's last provider is allowed.
- The user is not deleted.
- Missing link → `NotFound`.

Errors: `InvalidArgument`, `NotFound`, `Internal`.

```bash
grpcurl -plaintext -d '{
  "user_id": "<user-id>",
  "provider": "github",
  "provider_subject": "github-456"
}' localhost:50051 identity.v1.IdentityService/UnlinkProvider
```

### ListProviders

Lists provider identities attached to a user.

Request: `ListProvidersRequest`

| Field | Type | Required |
| --- | --- | --- |
| `user_id` | `string` | yes |

Response: `ListProvidersResponse`

| Field | Type | Description |
| --- | --- | --- |
| `providers` | repeated `ProviderAccount` | Ordered by `created_at`, then `id` |

Errors: `InvalidArgument`, `NotFound` (user does not exist), `Internal`.

```bash
grpcurl -plaintext -d '{"user_id": "<user-id>"}' \
  localhost:50051 identity.v1.IdentityService/ListProviders
```

## Health and reflection

```bash
grpcurl -plaintext localhost:50051 grpc.health.v1.Health/Check
```

```json
{
  "status": "SERVING"
}
```

`NOT_SERVING` means the database ping failed or the process is shutting down.

Reflection is enabled so `grpcurl` can discover services without local proto files:

```bash
grpcurl -plaintext localhost:50051 list
```

## Error examples

```text
ERROR:
  Code: NotFound
  Message: not found
```

```text
ERROR:
  Code: FailedPrecondition
  Message: email does not match the user's stored email
```

```text
ERROR:
  Code: AlreadyExists
  Message: provider account is already linked to another user
```

## Compatibility policy

The wire contract is defined by `identity.proto`. Additive message fields and new RPCs are non-breaking. Removing or renumbering fields, changing field types, or changing RPC semantics is breaking. See [Versioning](versioning.md) for the semantic version policy.
