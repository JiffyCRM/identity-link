FROM golang:1.26.11-alpine AS build

ARG VERSION=dev

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN mkdir -p /out \
    && CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w -X github.com/JiffyCRM/identity-link/internal/version.Version=${VERSION}" \
    -o /out/identity-link ./cmd/server \
    && CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/identity-link-migrate ./cmd/migrate

FROM alpine:3.22

RUN addgroup -S -g 65532 identity \
    && adduser -S -u 65532 -G identity identity

COPY --from=build /out/identity-link /usr/local/bin/identity-link
COPY --from=build /out/identity-link-migrate /usr/local/bin/identity-link-migrate

USER 65532:65532
EXPOSE 50051
ENTRYPOINT ["/usr/local/bin/identity-link"]
