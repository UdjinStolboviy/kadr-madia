package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/UdjinStolboviy/kadr-madia/internal/db"
	"github.com/UdjinStolboviy/kadr-madia/internal/importer"
	"github.com/UdjinStolboviy/kadr-madia/internal/queue"
)

// Failure codes of imports.
const (
	FailImportBlocked = "import_blocked"
	FailImportFailed  = "import_failed"
)

// ImportURLInput describes POST /v1/imports/url.
type ImportURLInput struct {
	URL        string          `json:"url"`
	Preset     string          `json:"preset"`
	Mode       string          `json:"mode"`
	Namespace  string          `json:"namespace"`
	Visibility string          `json:"visibility"`
	Filename   string          `json:"filename"`
	Metadata   json.RawMessage `json:"metadata"`
}

// ImportCloudInput describes POST /v1/imports/cloud.
type ImportCloudInput struct {
	ConnectionID string          `json:"connection_id"`
	FileID       string          `json:"file_id"`
	Preset       string          `json:"preset"`
	Mode         string          `json:"mode"`
	Namespace    string          `json:"namespace"`
	Visibility   string          `json:"visibility"`
	Filename     string          `json:"filename"`
	Metadata     json.RawMessage `json:"metadata"`
}

// ImportURL queues an SSRF-safe download and processing; the asset is returned
// in the processing state.
func (s *Service) ImportURL(ctx context.Context, projectID string, in ImportURLInput) (*AssetView, error) {
	errs := map[string]string{}
	c, err := s.validateCommon(ctx, projectID, in.Preset, in.Mode, in.URL+" "+in.Filename, in.Namespace, in.Visibility, in.Filename, in.Metadata, errs)
	if err != nil {
		return nil, err
	}
	if verr := s.fetcher.Validate(in.URL); verr != nil {
		errs["url"] = "must be an absolute http(s) URL without credentials, on a standard web port"
	}
	if len(errs) > 0 {
		return nil, validationErr(errs)
	}
	return s.queueImport(ctx, projectID, c, db.UploadMethodURL, strings.TrimSpace(in.URL))
}

// ImportCloud queues a download of one file through rclone.
func (s *Service) ImportCloud(ctx context.Context, projectID string, in ImportCloudInput) (*AssetView, error) {
	errs := map[string]string{}
	c, err := s.validateCommon(ctx, projectID, in.Preset, in.Mode, in.FileID+" "+in.Filename, in.Namespace, in.Visibility, in.Filename, in.Metadata, errs)
	if err != nil {
		return nil, err
	}
	if in.FileID == "" || len(in.FileID) > 1024 || strings.HasPrefix(in.FileID, "-") {
		errs["file_id"] = "required (Drive file id, or a path for OneDrive/Dropbox)"
	}
	if len(errs) > 0 {
		return nil, validationErr(errs)
	}
	con, err := s.store.GetCloudConnection(ctx, in.ConnectionID)
	if errors.Is(err, db.ErrNotFound) || (err == nil && (con.ProjectID != projectID || con.RevokedAt != nil)) {
		return nil, apiErr(http.StatusNotFound, CodeNotFound, "connection %s not found", in.ConnectionID)
	}
	if err != nil {
		return nil, err
	}
	if con.NS != nil && *con.NS != c.ns {
		return nil, apiErr(http.StatusForbidden, "forbidden", "connection %s is restricted to namespace %s", con.ID, *con.NS)
	}
	return s.queueImport(ctx, projectID, c, db.UploadMethodCloud, con.ID+":"+in.FileID)
}

