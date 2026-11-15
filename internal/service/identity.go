package service

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/JiffyCRM/identity-link/api/identity/v1"
	"github.com/JiffyCRM/identity-link/internal/hash"
	"github.com/JiffyCRM/identity-link/internal/store"
)

var providerPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

type IdentityService struct {
	identityv1.UnimplementedIdentityServiceServer

	store  store.Store
	hasher *hash.Hasher
	logger *slog.Logger
}

func New(st store.Store, hasher *hash.Hasher, logger *slog.Logger) *IdentityService {
	return &IdentityService{
		store:  st,
		hasher: hasher,
		logger: logger,
	}
}

func (s *IdentityService) GetOrCreate(ctx context.Context, req *identityv1.GetOrCreateRequest) (*identityv1.User, error) {
	provider, err := normalizeProvider(req.GetProvider())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	subject, err := normalizeSubject(req.GetProviderSubject())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := hash.Validate(req.GetEmail()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	user, err := s.store.GetOrCreate(ctx, provider, subject, s.hasher.Hash(req.GetEmail()))
	if err != nil {
		return nil, s.mapStoreError("GetOrCreate", err)
	}
	return userToProto(user), nil
}

func (s *IdentityService) GetByProvider(ctx context.Context, req *identityv1.GetByProviderRequest) (*identityv1.User, error) {
	provider, err := normalizeProvider(req.GetProvider())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	subject, err := normalizeSubject(req.GetProviderSubject())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	user, err := s.store.GetByProvider(ctx, provider, subject)
	if err != nil {
		return nil, s.mapStoreError("GetByProvider", err)
	}
	return userToProto(user), nil
}

func (s *IdentityService) GetByUserID(ctx context.Context, req *identityv1.GetByUserIDRequest) (*identityv1.User, error) {
	userID, err := parseUserID(req.GetUserId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	user, err := s.store.GetByUserID(ctx, userID)
	if err != nil {
		return nil, s.mapStoreError("GetByUserID", err)
	}
	return userToProto(user), nil
}

func (s *IdentityService) FindByEmail(ctx context.Context, req *identityv1.FindByEmailRequest) (*identityv1.User, error) {
	if err := hash.Validate(req.GetEmail()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	user, err := s.store.FindByEmailHash(ctx, s.hasher.Hash(req.GetEmail()))
	if err != nil {
		return nil, s.mapStoreError("FindByEmail", err)
	}
	return userToProto(user), nil
}

func (s *IdentityService) LinkProvider(ctx context.Context, req *identityv1.LinkProviderRequest) (*identityv1.User, error) {
	userID, err := parseUserID(req.GetUserId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	provider, err := normalizeProvider(req.GetProvider())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	subject, err := normalizeSubject(req.GetProviderSubject())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := hash.Validate(req.GetEmail()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	user, err := s.store.LinkProvider(ctx, userID, provider, subject, s.hasher.Hash(req.GetEmail()))
	if err != nil {
		return nil, s.mapStoreError("LinkProvider", err)
	}
	return userToProto(user), nil
}

func (s *IdentityService) UnlinkProvider(ctx context.Context, req *identityv1.UnlinkProviderRequest) (*emptypb.Empty, error) {
	userID, err := parseUserID(req.GetUserId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	provider, err := normalizeProvider(req.GetProvider())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	subject, err := normalizeSubject(req.GetProviderSubject())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	if err := s.store.UnlinkProvider(ctx, userID, provider, subject); err != nil {
		return nil, s.mapStoreError("UnlinkProvider", err)
	}
	return &emptypb.Empty{}, nil
}

func (s *IdentityService) ListProviders(ctx context.Context, req *identityv1.ListProvidersRequest) (*identityv1.ListProvidersResponse, error) {
	userID, err := parseUserID(req.GetUserId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	providers, err := s.store.ListProviders(ctx, userID)
	if err != nil {
		return nil, s.mapStoreError("ListProviders", err)
	}

	resp := &identityv1.ListProvidersResponse{
		Providers: make([]*identityv1.ProviderAccount, 0, len(providers)),
	}
	for _, provider := range providers {
		resp.Providers = append(resp.Providers, &identityv1.ProviderAccount{
			Id:              provider.ID.String(),
			Provider:        provider.Provider,
			ProviderSubject: provider.ProviderSubject,
			CreatedAt:       timestamppb.New(provider.CreatedAt),
		})
	}
	return resp, nil
}

func (s *IdentityService) mapStoreError(method string, err error) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return status.Error(codes.NotFound, "not found")
	case errors.Is(err, store.ErrEmailMismatch):
		return status.Error(codes.FailedPrecondition, "email does not match the user's stored email")
	case errors.Is(err, store.ErrAlreadyLinked):
		return status.Error(codes.AlreadyExists, "provider account is already linked to another user")
	case errors.Is(err, store.ErrProviderNotLinked):
		return status.Error(codes.NotFound, "provider account not found")
	case errors.Is(err, store.ErrConflict):
		return status.Error(codes.Aborted, "concurrent update conflict, retry the request")
	default:
		s.logger.Error("identity store error", "method", method, "error", err)
		return status.Error(codes.Internal, "internal error")
	}
}

func normalizeProvider(raw string) (string, error) {
	provider := strings.ToLower(strings.TrimSpace(raw))
	if provider == "" || len(provider) > 64 || !providerPattern.MatchString(provider) {
		return "", errors.New("provider must be 1-64 characters matching [a-z0-9][a-z0-9._-]*")
	}
	return provider, nil
}

func normalizeSubject(raw string) (string, error) {
	subject := strings.TrimSpace(raw)
	if subject == "" || len(subject) > 255 {
		return "", errors.New("provider_subject must be 1-255 characters")
	}
	return subject, nil
}

func parseUserID(raw string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil {
		return uuid.Nil, errors.New("user_id must be a valid UUID")
	}
	return id, nil
}

func userToProto(user store.User) *identityv1.User {
	return &identityv1.User{
		Id:        user.ID.String(),
		CreatedAt: timestamppb.New(user.CreatedAt),
		UpdatedAt: timestamppb.New(user.UpdatedAt),
	}
}
