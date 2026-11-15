package service_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	identityv1 "github.com/JiffyCRM/identity-link/api/identity/v1"
	"github.com/JiffyCRM/identity-link/internal/hash"
	"github.com/JiffyCRM/identity-link/internal/service"
	"github.com/JiffyCRM/identity-link/internal/store"
)

const testSecret = "0123456789abcdef0123456789abcdef"

type fakeStore struct {
	getOrCreate   func(ctx context.Context, provider, subject string, emailHash []byte) (store.User, error)
	findByEmail   func(ctx context.Context, emailHash []byte) (store.User, error)
	linkProvider  func(ctx context.Context, userID uuid.UUID, provider, subject string, emailHash []byte) (store.User, error)
	unlink        func(ctx context.Context, userID uuid.UUID, provider, subject string) error
	listProviders func(ctx context.Context, userID uuid.UUID) ([]store.ProviderAccount, error)
}

func (f *fakeStore) GetOrCreate(ctx context.Context, provider, subject string, emailHash []byte) (store.User, error) {
	if f.getOrCreate != nil {
		return f.getOrCreate(ctx, provider, subject, emailHash)
	}
	return store.User{}, store.ErrNotFound
}

func (f *fakeStore) GetByProvider(context.Context, string, string) (store.User, error) {
	return store.User{}, store.ErrNotFound
}

func (f *fakeStore) GetByUserID(context.Context, uuid.UUID) (store.User, error) {
	return store.User{}, store.ErrNotFound
}

func (f *fakeStore) FindByEmailHash(ctx context.Context, emailHash []byte) (store.User, error) {
	if f.findByEmail != nil {
		return f.findByEmail(ctx, emailHash)
	}
	return store.User{}, store.ErrNotFound
}

func (f *fakeStore) LinkProvider(ctx context.Context, userID uuid.UUID, provider, subject string, emailHash []byte) (store.User, error) {
	if f.linkProvider != nil {
		return f.linkProvider(ctx, userID, provider, subject, emailHash)
	}
	return store.User{}, store.ErrNotFound
}

func (f *fakeStore) UnlinkProvider(ctx context.Context, userID uuid.UUID, provider, subject string) error {
	if f.unlink != nil {
		return f.unlink(ctx, userID, provider, subject)
	}
	return store.ErrProviderNotLinked
}

func (f *fakeStore) ListProviders(ctx context.Context, userID uuid.UUID) ([]store.ProviderAccount, error) {
	if f.listProviders != nil {
		return f.listProviders(ctx, userID)
	}
	return nil, store.ErrNotFound
}

func (f *fakeStore) Ping(context.Context) error {
	return nil
}

func newClient(t *testing.T, fake store.Store) identityv1.IdentityServiceClient {
	t.Helper()

	hasher, err := hash.New([]byte(testSecret))
	if err != nil {
		t.Fatalf("hash.New() error = %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	identityService := service.New(fake, hasher, logger)

	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	identityv1.RegisterIdentityServiceServer(grpcServer, identityService)

	go func() {
		_ = grpcServer.Serve(listener)
	}()
	t.Cleanup(grpcServer.Stop)

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient() error = %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
	})

	return identityv1.NewIdentityServiceClient(conn)
}

func requireCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if got := status.Code(err); got != want {
		t.Fatalf("status code = %s, want %s (error: %v)", got, want, err)
	}
}

