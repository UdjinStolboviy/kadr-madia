package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/UdjinStolboviy/kadr-madia/internal/db"
	"github.com/UdjinStolboviy/kadr-madia/internal/engine/file"
	"github.com/UdjinStolboviy/kadr-madia/internal/preset"
	"github.com/UdjinStolboviy/kadr-madia/internal/queue"
	"github.com/UdjinStolboviy/kadr-madia/internal/storage"
	"github.com/UdjinStolboviy/kadr-madia/internal/webhook"
)

var namespaceRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@-]{0,127}$`)

// ValidNamespace reports whether ns is an acceptable namespace id.
func ValidNamespace(ns string) bool { return namespaceRe.MatchString(ns) }

// CreateUploadInput is the body of POST /v1/uploads.
type CreateUploadInput struct {
	Preset      string          `json:"preset"`
	Mode        string          `json:"mode"` // compact | balanced | quality (instead of preset)
	Namespace   string          `json:"namespace"`
	Visibility  string          `json:"visibility"`
	Filename    string          `json:"filename"`
	ContentType string          `json:"content_type"`
	Size        int64           `json:"size"`
	SHA256      string          `json:"sha256"`
	Metadata    json.RawMessage `json:"metadata"`
	Multipart   bool            `json:"multipart,omitempty"`
}

// CreateUploadResult is either a ticket (201) or a deduplicated ready asset (200).
type CreateUploadResult struct {
	Ticket *UploadTicket
	Asset  *AssetView
}

// common holds validated fields shared by ticket and server-side uploads.
type common struct {
	preset   *db.Preset
	kind     string
	params   preset.ImageParams
	vparams  preset.VideoParams
	fparams  preset.FileParams
	ns       string
	vis      string
	filename *string
	metadata json.RawMessage
}

func (s *Service) validateCommon(ctx context.Context, projectID, presetSlug, mode, hint, ns, vis, filename string, metadata json.RawMessage, errs map[string]string) (*common, error) {
	c := &common{ns: strings.TrimSpace(ns), vis: strings.TrimSpace(vis)}
	if !ValidNamespace(c.ns) {
		errs["namespace"] = "required; 1–128 characters of [A-Za-z0-9._:@-], starting with a letter or digit"
	} else if err := checkNamespace(ctx, c.ns); err != nil {
		return nil, err
	}
	if c.vis != "public" && c.vis != "private" {
		errs["visibility"] = `must be "public" or "private"`
	}
	if filename = strings.TrimSpace(filename); filename != "" {
		if len(filename) > 255 || strings.ContainsAny(filename, "\x00/\\") {
			errs["filename"] = "must be a plain file name of at most 255 bytes"
		} else {
			c.filename = &filename
		}
	}
	meta, msg := normalizeMetadata(metadata)
	if msg != "" {
		errs["metadata"] = msg
	}
	c.metadata = meta
	presetSlug, mode = strings.TrimSpace(presetSlug), strings.TrimSpace(mode)
	if presetSlug != "" && mode != "" {
		errs["mode"] = "send either preset or mode, not both"
		return c, nil
	}
	if presetSlug == "" {
		// Compression mode: the preset is image-<mode> or video-<mode>, chosen
		// by the kind of the file; the project's default mode applies when
		// neither a preset nor a mode is given.
		if mode == "" {
			project, err := s.store.GetProject(ctx, projectID)
			if err != nil {
				return nil, err
			}
			mode = project.DefaultMode
		}
		if !preset.ValidMode(mode) {
			errs["mode"] = "must be compact, balanced or quality"
			return c, nil
		}
		kind := kindFromHint(hint)
		if kind == "" {
			errs["preset"] = "required: the file type could not be determined, send preset or a content type"
			return c, nil
		}
		presetSlug = preset.ModeSlug(kind, mode)
	}
	p, err := s.store.GetPresetBySlug(ctx, projectID, presetSlug)
	if errors.Is(err, db.ErrNotFound) {
		return nil, apiErr(http.StatusNotFound, CodePresetNotFound, "preset %q does not exist in this project", presetSlug)
	}
	if err != nil {
		return nil, err
	}
	c.kind = p.Kind
	switch p.Kind {
	case "image":
		err = json.Unmarshal(p.Params, &c.params)
	case "video":
		err = json.Unmarshal(p.Params, &c.vparams)
	case "file":
		err = json.Unmarshal(p.Params, &c.fparams)
	default:
		return nil, apiErr(http.StatusUnsupportedMediaType, CodeUnsupportedMedia, "preset %q has unsupported kind %q", p.Slug, p.Kind)
	}
	if err != nil {
		return nil, fmt.Errorf("preset %s params: %w", p.ID, err)
	}
	c.preset = p
	return c, nil
}

// normalizeMetadata accepts a JSON object (or null/absent) of bounded size.
func normalizeMetadata(raw json.RawMessage) (json.RawMessage, string) {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || string(t) == "null" {
		return json.RawMessage("{}"), ""
	}
	if len(t) > MaxMetadataBytes {
		return nil, fmt.Sprintf("must be at most %d bytes of JSON", MaxMetadataBytes)
	}
	var obj map[string]any
	if err := json.Unmarshal(t, &obj); err != nil || obj == nil {
		return nil, "must be a JSON object"
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, t); err != nil {
		return nil, "must be a JSON object"
	}
	return json.RawMessage(buf.Bytes()), ""
}

// CreateUpload validates a ticket request, reserves quota and presigns the
// PUT. When sha256 matches a ready blob it returns a new ready asset instead.
func (s *Service) CreateUpload(ctx context.Context, projectID string, in CreateUploadInput) (*CreateUploadResult, error) {
	errs := map[string]string{}
	c, err := s.validateCommon(ctx, projectID, in.Preset, in.Mode, in.ContentType+" "+in.Filename, in.Namespace, in.Visibility, in.Filename, in.Metadata, errs)
	if err != nil {
		return nil, err
	}
	if in.Size <= 0 {
		errs["size"] = "required; the exact file size in bytes"
	}
	var sha []byte
	if in.SHA256 != "" {
		if sha, err = hex.DecodeString(strings.ToLower(in.SHA256)); err != nil || len(sha) != sha256.Size {
			errs["sha256"] = "must be 64 hex characters"
		}
	}
	ct := normalizeContentType(in.ContentType)
	if ct == "" {
		errs["content_type"] = "required"
	}
	if len(errs) > 0 {
		return nil, validationErr(errs)
	}
	if maxPut := c.maxPutBytes(); in.Size > maxPut {
		return nil, apiErr(http.StatusRequestEntityTooLarge, CodeTooLarge,
			"files larger than %d MB are not supported by a single upload; use a smaller file", maxPut>>20)
	}
	if lim := c.maxInputBytes(); lim > 0 && in.Size > lim {
		return nil, apiErr(http.StatusRequestEntityTooLarge, CodeTooLarge, "file is %d bytes; preset %q accepts at most %d bytes", in.Size, c.preset.Slug, lim)
	}
	if !c.acceptsContentType(ct) {
		return nil, apiErr(http.StatusUnsupportedMediaType, CodeUnsupportedMedia,
			"content type %q is not accepted by preset %q (%s)", ct, c.preset.Slug, c.acceptedDesc())
	}
	project, err := s.store.GetProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if sha != nil {
		if a, err := s.createDedupAsset(ctx, project, c, sha); err != nil || a != nil {
			if a != nil {
				return &CreateUploadResult{Asset: a}, nil
			}
			return nil, err
		}
	}
	tgt, err := s.storageFor(ctx, project)
	if err != nil {
		return nil, err
	}

	now := s.now()
	u := &db.Upload{
		ID: db.NewID(db.PrefixUpload), ProjectID: projectID, NS: c.ns, PresetID: c.preset.ID, PresetVersion: c.preset.CurrentVersion,
		Visibility: c.vis, Method: db.UploadMethodPut, Filename: c.filename, ContentType: &ct, DeclaredBytes: in.Size,
		DeclaredSHA256: sha, Metadata: c.metadata, Status: db.UploadPending, ExpiresAt: now.Add(s.uploadTTL),
		CreatedAt: now, UpdatedAt: now,
	}
	key := "tmp/" + project.Slug + "/" + u.ID
	u.TmpKey = &key
	if (in.Multipart && in.Size > MinMultipartPartBytes) || (c.kind != "image" && in.Size > s.mpThreshold) {
		return s.createMultipartTicket(ctx, tgt, u, c, key, ct, in.Size)
	}
	pr, err := tgt.driver.PresignPut(ctx, tgt.profile.PrivateBucket, key, storage.PresignPutOptions{
		ContentType: ct, ContentLength: in.Size, TTL: s.uploadTTL,
	})
	if err != nil {
		return nil, fmt.Errorf("presign upload: %w", err)
	}
	err = s.store.InTx(ctx, func(tx *db.Store) error {
		if err := s.reserve(ctx, tx, projectID, c.ns, in.Size); err != nil {
			return err
		}
		return tx.CreateUpload(ctx, u)
	})
	if err != nil {
		return nil, err
	}
	headers := map[string]string{}
	for k, v := range pr.Headers {
		if len(v) > 0 {
			headers[http.CanonicalHeaderKey(k)] = v[0]
		}
	}
	headers["Content-Type"] = ct
	headers["Content-Length"] = strconv.FormatInt(in.Size, 10)
	return &CreateUploadResult{Ticket: &UploadTicket{
		UploadID: u.ID, Method: db.UploadMethodPut, URL: pr.URL, Headers: headers, ExpiresAt: rfc3339(u.ExpiresAt),
	}}, nil
}

// createDedupAsset creates a ready asset that references an existing ready
// blob with the same content. It returns (nil, nil) when there is no match.
func (s *Service) createDedupAsset(ctx context.Context, project *db.Project, c *common, sha []byte) (*AssetView, error) {
	var view *AssetView
	err := s.store.InTx(ctx, func(tx *db.Store) error {
		b, err := tx.FindReadyBlob(ctx, project.ID, c.vis, c.preset.ID, c.preset.CurrentVersion, sha)
		if errors.Is(err, db.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := s.checkQuota(ctx, tx, project.ID, c.ns, b.TotalBytes); err != nil {
			return err
		}
		if ok, err := tx.AddBlobRef(ctx, b.ID); err != nil || !ok {
			return err
		}
		now := s.now()
		a := &db.Asset{ID: db.NewID(db.PrefixAsset), ProjectID: project.ID, NS: c.ns, BlobID: &b.ID, Kind: b.Kind,
			Visibility: c.vis, Filename: c.filename, Metadata: c.metadata, Status: db.AssetReady, PresetID: &c.preset.ID,
			PresetVersion: ptr(int64(c.preset.CurrentVersion)), CreatedAt: now, ReadyAt: &now}
		if err := tx.CreateAsset(ctx, a); err != nil {
			return err
		}
		if err := tx.AdjustUsage(ctx, project.ID, c.ns, db.QuotaDelta{UsedBytes: b.TotalBytes, AssetCount: 1}); err != nil {
			return err
		}
		if err := tx.AddUsageDaily(ctx, project.ID, c.ns, now, db.UsageDelta{Uploads: 1, AssetsReady: 1, DedupHits: 1}); err != nil {
			return err
		}
		if err := s.updateQuotaAlerts(ctx, tx, project.ID, c.ns); err != nil {
			return err
		}
		if view, err = s.assetView(ctx, tx, a); err != nil {
			return err
		}
		return s.emit(ctx, tx, project.ID, webhook.EventAssetReady, map[string]any{"asset": view, "metadata": view.Metadata})
	})
	if err != nil || view == nil {
		return nil, err
	}
	s.dedupHits.Inc()
	s.afterCommit(view.ID)
	return view, nil
}

// loadUpload returns an upload of the project or 404.
func (s *Service) loadUpload(ctx context.Context, projectID, id string) (*db.Upload, error) {
	u, err := s.store.GetUpload(ctx, id)
	if errors.Is(err, db.ErrNotFound) || (err == nil && u.ProjectID != projectID) {
		return nil, apiErr(http.StatusNotFound, CodeNotFound, "upload %s not found", id)
	}
	if err != nil {
		return nil, err
	}
	if err := checkNamespace(ctx, u.NS); err != nil {
		return nil, err
	}
	return u, nil
}

// CompleteUpload verifies the uploaded object, starts processing and waits
// up to wait for the asset to become ready or failed. Idempotent.
func (s *Service) CompleteUpload(ctx context.Context, projectID, uploadID string, parts []CompletedPartInput, wait time.Duration) (*AssetView, error) {
	u, err := s.loadUpload(ctx, projectID, uploadID)
	if err != nil {
		return nil, err
	}
	switch u.Status {
	case db.UploadPending:
		if u.Method == db.UploadMethodMultipart {
			if err := s.finishMultipart(ctx, u, parts); err != nil {
				return nil, err
			}
		}
		assetID, err := s.promote(ctx, u, true)
		if err != nil {
			return nil, err
		}
		return s.waitAsset(ctx, assetID, wait)
	case db.UploadExpired:
		return nil, apiErr(http.StatusGone, CodeUploadExpired, "upload %s expired before the file was uploaded", u.ID)
	case db.UploadAborted:
		return nil, apiErr(http.StatusConflict, CodeUploadAborted, "upload %s was cancelled", u.ID)
	}
	if u.AssetID == nil {
		// Failed during verification (size mismatch) before an asset existed.
		return nil, apiErr(http.StatusUnprocessableEntity, CodeSizeMismatch, "upload %s failed verification", u.ID)
	}
	return s.waitAsset(ctx, *u.AssetID, wait)
}

// promote moves a pending upload whose object exists to uploaded: it HEADs
// the tmp object, verifies its size, creates the processing asset and
// enqueues process_image in one transaction. requireObject=true returns
// upload_incomplete when the object is missing (complete); the sweeper
// passes false and handles absence itself. Returns the asset id.
func (s *Service) promote(ctx context.Context, u *db.Upload, requireObject bool) (string, error) {
	project, err := s.store.GetProject(ctx, u.ProjectID)
	if err != nil {
		return "", err
	}
	tgt, err := s.storageFor(ctx, project)
	if err != nil {
		return "", err
	}
	info, err := tgt.driver.Head(ctx, tgt.profile.PrivateBucket, deref(u.TmpKey))
	if errors.Is(err, storage.ErrNotFound) {
		if requireObject {
			return "", apiErr(http.StatusConflict, CodeUploadIncomplete, "the file for upload %s has not been uploaded yet", u.ID)
		}
		return "", err
	}
	if err != nil {
		return "", apiErr(http.StatusBadGateway, CodeStorageError, "could not verify the uploaded file: %v", err)
	}
	if info.Size != u.DeclaredBytes {
		s.failUploadVerification(ctx, u, tgt)
		return "", apiErr(http.StatusUnprocessableEntity, CodeSizeMismatch,
			"uploaded file is %d bytes, but the ticket declared %d bytes", info.Size, u.DeclaredBytes)
	}

	now := s.now()
	kind := "image"
	jobKind, pool := JobProcessImage, queue.PoolImage
	if pv, err := s.store.GetPresetVersion(ctx, u.PresetID, u.PresetVersion); err == nil && pv.Kind != "image" {
		kind, jobKind, pool = pv.Kind, jobKindFor(pv.Kind), poolFor(pv.Kind)
	}
	a := &db.Asset{ID: db.NewID(db.PrefixAsset), ProjectID: u.ProjectID, NS: u.NS, UploadID: &u.ID, Kind: kind,
		Visibility: u.Visibility, Filename: u.Filename, Metadata: u.Metadata, Status: db.AssetProcessing,
		PresetID: &u.PresetID, PresetVersion: ptr(int64(u.PresetVersion)), CreatedAt: now}
	assetID := a.ID
	err = s.store.InTx(ctx, func(tx *db.Store) error {
		ok, err := tx.TransitionUpload(ctx, u.ID, []string{db.UploadPending}, db.UploadUploaded, &a.ID, now)
		if err != nil {
			return err
		}
		if !ok {
			// A concurrent complete (or the sweeper) won; use its asset.
			cur, err := tx.GetUpload(ctx, u.ID)
			if err != nil {
				return err
			}
			if cur.AssetID == nil {
				return apiErr(http.StatusConflict, CodeUploadNotPending, "upload %s is %s", u.ID, cur.Status)
			}
			assetID = *cur.AssetID
			return nil
		}
		if err := tx.CreateAsset(ctx, a); err != nil {
			return err
		}
		if err := tx.AddUsageDaily(ctx, u.ProjectID, u.NS, now, db.UsageDelta{Uploads: 1}); err != nil {
			return err
		}
		_, _, err = s.queue.Enqueue(ctx, tx, queue.EnqueueOptions{
			Kind: jobKind, Pool: pool, DedupeKey: u.ID, Payload: processPayload{UploadID: u.ID},
		})
		return err
	})
	if err != nil {
		return "", err
	}
	return assetID, nil
}

// failUploadVerification marks a pending upload failed (wrong object size),
// releases its reservation and deletes the object.
func (s *Service) failUploadVerification(ctx context.Context, u *db.Upload, tgt *target) {
	err := s.store.InTx(ctx, func(tx *db.Store) error {
		ok, err := tx.TransitionUpload(ctx, u.ID, []string{db.UploadPending}, db.UploadFailed, nil, s.now())
		if err != nil || !ok {
			return err
		}
		return tx.AdjustUsage(ctx, u.ProjectID, u.NS, db.QuotaDelta{ReservedBytes: -u.DeclaredBytes})
	})
	if err != nil {
		s.log.ErrorContext(ctx, "fail upload after size mismatch", "upload_id", u.ID, "err", err)
	}
	if err := tgt.driver.Delete(ctx, tgt.profile.PrivateBucket, deref(u.TmpKey)); err != nil {
		s.log.WarnContext(ctx, "delete rejected upload object", "upload_id", u.ID, "err", err)
	}
}

// AbortUpload cancels a pending upload, deletes its object and releases the
// reservation. Aborting twice is a no-op.
func (s *Service) AbortUpload(ctx context.Context, projectID, uploadID string) error {
	u, err := s.loadUpload(ctx, projectID, uploadID)
	if err != nil {
		return err
	}
	switch u.Status {
	case db.UploadAborted:
		return nil
	case db.UploadPending:
	default:
		return apiErr(http.StatusConflict, CodeUploadNotPending, "upload %s is %s and can no longer be cancelled", u.ID, u.Status)
	}
	var aborted bool
	err = s.store.InTx(ctx, func(tx *db.Store) error {
		ok, err := tx.TransitionUpload(ctx, u.ID, []string{db.UploadPending}, db.UploadAborted, nil, s.now())
		if err != nil || !ok {
			return err
		}
		aborted = true
		if err := tx.AdjustUsage(ctx, u.ProjectID, u.NS, db.QuotaDelta{ReservedBytes: -u.DeclaredBytes}); err != nil {
			return err
		}
		return s.updateQuotaAlerts(ctx, tx, u.ProjectID, u.NS)
	})
	if err != nil {
		return err
	}
	if !aborted {
		return s.AbortUpload(ctx, projectID, uploadID) // state changed concurrently; re-evaluate
	}
	if project, err := s.store.GetProject(ctx, u.ProjectID); err == nil {
		if tgt, err := s.storageFor(ctx, project); err == nil {
			s.abortMultipart(ctx, u, tgt)
			if err := tgt.driver.Delete(ctx, tgt.profile.PrivateBucket, deref(u.TmpKey)); err != nil {
				s.log.WarnContext(ctx, "delete aborted upload object", "upload_id", u.ID, "err", err)
			}
		}
	}
	return nil
}

// ServerUploadInput describes POST /v1/assets (multipart/form-data).
type ServerUploadInput struct {
	Preset      string
	Mode        string
	Namespace   string
	Visibility  string
	Metadata    json.RawMessage
	Filename    string
	ContentType string
}

// Staged is a server-side upload written to TmpDir/staged.
type Staged struct {
	ID     string // future upload id
	Path   string
	Size   int64
	SHA256 []byte
	used   bool
}

// Discard removes the staged file unless an upload took ownership of it.
func (st *Staged) Discard() {
	if st != nil && !st.used {
		_ = os.Remove(st.Path)
	}
}

// StageServerUpload streams a file sent directly to Kadr to local disk,
// hashing it on the way. More than MaxServerUploadBytes yields 413.
func (s *Service) StageServerUpload(r io.Reader) (*Staged, error) {
	stagedDir := filepath.Join(s.tmpDir, "staged")
	if err := os.MkdirAll(stagedDir, 0o750); err != nil {
		return nil, fmt.Errorf("create staging dir: %w", err)
	}
	st := &Staged{ID: db.NewID(db.PrefixUpload)}
	st.Path = filepath.Join(stagedDir, st.ID)
	size, sha, err := writeHashed(st.Path, r, MaxServerUploadBytes)
	if err != nil {
		_ = os.Remove(st.Path)
		if errors.Is(err, errTooLarge) {
			return nil, apiErr(http.StatusRequestEntityTooLarge, CodeTooLarge, "server-side uploads are limited to %d MB", MaxServerUploadBytes>>20)
		}
		return nil, fmt.Errorf("stage upload: %w", err)
	}
	st.Size, st.SHA256 = size, sha
	return st, nil
}

// CreateServerAsset processes a staged file through the queue like a ticket
// upload and waits up to wait. The caller must Discard st afterwards.
func (s *Service) CreateServerAsset(ctx context.Context, projectID string, in ServerUploadInput, st *Staged, wait time.Duration) (*AssetView, error) {
	errs := map[string]string{}
	c, err := s.validateCommon(ctx, projectID, in.Preset, in.Mode, in.ContentType+" "+in.Filename, in.Namespace, in.Visibility, in.Filename, in.Metadata, errs)
	if err != nil {
		return nil, err
	}
	if st == nil || st.Size == 0 {
		errs["file"] = "required and must not be empty"
	}
	if len(errs) > 0 {
		return nil, validationErr(errs)
	}
	if lim := c.maxInputBytes(); lim > 0 && st.Size > lim {
		return nil, apiErr(http.StatusRequestEntityTooLarge, CodeTooLarge, "file is %d bytes; preset %q accepts at most %d bytes", st.Size, c.preset.Slug, lim)
	}
	project, err := s.store.GetProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if a, err := s.createDedupAsset(ctx, project, c, st.SHA256); err != nil || a != nil {
		return a, err
	}
	var ct *string
	if v := normalizeContentType(in.ContentType); v != "" && v != "application/octet-stream" {
		ct = &v
	}

	now := s.now()
	ref := st.ID
	u := &db.Upload{ID: st.ID, ProjectID: projectID, NS: c.ns, PresetID: c.preset.ID, PresetVersion: c.preset.CurrentVersion,
		Visibility: c.vis, Method: db.UploadMethodServer, Filename: c.filename, ContentType: ct, DeclaredBytes: st.Size,
		DeclaredSHA256: st.SHA256, SourceRef: &ref, Metadata: c.metadata, Status: db.UploadUploaded, ExpiresAt: now.Add(s.uploadTTL),
		CreatedAt: now, UpdatedAt: now}
	a := &db.Asset{ID: db.NewID(db.PrefixAsset), ProjectID: projectID, NS: c.ns, UploadID: &u.ID, Kind: c.kind,
		Visibility: c.vis, Filename: c.filename, Metadata: c.metadata, Status: db.AssetProcessing,
		PresetID: &c.preset.ID, PresetVersion: ptr(int64(c.preset.CurrentVersion)), CreatedAt: now}
	u.AssetID = &a.ID
	err = s.store.InTx(ctx, func(tx *db.Store) error {
		if err := s.reserve(ctx, tx, projectID, c.ns, st.Size); err != nil {
			return err
		}
		if err := tx.CreateUpload(ctx, u); err != nil {
			return err
		}
		if err := tx.CreateAsset(ctx, a); err != nil {
			return err
		}
		if err := tx.AddUsageDaily(ctx, projectID, c.ns, now, db.UsageDelta{Uploads: 1}); err != nil {
			return err
		}
		_, _, err := s.queue.Enqueue(ctx, tx, queue.EnqueueOptions{
			Kind: jobKindFor(c.kind), Pool: poolFor(c.kind), DedupeKey: u.ID, Payload: processPayload{UploadID: u.ID},
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	st.used = true
	return s.waitAsset(ctx, a.ID, wait)
}

// waitAsset waits until the asset leaves processing or wait elapses, then
// returns its current view.
func (s *Service) waitAsset(ctx context.Context, assetID string, wait time.Duration) (*AssetView, error) {
	wait = min(max(wait, 0), MaxWait)
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for {
		ch, unsubscribe := s.notifier.subscribe(assetID)
		a, err := s.store.GetAsset(ctx, assetID)
		if err != nil {
			unsubscribe()
			return nil, err
		}
		if a.Status != db.AssetProcessing || wait == 0 {
			unsubscribe()
			return s.assetView(ctx, s.store, a)
		}
		select {
		case <-ch:
			unsubscribe()
			continue
		case <-deadline.C:
			unsubscribe()
			wait = 0 // one final read
		case <-ctx.Done():
			unsubscribe()
			return nil, ctx.Err()
		}
	}
}

// ParseWait parses ?wait= as a Go duration ("5s") or plain seconds ("5"),
// capped at MaxWait.
func ParseWait(v string) (time.Duration, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		n, nerr := strconv.ParseFloat(v, 64)
		if nerr != nil {
			return 0, fmt.Errorf("wait must be a duration like 5s")
		}
		d = time.Duration(n * float64(time.Second))
	}
	if d < 0 {
		return 0, fmt.Errorf("wait must not be negative")
	}
	return min(d, MaxWait), nil
}

var errTooLarge = errors.New("file too large")

// writeHashed streams r into path, returning size and SHA-256. More than
// limit bytes yields errTooLarge.
func writeHashed(path string, r io.Reader, limit int64) (int64, []byte, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, nil, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(r, limit+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return n, nil, err
	}
	if n > limit {
		return n, nil, errTooLarge
	}
	return n, h.Sum(nil), nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func jobKindFor(kind string) string {
	switch kind {
	case "video":
		return JobProcessVideo
	case "file":
		return JobProcessFile
	}
	return JobProcessImage
}

func poolFor(kind string) string {
	switch kind {
	case "video":
		return queue.PoolVideo
	case "file":
		return queue.PoolFile
	}
	return queue.PoolImage
}

func (c *common) maxPutBytes() int64 {
	if c.kind == "video" || c.kind == "file" {
		return MaxVideoPutBytes
	}
	return MaxPutBytes
}

func (c *common) maxInputBytes() int64 {
	switch c.kind {
	case "video":
		return c.vparams.Limits.MaxInputBytes
	case "file":
		return c.fparams.Limits.MaxInputBytes
	}
	return c.params.Limits.MaxInputBytes
}

func (c *common) acceptsContentType(ct string) bool {
	switch c.kind {
	case "video":
		return strings.HasPrefix(ct, "video/")
	case "file":
		// The declared type only has to be plausible: the engine decides by content.
		f := file.FormatForContentType(ct)
		return ct == "application/octet-stream" || (f != "" && slices.Contains(c.fparams.Limits.Accept, f))
	}
	return acceptsContentType(c.params.Limits.Accept, ct)
}

func (c *common) acceptedDesc() string {
	switch c.kind {
	case "video":
		return "accepted: video/*"
	case "file":
		return "accepted documents: " + strings.Join(c.fparams.Limits.Accept, ", ")
	}
	return "accepted formats: " + strings.Join(c.params.Limits.Accept, ", ")
}

const maxParts = 10000

// createMultipartTicket starts an S3 multipart upload and presigns every part.
func (s *Service) createMultipartTicket(ctx context.Context, tgt *target, u *db.Upload, c *common, key, ct string, size int64) (*CreateUploadResult, error) {
	partSize := s.multipartPartSizeForBytes(size)
	n := int((size + partSize - 1) / partSize)
	mp, err := tgt.driver.CreateMultipart(ctx, tgt.profile.PrivateBucket, key, ct)
	if err != nil {
		return nil, apiErr(http.StatusBadGateway, CodeStorageError, "could not start a multipart upload: %v", err)
	}
	u.Method, u.MultipartID, u.PartSize = db.UploadMethodMultipart, &mp.UploadID, partSize
	// Multipart sessions live longer so paused/resumed uploads can refresh part URLs.
	u.ExpiresAt = u.CreatedAt.Add(max(s.uploadTTL, s.mpSessionTTL))
	remaining := u.ExpiresAt.Sub(s.now())
	urlTTL := min(s.mpPartTTL, remaining)
	if urlTTL < time.Second {
		urlTTL = remaining
	}
	parts := make([]PartTicket, n)
	for i := range parts {
		pr, err := tgt.driver.PresignUploadPart(ctx, mp, int32(i+1), urlTTL)
		if err != nil {
			_ = tgt.driver.AbortMultipart(context.WithoutCancel(ctx), mp)
			return nil, fmt.Errorf("presign part: %w", err)
		}
		parts[i] = PartTicket{PartNumber: i + 1, URL: pr.URL}
	}
	err = s.store.InTx(ctx, func(tx *db.Store) error {
		if err := s.reserve(ctx, tx, u.ProjectID, c.ns, size); err != nil {
			return err
		}
		return tx.CreateUpload(ctx, u)
	})
	if err != nil {
		_ = tgt.driver.AbortMultipart(context.WithoutCancel(ctx), mp)
		return nil, err
	}
	return &CreateUploadResult{Ticket: &UploadTicket{
		UploadID: u.ID, Method: db.UploadMethodMultipart,
		ExpiresAt: rfc3339(s.now().Add(urlTTL)), SessionExpiresAt: rfc3339(u.ExpiresAt),
		PartSize: partSize, Parts: parts,
	}}, nil
}

func (s *Service) multipartPartSizeForBytes(size int64) int64 {
	partSize := max(s.partSize, MinMultipartPartBytes)
	if minSize := (size + maxParts - 1) / maxParts; minSize > partSize {
		partSize = (minSize + (1 << 20) - 1) &^ ((1 << 20) - 1) // round up to MB
	}
	return partSize
}

func (s *Service) resolveMultipartPartSize(u *db.Upload) int64 {
	if u.PartSize > 0 {
		return u.PartSize
	}
	return s.multipartPartSizeForBytes(u.DeclaredBytes)
}

// GetUpload returns the current state of an upload ticket. For pending
// MULTIPART uploads, it queries storage for parts uploaded so far so a client
// can resume without re-uploading completed parts.
func (s *Service) GetUpload(ctx context.Context, projectID, uploadID string) (*UploadStatusView, error) {
	u, err := s.loadUpload(ctx, projectID, uploadID)
	if err != nil {
		return nil, err
	}
	presetSlug := ""
	if pv, err := s.store.GetPresetVersion(ctx, u.PresetID, u.PresetVersion); err == nil {
		presetSlug = pv.Slug
	} else if !errors.Is(err, db.ErrNotFound) {
		return nil, err
	}
	out := &UploadStatusView{
		UploadID: u.ID, Status: u.Status, Method: u.Method, Namespace: u.NS, Preset: presetSlug,
		Visibility: u.Visibility, Filename: u.Filename, ContentType: u.ContentType,
		Size: u.DeclaredBytes, ExpiresAt: rfc3339(u.ExpiresAt), AssetID: u.AssetID, CreatedAt: rfc3339(u.CreatedAt),
	}
	if u.Method == db.UploadMethodMultipart {
		partSize := s.resolveMultipartPartSize(u)
		out.PartSize = partSize
		out.TotalParts = int((u.DeclaredBytes + partSize - 1) / partSize)
		if u.Status == db.UploadPending && u.MultipartID != nil {
			out.UploadedParts = []UploadedPartView{}
			project, err := s.store.GetProject(ctx, u.ProjectID)
			if err != nil {
				return nil, err
			}
			tgt, err := s.storageFor(ctx, project)
			if err != nil {
				return nil, err
			}
			mp := storage.MultipartUpload{Bucket: tgt.profile.PrivateBucket, Key: deref(u.TmpKey), UploadID: *u.MultipartID}
			listed, err := tgt.driver.ListParts(ctx, mp)
			if err != nil && !errors.Is(err, storage.ErrNotFound) {
				return nil, apiErr(http.StatusBadGateway, CodeStorageError, "could not list uploaded parts: %v", err)
			}
			for _, p := range listed {
				out.UploadedParts = append(out.UploadedParts, UploadedPartView{PartNumber: int(p.Number), ETag: p.ETag, Size: p.Size})
			}
		}
	}
	return out, nil
}

// RefreshUploadParts re-signs presigned PUT URLs for a pending MULTIPART
// upload whose session has not expired. When partNumbers is empty, all parts
// not yet reported by storage ListParts are signed.
func (s *Service) RefreshUploadParts(ctx context.Context, projectID, uploadID string, partNumbers []int) (*RefreshPartsView, error) {
	u, err := s.loadUpload(ctx, projectID, uploadID)
	if err != nil {
		return nil, err
	}
	if u.Method != db.UploadMethodMultipart {
		return nil, apiErr(http.StatusConflict, CodeUploadNotMultipart, "upload %s is %s, not MULTIPART", u.ID, u.Method)
	}
	switch u.Status {
	case db.UploadPending:
	case db.UploadExpired:
		return nil, apiErr(http.StatusGone, CodeUploadExpired, "upload %s expired", u.ID)
	case db.UploadAborted:
		return nil, apiErr(http.StatusConflict, CodeUploadAborted, "upload %s was cancelled", u.ID)
	default:
		return nil, apiErr(http.StatusConflict, CodeUploadNotPending, "upload %s is %s", u.ID, u.Status)
	}
	now := s.now()
	remaining := u.ExpiresAt.Sub(now)
	if remaining < time.Second || u.MultipartID == nil {
		return nil, apiErr(http.StatusGone, CodeUploadExpired, "upload %s session has expired", u.ID)
	}

	partSize := s.resolveMultipartPartSize(u)
	totalParts := int((u.DeclaredBytes + partSize - 1) / partSize)
	if len(partNumbers) > 0 {
		seen := map[int]bool{}
		filtered := make([]int, 0, len(partNumbers))
		for _, n := range partNumbers {
			if n < 1 || n > totalParts {
				return nil, validationErr(map[string]string{
					"part_numbers": fmt.Sprintf("part numbers must be between 1 and %d", totalParts),
				})
			}
			if !seen[n] {
				seen[n] = true
				filtered = append(filtered, n)
			}
		}
		slices.Sort(filtered)
		partNumbers = filtered
	}

	project, err := s.store.GetProject(ctx, u.ProjectID)
	if err != nil {
		return nil, err
	}
	tgt, err := s.storageFor(ctx, project)
	if err != nil {
		return nil, err
	}
	mp := storage.MultipartUpload{Bucket: tgt.profile.PrivateBucket, Key: deref(u.TmpKey), UploadID: *u.MultipartID}
	listed, err := tgt.driver.ListParts(ctx, mp)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, apiErr(http.StatusGone, CodeUploadExpired, "multipart upload %s no longer exists in storage", u.ID)
		}
		return nil, apiErr(http.StatusBadGateway, CodeStorageError, "could not list uploaded parts: %v", err)
	}

	uploaded := make([]UploadedPartView, 0, len(listed))
	done := map[int]bool{}
	for _, p := range listed {
		n := int(p.Number)
		done[n] = true
		uploaded = append(uploaded, UploadedPartView{PartNumber: n, ETag: p.ETag, Size: p.Size})
	}

	if len(partNumbers) == 0 {
		partNumbers = make([]int, 0, totalParts)
		for n := 1; n <= totalParts; n++ {
			if !done[n] {
				partNumbers = append(partNumbers, n)
			}
		}
	}

	urlTTL := min(s.mpPartTTL, remaining)
	signed := make([]PartTicket, 0, len(partNumbers))
	for _, n := range partNumbers {
		pr, err := tgt.driver.PresignUploadPart(ctx, mp, int32(n), urlTTL)
		if err != nil {
			return nil, fmt.Errorf("presign part %d: %w", n, err)
		}
		signed = append(signed, PartTicket{PartNumber: n, URL: pr.URL})
	}
	return &RefreshPartsView{
		UploadID:         u.ID,
		PartSize:         partSize,
		Parts:            signed,
		UploadedParts:    uploaded,
		ExpiresAt:        rfc3339(now.Add(urlTTL)),
		SessionExpiresAt: rfc3339(u.ExpiresAt),
	}, nil
}

// finishMultipart assembles the parts of a pending multipart upload. When the
// assembly already happened (a retried complete) it succeeds if the object exists.
func (s *Service) finishMultipart(ctx context.Context, u *db.Upload, parts []CompletedPartInput) error {
	project, err := s.store.GetProject(ctx, u.ProjectID)
	if err != nil {
		return err
	}
	tgt, err := s.storageFor(ctx, project)
	if err != nil {
		return err
	}
	if len(parts) == 0 {
		if _, herr := tgt.driver.Head(ctx, tgt.profile.PrivateBucket, deref(u.TmpKey)); herr == nil {
			return nil // already assembled
		}
		return apiErr(http.StatusUnprocessableEntity, "validation_failed", "parts are required to complete a multipart upload")
	}
	cp := make([]storage.CompletedPart, 0, len(parts))
	for _, p := range parts {
		if p.PartNumber < 1 || p.PartNumber > maxParts || p.ETag == "" {
			return apiErr(http.StatusUnprocessableEntity, "validation_failed", "each part needs part_number (1–%d) and etag", maxParts)
		}
		cp = append(cp, storage.CompletedPart{Number: int32(p.PartNumber), ETag: p.ETag})
	}
	mp := storage.MultipartUpload{Bucket: tgt.profile.PrivateBucket, Key: deref(u.TmpKey), UploadID: deref(u.MultipartID)}
	if err := tgt.driver.CompleteMultipart(ctx, mp, cp); err != nil {
		if _, herr := tgt.driver.Head(ctx, mp.Bucket, mp.Key); herr == nil {
			return nil // retry of a complete that already succeeded
		}
		return apiErr(http.StatusUnprocessableEntity, CodeUploadIncomplete, "could not assemble the uploaded parts: %v", err)
	}
	return nil
}

// abortMultipart discards the parts of a multipart upload (best effort).
func (s *Service) abortMultipart(ctx context.Context, u *db.Upload, tgt *target) {
	if u.MultipartID == nil || tgt == nil {
		return
	}
	mp := storage.MultipartUpload{Bucket: tgt.profile.PrivateBucket, Key: deref(u.TmpKey), UploadID: *u.MultipartID}
	if err := tgt.driver.AbortMultipart(context.WithoutCancel(ctx), mp); err != nil {
		s.log.WarnContext(ctx, "abort multipart upload", "upload_id", u.ID, "err", err)
	}
}

var extKinds = map[string]string{
	"jpg": "image", "jpeg": "image", "png": "image", "webp": "image", "heic": "image", "heif": "image", "avif": "image",
	"gif": "image", "tif": "image", "tiff": "image",
	"pdf": "file", "docx": "file", "xlsx": "file", "pptx": "file", "odt": "file", "ods": "file", "odp": "file",
	"rtf": "file", "txt": "file", "csv": "file",
	"mp4": "video", "mov": "video", "m4v": "video", "webm": "video", "mkv": "video", "avi": "video", "mpg": "video", "mpeg": "video", "3gp": "video",
}

// kindFromHint guesses "image" or "video" from text containing a content type
// and/or a file name or URL; "" when unknown.
func kindFromHint(hint string) string {
	for _, f := range strings.Fields(strings.ToLower(hint)) {
		switch {
		case strings.HasPrefix(f, "image/"):
			return "image"
		case strings.HasPrefix(f, "video/"):
			return "video"
		case file.FormatForContentType(f) != "":
			return "file"
		}
	}
	for _, f := range strings.Fields(strings.ToLower(hint)) {
		f, _, _ = strings.Cut(f, "?")
		if i := strings.LastIndex(f, "."); i >= 0 && i < len(f)-1 {
			if k := extKinds[f[i+1:]]; k != "" {
				return k
			}
		}
	}
	return ""
}
