package admin

import (
	"net/http"
	"runtime"
	"strconv"
	"time"

	"github.com/UdjinStolboviy/kadr-madia/internal/api/httpx"
	"github.com/UdjinStolboviy/kadr-madia/internal/capabilities"
)

type systemDTO struct {
	Version      string              `json:"version"`
	Commit       string              `json:"commit"`
	StartedAt    string              `json:"started_at"`
	UptimeSec    int64               `json:"uptime_sec"`
	GoVersion    string              `json:"go_version"`
	Dev          bool                `json:"dev"`
	DB           systemDB            `json:"db"`
	Tmp          systemTmp           `json:"tmp"`
	Capabilities capabilities.Report `json:"capabilities"`
}

type systemDB struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	WALBytes  int64  `json:"wal_bytes"`
}

type systemTmp struct {
	Path      string `json:"path"`
	FreeBytes uint64 `json:"free_bytes"`
}

func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	d := s.store.DB()
	size, wal := d.SizeInfo()
	free, err := capabilities.DiskFree(s.cfg.TmpDir)
	if err != nil {
		s.log.WarnContext(r.Context(), "tmp free space", "err", err)
	}
	b := s.cfg.Build
	httpx.WriteJSON(w, http.StatusOK, systemDTO{
		Version:      b.Version,
		Commit:       b.Commit,
		StartedAt:    ts(b.StartedAt),
		UptimeSec:    int64(s.now().Sub(b.StartedAt) / time.Second),
		GoVersion:    runtime.Version(),
		Dev:          s.cfg.Dev,
		DB:           systemDB{Path: d.Path, SizeBytes: size, WALBytes: wal},
		Tmp:          systemTmp{Path: s.cfg.TmpDir, FreeBytes: free},
		Capabilities: s.cfg.Caps.Get(),
	})
}

const (
	defaultAuditLimit = 50
	maxAuditLimit     = 200
)

type auditPage struct {
	Items      []auditDTO `json:"items"`
	NextCursor *string    `json:"next_cursor"`
}

// handleListAudit pages through audit_log newest first. The cursor is the
// id of the last item of the previous page.
func (s *Server) handleListAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := defaultAuditLimit
	errs := map[string]string{}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxAuditLimit {
			errs["limit"] = "must be between 1 and 200"
		} else {
			limit = n
		}
	}
	var before int64
	if v := q.Get("cursor"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			errs["cursor"] = "invalid cursor"
		} else {
			before = n
		}
	}
	if len(errs) > 0 {
		validationFailed(w, errs)
		return
	}
	entries, err := s.store.ListAudit(r.Context(), limit, before)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	page := auditPage{Items: make([]auditDTO, 0, len(entries))}
	for _, e := range entries {
		page.Items = append(page.Items, toAuditDTO(e))
	}
	if len(entries) == limit {
		c := strconv.FormatInt(entries[len(entries)-1].ID, 10)
		page.NextCursor = &c
	}
	httpx.WriteJSON(w, http.StatusOK, page)
}