func TestValidationErrors(t *testing.T) {
	client := newClient(t, &fakeStore{})
	ctx := context.Background()

	t.Run("empty provider", func(t *testing.T) {
		_, err := client.GetOrCreate(ctx, &identityv1.GetOrCreateRequest{
			Provider:        "",
			ProviderSubject: "subject",
			Email:           "user@example.com",
		})
		requireCode(t, err, codes.InvalidArgument)
	})

	t.Run("invalid provider characters", func(t *testing.T) {
		_, err := client.GetOrCreate(ctx, &identityv1.GetOrCreateRequest{
			Provider:        "google provider",
			ProviderSubject: "subject",
			Email:           "user@example.com",
		})
		requireCode(t, err, codes.InvalidArgument)
	})

	t.Run("empty subject", func(t *testing.T) {
		_, err := client.GetOrCreate(ctx, &identityv1.GetOrCreateRequest{
			Provider:        "google",
			ProviderSubject: "",
			Email:           "user@example.com",
		})
		requireCode(t, err, codes.InvalidArgument)
	})

	t.Run("invalid email", func(t *testing.T) {
		_, err := client.GetOrCreate(ctx, &identityv1.GetOrCreateRequest{
			Provider:        "google",
			ProviderSubject: "subject",
			Email:           "not-an-email",
		})
		requireCode(t, err, codes.InvalidArgument)
	})

	t.Run("invalid user id", func(t *testing.T) {
		_, err := client.GetByUserID(ctx, &identityv1.GetByUserIDRequest{UserId: "not-a-uuid"})
		requireCode(t, err, codes.InvalidArgument)
	})
}

func TestGetOrCreateNormalizesInput(t *testing.T) {
	var gotProvider, gotSubject string
	var gotHash []byte

	fake := &fakeStore{
		getOrCreate: func(_ context.Context, provider, subject string, emailHash []byte) (store.User, error) {
			gotProvider = provider
			gotSubject = subject
			gotHash = emailHash
			return store.User{ID: uuid.New()}, nil
		},
	}
	client := newClient(t, fake)

	_, err := client.GetOrCreate(context.Background(), &identityv1.GetOrCreateRequest{
		Provider:        "  Google ",
		ProviderSubject: " Subject ",
		Email:           " User@Example.com ",
	})
	if err != nil {
		t.Fatalf("GetOrCreate() error = %v", err)
	}

	if gotProvider != "google" {
		t.Fatalf("provider = %q, want %q", gotProvider, "google")
	}
	if gotSubject != "Subject" {
		t.Fatalf("subject = %q, want %q", gotSubject, "Subject")
	}

	hasher, err := hash.New([]byte(testSecret))
	if err != nil {
		t.Fatalf("hash.New() error = %v", err)
	}
	if !bytes.Equal(gotHash, hasher.Hash("user@example.com")) {
		t.Fatal("store received a hash for a differently normalized email")
	}
}

func TestFindByEmailNotFound(t *testing.T) {
	client := newClient(t, &fakeStore{})
	_, err := client.FindByEmail(context.Background(), &identityv1.FindByEmailRequest{Email: "missing@example.com"})
	requireCode(t, err, codes.NotFound)
}

func TestLinkProviderEmailMismatch(t *testing.T) {
	fake := &fakeStore{
		linkProvider: func(context.Context, uuid.UUID, string, string, []byte) (store.User, error) {
			return store.User{}, store.ErrEmailMismatch
		},
	}
	client := newClient(t, fake)

	_, err := client.LinkProvider(context.Background(), &identityv1.LinkProviderRequest{
		UserId:          uuid.New().String(),
		Provider:        "github",
		ProviderSubject: "subject",
		Email:           "user@example.com",
	})
	requireCode(t, err, codes.FailedPrecondition)
}

func TestUnlinkProviderNotFound(t *testing.T) {
	fake := &fakeStore{
		unlink: func(context.Context, uuid.UUID, string, string) error {
			return store.ErrProviderNotLinked
		},
	}
	client := newClient(t, fake)

	_, err := client.UnlinkProvider(context.Background(), &identityv1.UnlinkProviderRequest{
		UserId:          uuid.New().String(),
		Provider:        "github",
		ProviderSubject: "subject",
	})
	requireCode(t, err, codes.NotFound)
}

func TestInternalErrorsAreNotLeaked(t *testing.T) {
	fake := &fakeStore{
		getOrCreate: func(context.Context, string, string, []byte) (store.User, error) {
			return store.User{}, errors.New("database exploded: secret detail")
		},
	}
	client := newClient(t, fake)

	_, err := client.GetOrCreate(context.Background(), &identityv1.GetOrCreateRequest{
		Provider:        "google",
		ProviderSubject: "subject",
		Email:           "user@example.com",
	})
	requireCode(t, err, codes.Internal)
	if status.Convert(err).Message() != "internal error" {
		t.Fatalf("internal error message leaked detail: %q", status.Convert(err).Message())
	}
}
