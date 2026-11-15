# Versioning

## Single source of truth

The microservice version lives in one file:

```text
identity-link/VERSION
```

The current value is whatever that file contains. Do not hardcode the release number in Go source, the Dockerfile, or documentation. Read it with:

```bash
make version
```

or:

```bash
cat VERSION
```

The version follows semantic versioning: `MAJOR.MINOR.PATCH`, with optional pre-release suffixes such as `1.0.0-rc.1`.

## How the version reaches the binary

`internal/version/version.go` declares:

```go
var Version = "dev"
```

Build and run targets inject the `VERSION` file value with Go linker flags:

```bash
go build \
  -ldflags "-X github.com/JiffyCRM/identity-link/internal/version.Version=$(cat VERSION)" \
  -o bin/identity-link ./cmd/server
```

Implementation:

| Path | Mechanism |
| --- | --- |
| `make build` | Reads `VERSION`, passes `-ldflags` |
| `make run` | Reads `VERSION`, passes `-ldflags` |
| `make docker-build` | Reads `VERSION`, passes `--build-arg VERSION` |
| `Dockerfile` | Receives `ARG VERSION` and injects it with `-ldflags` |
| Plain `go build ./cmd/server` | Version remains `dev` |

Verify an injected build:

```bash
make build
./bin/identity-link --version
```

The server also logs the version at startup:

```json
{"level":"INFO","msg":"identity-link gRPC server started","port":50051,"version":"x.y.z"}
```

`--version` exits before configuration or database checks, so it works in minimal environments.

## How to bump the version

1. Decide the change class using the policy below.
2. Edit `VERSION`:

   ```bash
   printf '0.2.0\n' > VERSION
   ```

3. Update documentation only if behavior, API, schema, or operations changed. Do not copy the new number into other files.
4. Run validation:

   ```bash
   make test
   make build
   ./bin/identity-link --version
   ```

5. Build the image:

   ```bash
   make docker-build
   ```

   This produces `identity-link:0.2.0`.

6. Tag the source release, for example `v0.2.0`, according to team policy.

`make build VERSION=0.2.0` and `make docker-build VERSION=0.2.0` can override the file for a one-off build, but the file remains the release source of truth. Do not leave overrides unrecorded.

## Semantic version policy

### Before 1.0.0

The service is at `0.x`. While major version is `0`:

- `PATCH` — backward-compatible fixes, documentation, internal refactors, performance work.
- `MINOR` — backward-compatible additions, and, by explicit decision, breaking changes.
- Breaking changes must be called out in the release notes even though only `MINOR` is bumped.

### From 1.0.0 onward

- `MAJOR` — breaking changes:
  - Removing or renaming an RPC
  - Removing a proto field, renumbering a field, or changing a field type
  - Changing validation in a way that rejects previously valid input
  - Changing RPC semantics, including response data or error-code meaning
  - Destructive database migrations
- `MINOR` — backward-compatible changes:
  - Adding an RPC
  - Adding a proto field
  - Adding an index or an additive migration
  - New configuration with a safe default
- `PATCH` — fixes and internal changes with no external contract change.

## Compatibility expectations

| Artifact | Compatibility rule |
| --- | --- |
| gRPC proto | Additive fields and new RPCs are compatible; removals, renumbering, and type changes are breaking |
| Response shape | Never add email or email-hash data; adding it would be a security and contract break |
| Database schema | Additive migrations are compatible; dropping/renaming columns or tightening constraints is breaking |
| Configuration | New required environment variables are breaking; new optional variables with defaults are compatible |
| Behavior | Changing identity resolution order, email normalization, or HMAC inputs is breaking because it can produce different users or failed lookups |

## Container tags

| Build | Image tag |
| --- | --- |
| `make docker-build` with `VERSION` = `0.2.0` | `identity-link:0.2.0` |
| Pre-release `VERSION` = `0.3.0-rc.1` | `identity-link:0.3.0-rc.1` |

Do not publish a floating `latest` tag from a versioned release pipeline unless the deployment policy explicitly calls for it.

## CI recommendation

CI should:

1. Read `VERSION`.
2. Run `go vet ./...` and `make test`.
3. Build with the Makefile or the equivalent `-ldflags`.
4. Verify `./bin/identity-link --version` matches `VERSION`.
5. Build and publish the image tagged with the same value.

The version check prevents a build that silently reverted to `dev`.

## Related files

- `VERSION` — change this to change the release version
- `internal/version/version.go` — linker-injected variable
- `Makefile` — reads `VERSION` and injects it
- `Dockerfile` — accepts `VERSION` as a build argument
- `cmd/server/main.go` — `--version` output and startup log
