package pipeline

import (
	"context"
	"errors"

	"github.com/UdjinStolboviy/kadr-madia/internal/db"
	"github.com/UdjinStolboviy/kadr-madia/internal/delivery"
)

// DeliveryGrant implements delivery.Resolver: a kid that is active or
// verify_only gives read access to its project's private objects
// ({project_slug}/…) in the project's storage profile.
func (s *Service) DeliveryGrant(ctx context.Context, kid string) (*delivery.Grant, error) {
	k, err := s.store.GetSigningKey(ctx, kid)
	if errors.Is(err, db.ErrNotFound) {
		return nil, delivery.ErrUnknownKey
	}
	if err != nil {
		return nil, err
	}
	if k.Status != db.SigningActive && k.Status != db.SigningVerifyOnly {
		return nil, delivery.ErrUnknownKey
	}
	secret, err := s.keyring.Decrypt(k.SecretEnc, SigningKeySecretAAD(k.ID))
	if err != nil {
		return nil, err
	}
	p, err := s.store.GetProject(ctx, k.ProjectID)
	if errors.Is(err, db.ErrNotFound) {
		return nil, delivery.ErrUnknownKey
	}
	if err != nil {
		return nil, err
	}
	tgt, err := s.storageFor(ctx, p)
	if err != nil {
		return nil, err
	}
	return &delivery.Grant{Secret: secret, Prefix: p.Slug + "/", Driver: tgt.driver, Bucket: tgt.profile.PrivateBucket}, nil
}
