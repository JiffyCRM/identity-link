.PHONY: generate migrate test run build docker-build version

VERSION ?= $(shell cat VERSION)
LDFLAGS := -X github.com/JiffyCRM/identity-link/internal/version.Version=$(VERSION)
DATABASE_URL ?= postgres://identity_link:identity_link@localhost:5433/identity_link?sslmode=disable

generate:
	./scripts/generate.sh

migrate:
	DATABASE_URL="$(DATABASE_URL)" go run ./cmd/migrate up

test:
	go test ./...

run:
	go run -ldflags "$(LDFLAGS)" ./cmd/server

build:
	go build -ldflags "$(LDFLAGS)" -o bin/identity-link ./cmd/server

docker-build:
	docker build --build-arg VERSION="$(VERSION)" -t identity-link:$(VERSION) .

version:
	@echo "$(VERSION)"
