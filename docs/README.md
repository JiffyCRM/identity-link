# Identity Link Service Documentation

This directory is the complete documentation for the `identity-link` microservice. It covers the architecture, API contract, data model, security model, operational procedures, development workflow, and release versioning.

## Documentation map

| Document | Audience | Contents |
| --- | --- | --- |
| [Architecture](architecture.md) | Engineers | System context, package responsibilities, request flows, concurrency model, design decisions |
| [API](api.md) | Service consumers | Every RPC, field validation, response shape, status codes, grpcurl examples |
| [Data model](data-model.md) | Engineers, DBAs | Tables, constraints, indexes, email hashing, transactions, migrations, data lifecycle |
| [Security](security.md) | Engineers, security reviewers | Threat model, guarantees, secret handling, logging hygiene, checklist, limitations |
| [Operations](operations.md) | Operators, SREs | Configuration, database provisioning, deployment, health, logs, scaling, troubleshooting |
| [Development](development.md) | Contributors | Local setup, tests, proto generation, migrations, code conventions, extending the service |
| [Versioning](versioning.md) | Maintainers, release engineers | Single source of truth for the version, bump procedure, semantic version policy, build injection |

The top-level [README](../README.md) contains the short version: purpose, quick start, and links back here.

## Sources of truth

When documentation and code disagree, the code wins. The authoritative technical artifacts are:

| Concern | Authoritative file |
| --- | --- |
| RPC contract | `api/proto/identity/v1/identity.proto` |
| Database schema | `migrations/001_init.up.sql` |
| Service behavior | `internal/service/identity.go` |
| Persistence and race handling | `internal/store/postgres.go` |
| Configuration | `internal/config/config.go` |
| Version | `VERSION` |
| Email hashing | `internal/hash/email.go` |

## Service at a glance

- Language: Go 1.26.11
- API: gRPC, package `identity.v1`
- Storage: PostgreSQL 16+, raw SQL through `pgx/v5`
- Identity model: external `(provider, provider_subject)` pairs map to an internal opaque UUID
- Email model: one email per user, stored only as a raw `HMAC-SHA256` hash
- Version source: root [`VERSION`](../VERSION) file (see [Versioning](versioning.md))
