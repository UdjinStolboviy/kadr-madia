package pipeline

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/UdjinStolboviy/kadr-madia/internal/db"
	"github.com/UdjinStolboviy/kadr-madia/internal/engine/video"
	"github.com/UdjinStolboviy/kadr-madia/internal/storage"
)

// HLS rendition identity: one row per blob, pointing at the master playlist.
// All other files of the package live next to it under {project}/{blob}/hls/
// and are found through the storage listing (gc, visibility change), so the
// DB does not need a row per segment.
const (
	hlsRenditionName   = "hls"
	hlsRenditionFormat = "m3u8"
	hlsSubdir          = "hls"
)

// hlsPrefix returns the key prefix ("…/hls/") of the package whose master
// playlist is at masterKey.
func hlsPrefix(masterKey string) string { return path.Dir(masterKey) + "/" }

// hlsContentType maps an HLS package file to its Content-Type.
func hlsContentType(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".m3u8":
		return "application/vnd.apple.mpegurl"
	case ".m4s":
		return "video/iso.segment"
	case ".mp4":
		return "video/mp4"
	}
	return "application/octet-stream"
}

// packageHLS runs the optional HLS engine for presets with hls=true.
func (s *Service) packageHLS(ctx context.Context, pc *processCtx, info video.SourceInfo, outDir string, report func(pct int)) (*video.HLSOutput, error) {
	he, ok := s.video.(video.HLSEngine)
	if !ok {
		return nil, &jobError{code: video.CodeEngineFailure, detail: "the video engine does not support HLS"}
	}
	ho, err := he.PackageHLS(ctx, info, pc.vparams, outDir, report)
	if err != nil {
		return nil, classifyEngineError(err)
	}
	return ho, nil
}

// uploadHLS uploads every file of the package under
// {blob prefix}/hls/ (see keys.go) and returns the single rendition row (master
// playlist; bytes = size of the whole package) plus all uploaded keys (for
// cleanup on failure).
func (s *Service) uploadHLS(ctx context.Context, pc *processCtx, blob *db.Blob, bucket, cacheControl string, h *video.HLSOutput) (*db.Rendition, []string, error) {
	prefix := blobPrefix(pc.project.Slug, blob) + hlsSubdir + "/"
	var keys []string
	for _, f := range h.Files {
		key := prefix + f
		if err := putFile(ctx, pc.tgt.driver, bucket, key, filepath.Join(h.Dir, filepath.FromSlash(f)), storage.PutOptions{
			ContentType: hlsContentType(f), CacheControl: cacheControl,
		}); err != nil {
			return nil, keys, fmt.Errorf("upload hls file %s: %w", f, err)
		}
		keys = append(keys, key)
	}
	rd := &db.Rendition{BlobID: blob.ID, Name: hlsRenditionName, Format: hlsRenditionFormat, StorageKey: prefix + h.Master, Bytes: h.Bytes}
	if n := len(h.Variants); n > 0 {
		top := h.Variants[n-1]
		rd.Width, rd.Height = ptr(int64(top.Width)), ptr(int64(top.Height))
	}
	return rd, keys, nil
}

// copyHLSPackage copies every object under the package prefix of masterKey
// to the same relative place in the new blob, returning the new keys.
func (s *Service) copyHLSPackage(ctx context.Context, d storage.Driver, srcBucket, dstBucket, masterKey, oldBlob, newBlob, cache string) ([]string, error) {
	prefix := hlsPrefix(masterKey)
	objs, err := d.List(ctx, srcBucket, prefix)
	if err != nil {
		return nil, err
	}
	var copied []string
	for _, o := range objs {
		dst := strings.Replace(o.Key, oldBlob, newBlob, 1)
		if err := d.Copy(ctx, srcBucket, o.Key, dstBucket, dst,
			storage.PutOptions{ContentType: hlsContentType(o.Key), CacheControl: cache}); err != nil {
			return copied, fmt.Errorf("copy %s: %w", o.Key, err)
		}
		copied = append(copied, dst)
	}
	return copied, nil
}

// deleteHLSPackage removes every object under the package prefix.
func deleteHLSPackage(ctx context.Context, d storage.Driver, bucket, masterKey string) error {
	objs, err := d.List(ctx, bucket, hlsPrefix(masterKey))
	if err != nil {
		return err
	}
	for _, o := range objs {
		if err := d.Delete(ctx, bucket, o.Key); err != nil {
			return err
		}
	}
	return nil
}