func (s *Service) queueImport(ctx context.Context, projectID string, c *common, method, ref string) (*AssetView, error) {
	reserve := c.maxInputBytes()
	if reserve <= 0 {
		reserve = MaxServerUploadBytes
	}
	now := s.now()
	u := &db.Upload{ID: db.NewID(db.PrefixUpload), ProjectID: projectID, NS: c.ns, PresetID: c.preset.ID, PresetVersion: c.preset.CurrentVersion,
		Visibility: c.vis, Method: method, Filename: c.filename, DeclaredBytes: reserve, SourceRef: &ref, Metadata: c.metadata,
		Status: db.UploadUploaded, ExpiresAt: now.Add(s.uploadTTL), CreatedAt: now, UpdatedAt: now}
	a := &db.Asset{ID: db.NewID(db.PrefixAsset), ProjectID: projectID, NS: c.ns, UploadID: &u.ID, Kind: c.kind,
		Visibility: c.vis, Filename: c.filename, Metadata: c.metadata, Status: db.AssetProcessing,
		PresetID: &c.preset.ID, PresetVersion: ptr(int64(c.preset.CurrentVersion)), CreatedAt: now}
	u.AssetID = &a.ID
	var view *AssetView
	err := s.store.InTx(ctx, func(tx *db.Store) error {
		if err := s.reserve(ctx, tx, projectID, c.ns, reserve); err != nil {
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
		if _, _, err := s.queue.Enqueue(ctx, tx, queue.EnqueueOptions{
			Kind: jobKindFor(c.kind), Pool: poolFor(c.kind), DedupeKey: u.ID, Payload: processPayload{UploadID: u.ID},
		}); err != nil {
			return err
		}
		var verr error
		view, verr = s.assetView(ctx, tx, a)
		return verr
	})
	if err != nil {
		return nil, err
	}
	return view, nil
}

// importLimit is the byte limit of the source of pc.
func (pc *processCtx) importLimit() int64 {
	if pc.kind == "video" {
		return pc.vparams.Limits.MaxInputBytes
	}
	return pc.params.Limits.MaxInputBytes
}

func (s *Service) fetchURL(ctx context.Context, pc *processCtx) (string, int64, []byte, error) {
	path := filepath.Join(pc.workDir, "source")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", 0, nil, err
	}
	h := sha256.New()
	limit := pc.importLimit()
	n, _, _, gerr := s.fetcher.Get(ctx, deref(pc.upload.SourceRef), limit, io.MultiWriter(f, h))
	if cerr := f.Close(); gerr == nil {
		gerr = cerr
	}
	if gerr != nil {
		return "", 0, nil, classifyImportError(gerr, limit)
	}
	if n == 0 {
		return "", 0, nil, permanent(FailImportFailed, "the remote file is empty", nil)
	}
	return path, n, h.Sum(nil), nil
}

func (s *Service) fetchCloud(ctx context.Context, pc *processCtx) (string, int64, []byte, error) {
	conID, fileID, _ := strings.Cut(deref(pc.upload.SourceRef), ":")
	con, err := s.store.GetCloudConnection(ctx, conID)
	if err != nil || con.RevokedAt != nil {
		return "", 0, nil, permanent(FailImportFailed, "the cloud connection no longer exists", err)
	}
	token, err := s.keyring.Decrypt(con.TokenEnc, CloudTokenAAD(con.ID))
	if err != nil {
		return "", 0, nil, permanent(FailInternal, "cannot decrypt the cloud connection", err)
	}
	path := filepath.Join(pc.workDir, "source")
	limit := pc.importLimit()
	if _, err := s.rclone.Fetch(ctx, con.Provider, string(token), fileID, path, limit); err != nil {
		if ctx.Err() != nil {
			return "", 0, nil, err
		}
		return "", 0, nil, &jobError{code: FailImportFailed, detail: "could not download the file from the cloud drive", err: err}
	}
	f, err := os.Open(path)
	if err != nil {
		return "", 0, nil, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, nil, err
	}
	if limit > 0 && n > limit {
		return "", 0, nil, permanent("too_large", fmt.Sprintf("the file is larger than the %d byte limit", limit), nil)
	}
	return path, n, h.Sum(nil), nil
}

// CloudTokenAAD is the associated data of an encrypted cloud token.
func CloudTokenAAD(id string) []byte { return []byte("cloud_connection:" + id) }

func classifyImportError(err error, limit int64) error {
	var se *importer.StatusError
	switch {
	case errors.Is(err, importer.ErrBlockedAddress), errors.Is(err, importer.ErrBadURL):
		return permanent(FailImportBlocked, "the URL is not allowed", err)
	case errors.Is(err, importer.ErrTooLarge):
		return permanent("too_large", fmt.Sprintf("the remote file is larger than the %d byte limit", limit), err)
	case errors.Is(err, importer.ErrTooManyRedirs):
		return permanent(FailImportFailed, "too many redirects", err)
	case errors.As(err, &se) && se.Permanent():
		return permanent(FailImportFailed, fmt.Sprintf("the remote server answered HTTP %d", se.Code), err)
	case errors.Is(err, context.Canceled):
		return err
	}
	return &jobError{code: FailImportFailed, detail: "download failed", err: err}
}
