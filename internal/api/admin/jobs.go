package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/UdjinStolboviy/kadr-madia/internal/api/httpx"
	"github.com/UdjinStolboviy/kadr-madia/internal/db"
)

type jobDTO struct {
	ID          string  `json:"id"`
	Kind        string  `json:"kind"`
	Pool        string  `json:"pool"`
	Status      string  `json:"status"`
	Attempts    int     `json:"attempts"`
	MaxAttempts int     `json:"max_attempts"`
	Progress    *int64  `json:"progress"`
	LastError   *string `json:"last_error"`
	RunAt       string  `json:"run_at"`
	CreatedAt   string  `json:"created_at"`
	FinishedAt  *string `json:"finished_at"`
}

func toJobDTO(j *db.Job) jobDTO {
	return jobDTO{ID: j.ID, Kind: j.Kind, Pool: j.Pool, Status: j.Status, Attempts: j.Attempts, MaxAttempts: j.MaxAttempts,
		Progress: j.Progress, LastError: j.LastError, RunAt: ts(j.RunAt), CreatedAt: ts(j.CreatedAt), FinishedAt: tsp(j.FinishedAt)}
}

func (s *Server) listJobDTOs(r *http.Request) ([]jobDTO, error) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	js, err := s.store.ListJobs(r.Context(), db.JobFilter{Status: q.Get("status"), Pool: q.Get("pool"), Kind: q.Get("kind"), Limit: limit})
	if err != nil {
		return nil, err
	}
	out := make([]jobDTO, 0, len(js))
	for _, j := range js {
		out = append(out, toJobDTO(j))
	}
	return out, nil
}

// handleListJobs: GET /admin/api/jobs?status=&pool=&kind=&limit=
func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	out, err := s.listJobDTOs(r)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

// handleJobsStream: GET /admin/api/jobs/stream — Server-Sent Events. Sends a
// "jobs" event with the current list (same filters as the list endpoint)
// whenever it changes, and a comment heartbeat every 15 seconds.
func (s *Server) handleJobsStream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		httpx.WriteProblem(w, http.StatusInternalServerError, "internal", "streaming is not supported")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	var last string
	var lastBeat = time.Now()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		out, err := s.listJobDTOs(r)
		if err != nil {
			fmt.Fprintf(w, "event: error\ndata: %q\n\n", "internal error")
			fl.Flush()
			return
		}
		b, _ := json.Marshal(map[string]any{"items": out})
		switch {
		case string(b) != last:
			last = string(b)
			fmt.Fprintf(w, "event: jobs\ndata: %s\n\n", b)
			fl.Flush()
			lastBeat = time.Now()
		case time.Since(lastBeat) > 15*time.Second:
			fmt.Fprint(w, ": keepalive\n\n")
			fl.Flush()
			lastBeat = time.Now()
		}
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
		}
	}
}

func (s *Server) handleRetryJob(w http.ResponseWriter, r *http.Request) {
	s.jobAction(w, r, "job.retry", func(r *http.Request, tx *db.Store, id string) (bool, error) {
		return tx.RequeueJob(r.Context(), id, s.now())
	}, "only dead or cancelled jobs can be retried")
}

func (s *Server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	s.jobAction(w, r, "job.cancel", func(r *http.Request, tx *db.Store, id string) (bool, error) {
		return tx.CancelJob(r.Context(), id, s.now())
	}, "the job already finished")
}

func (s *Server) jobAction(w http.ResponseWriter, r *http.Request, action string, do func(*http.Request, *db.Store, string) (bool, error), conflict string) {
	id := chi.URLParam(r, "id")
	j, err := s.store.GetJob(r.Context(), id)
	if errors.Is(err, db.ErrNotFound) {
		notFound(w, "job")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var changed bool
	err = s.store.InTx(r.Context(), func(tx *db.Store) error {
		var err error
		if changed, err = do(r, tx, id); err != nil || !changed {
			return err
		}
		return s.audit(r, tx, action, nil, &id, map[string]any{"kind": j.Kind, "previous_status": j.Status})
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !changed {
		httpx.WriteProblem(w, http.StatusConflict, "invalid_state", conflict)
		return
	}
	if s.cfg.Wake != nil {
		s.cfg.Wake(j.Pool)
	}
	j, err = s.store.GetJob(r.Context(), id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toJobDTO(j))
}
