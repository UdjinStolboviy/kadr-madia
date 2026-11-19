package pipeline

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/UdjinStolboviy/kadr-madia/internal/db"
	"github.com/UdjinStolboviy/kadr-madia/internal/delivery"
	"github.com/UdjinStolboviy/kadr-madia/internal/storage"
)

// Limits of signed URLs.
const (
	DefaultSignTTL = time.Hour
	MaxSignTTL     = 7 * 24 * time.Hour
	MaxSignItems   = 100
)

// SignItem asks for one signed URL.
type SignItem struct {
	AssetID   string `json:"asset_id"`
	Rendition string `json:"rendition"` // name, e.g. "main"; with several formats pass "name.format" (e.g. "main.avif")
	TTL       int    `json:"ttl"`       // seconds
	Download  string `json:"download"`
}

// SignedURL is one result.
type SignedURL struct {
	URL       string `json:"url"`
	ExpiresAt string `json:"expires_at"`
}

// SigningKeySecretAAD is the associated data of an encrypted signing secret.
func SigningKeySecretAAD(kid string) []byte { return []byte("signing_key:" + kid) }

// SignKey computes the signature of the canonical string shared with the SDK
// and kadr-edge: GET\n/{key}\n{exp}\n{download}.
func SignKey(secret, key string, exp int64, download string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte("GET\n/" + strings.TrimLeft(key, "/") + "\n" + strconv.FormatInt(exp, 10) + "\n" + download))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// SignKeyPrefix signs a directory-scoped URL (HLS packages): the canonical
// string is GETP\n/{dir}/\n{exp}\n where dir is the directory of key. kadr-edge
// accepts it for any sibling file when the URL carries prefix=1.
func SignKeyPrefix(secret, key string, exp int64) string {
	dir := strings.TrimLeft(key, "/")
	dir = dir[:strings.LastIndex(dir, "/")+1]
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte("GETP\n/" + dir + "\n" + strconv.FormatInt(exp, 10) + "\n"))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// SignURLs signs private rendition URLs with the project's active signing key.
func (s *Service) SignURLs(ctx context.Context, projectID string, items []SignItem) ([]SignedURL, error) {
	if len(items) == 0 || len(items) > MaxSignItems {
		return nil, validationErr(map[string]string{"items": "1–100 items required"})
	}
	// The private delivery host belongs to the project's storage profile
	// (one kadr-edge per Cloudflare account); KADR_EDGE_BASE_URL is the default.
	// Without one, clients read straight from the bucket with presigned URLs.
	edgeBase := s.edgeBaseURL
	project, err := s.store.GetProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	tgt, terr := s.storageFor(ctx, project)
	if terr == nil && tgt.profile.PrivateBaseURL != nil && *tgt.profile.PrivateBaseURL != "" {
		edgeBase = *tgt.profile.PrivateBaseURL
	}
	if edgeBase == "" {
		if terr != nil {
			return nil, terr
		}
		return s.presignDirect(ctx, projectID, tgt, items)
	}
	k, err := s.store.GetActiveSigningKey(ctx, projectID)
	if errors.Is(err, db.ErrNotFound) {
		return nil, apiErr(http.StatusConflict, "no_signing_key", "the project has no active signing key; create one in the admin console")
	}
	if err != nil {
		return nil, err
	}
	secret, err := s.keyring.Decrypt(k.SecretEnc, SigningKeySecretAAD(k.ID))
	if err != nil {
		return nil, err
	}
	out := make([]SignedURL, len(items))
	for i, it := range items {
		key, prefixMode, ttl, err := s.signableKey(ctx, projectID, it)
		if err != nil {
			return nil, err
		}
		exp := s.now().Add(ttl).Unix()
		u := strings.TrimRight(edgeBase, "/") + "/" + escapeKey(key) + "?exp=" + strconv.FormatInt(exp, 10) +
			"&kid=" + url.QueryEscape(k.ID)
		if prefixMode {
			u += "&sig=" + SignKeyPrefix(string(secret), key, exp) + "&prefix=1"
		} else {
			u += "&sig=" + SignKey(string(secret), key, exp, it.Download)
			if it.Download != "" {
				u += "&download=" + url.QueryEscape(it.Download)
			}
		}
		out[i] = SignedURL{URL: u, ExpiresAt: rfc3339(time.Unix(exp, 0))}
	}
	return out, nil
}

// signableKey resolves one item to the storage key of a ready private
// rendition, whether it is an HLS package, and the validated TTL.
func (s *Service) signableKey(ctx context.Context, projectID string, it SignItem) (key string, prefixMode bool, ttl time.Duration, err error) {
	a, err := s.loadAsset(ctx, projectID, it.AssetID)
	if err != nil {
		return "", false, 0, err
	}
	if a.Visibility != "private" || a.BlobID == nil || a.Status != db.AssetReady {
		return "", false, 0, apiErr(http.StatusConflict, "not_signable", "asset %s is not a ready private asset", it.AssetID)
	}
	ttl = time.Duration(it.TTL) * time.Second
	if it.TTL == 0 {
		ttl = DefaultSignTTL
	}
	if ttl < 0 || ttl > MaxSignTTL {
		return "", false, 0, validationErr(map[string]string{"ttl": "must be between 1 second and 7 days"})
	}
	rs, err := s.store.ListRenditions(ctx, *a.BlobID)
	if err != nil {
		return "", false, 0, err
	}
	for _, r := range rs {
		if it.Rendition == "" || it.Rendition == r.Name || it.Rendition == r.Name+"."+r.Format {
			// HLS playlists: one signature covers the whole package directory.
			return r.StorageKey, r.Format == hlsRenditionFormat, ttl, nil
		}
	}
	return "", false, 0, apiErr(http.StatusNotFound, CodeNotFound, "asset %s has no rendition %q", it.AssetID, it.Rendition)
}

// presignDirect answers POST /v1/signed-urls with presigned GETs on the
// storage provider itself (no delivery host): bytes never pass through Kadr
// or a Worker. URLs are stable within a window of min(ttl, 1h) so caches hit.
// HLS packages need a delivery host (each segment would need its own signature).
func (s *Service) presignDirect(ctx context.Context, projectID string, tgt *target, items []SignItem) ([]SignedURL, error) {
	out := make([]SignedURL, len(items))
	for i, it := range items {
		key, prefixMode, ttl, err := s.signableKey(ctx, projectID, it)
		if err != nil {
			return nil, err
		}
		if prefixMode {
			return nil, apiErr(http.StatusConflict, "hls_requires_delivery_host",
				"private HLS needs a delivery host (kadr-edge or KADR_DELIVERY_ADDR); sign the MP4 rendition instead")
		}
		o := storage.PresignGetOptions{TTL: ttl, StableWindow: min(ttl, time.Hour), Now: s.now()}
		if it.Download != "" {
			o.ContentDisposition = delivery.ContentDisposition(it.Download)
		}
		r, err := tgt.driver.PresignDownload(ctx, tgt.profile.PrivateBucket, key, o)
		if err != nil {
			return nil, apiErr(http.StatusBadGateway, CodeStorageError, "could not presign %s: %v", key, err)
		}
		out[i] = SignedURL{URL: r.URL, ExpiresAt: rfc3339(r.ExpiresAt)}
	}
	return out, nil
}

func escapeKey(key string) string {
	parts := strings.Split(strings.TrimLeft(key, "/"), "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}
