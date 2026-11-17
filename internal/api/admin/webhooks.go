package admin

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/UdjinStolboviy/kadr-madia/internal/api/httpx"
	"github.com/UdjinStolboviy/kadr-madia/internal/db"
	"github.com/UdjinStolboviy/kadr-madia/internal/webhook"
)

const (
	maxWebhookURL       = 2048
	maxWebhookFilters   = 20
	maxEndpointsPerProj = 20
)

type webhookEndpointDTO struct {
	ID        string   `json:"id"`
	ProjectID string   `json:"project_id"`
	URL       string   `json:"url"`
	Events    []string `json:"events"`
	Disabled  bool     `json:"disabled"`
	CreatedAt string   `json:"created_at"`
}

func toWebhookEndpointDTO(e *db.WebhookEndpoint) webhookEndpointDTO {
	events := e.Events
	if events == nil {
		events = []string{}
	}
	return webhookEndpointDTO{ID: e.ID, ProjectID: e.ProjectID, URL: e.URL, Events: events, Disabled: e.DisabledAt != nil,
		CreatedAt: ts(e.CreatedAt)}
}

type deliveryDTO struct {
	ID             string  `json:"id"`
	EventID        string  `json:"event_id"`
	EventType      string  `json:"event_type"`
	EndpointID     string  `json:"endpoint_id"`
	EndpointURL    string  `json:"endpoint_url"`
	Status         string  `json:"status"`
	Attempts       int     `json:"attempts"`
	NextAttemptAt  *string `json:"next_attempt_at"`
	LastStatusCode *int64  `json:"last_status_code"`
	LastError      *string `json:"last_error"`
	LastDurationMs *int64  `json:"last_duration_ms"`
	DeliveredAt    *string `json:"delivered_at"`
	CreatedAt      string  `json:"created_at"`
}

func toDeliveryDTO(d *db.Delivery) deliveryDTO {
	out := deliveryDTO{ID: d.ID, EventID: d.EventID, EventType: d.EventType, EndpointID: d.EndpointID, EndpointURL: d.EndpointURL,
		Status: d.Status, Attempts: d.Attempts, LastStatusCode: d.LastStatusCode, LastError: d.LastError,
		LastDurationMs: d.LastDurationMs, DeliveredAt: tsp(d.DeliveredAt), CreatedAt: ts(d.EventCreatedAt)}
	if d.Status == db.DeliveryPending {
		out.NextAttemptAt = tsp(&d.NextAttemptAt)
	}
	return out
}

func (s *Server) handleListWebhooks(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	es, err := s.store.ListWebhookEndpoints(r.Context(), p.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]webhookEndpointDTO, 0, len(es))
	for _, e := range es {
		out = append(out, toWebhookEndpointDTO(e))
	}
	httpx.WriteJSON(w, http.StatusOK, itemsResponse[webhookEndpointDTO]{Items: out})
}

type createWebhookRequest struct {
	URL    string   `json:"url"`
	Events []string `json:"events"`
}

func (s *Server) handleCreateWebhook(w http.ResponseWriter, r *http.Request) {
	var req createWebhookRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		badJSON(w, err)
		return
	}
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	errs := map[string]string{}
	req.URL = strings.TrimSpace(req.URL)
	if u, err := url.Parse(req.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		u.User != nil || u.Fragment != "" || len(req.URL) > maxWebhookURL {
		errs["url"] = "must be an absolute http(s) URL without credentials"
	}
	events := []string{}
	seen := map[string]bool{}
	if len(req.Events) == 0 {
		events = []string{"*"}
	}
	if len(req.Events) > maxWebhookFilters {
		errs["events"] = "too many event filters"
	}
	for _, e := range req.Events {
		e = strings.TrimSpace(e)
		if !webhook.ValidFilter(e) {
			errs["events"] = "unknown event type " + strconv.Quote(e)
			break
		}
		if !seen[e] {
			seen[e] = true
			events = append(events, e)
		}
	}
	if len(errs) > 0 {
		validationFailed(w, errs)
		return
	}
	existing, err := s.store.ListWebhookEndpoints(r.Context(), p.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if len(existing) >= maxEndpointsPerProj {
		httpx.WriteProblem(w, http.StatusConflict, "limit_reached", "a project can have at most 20 webhook endpoints")
		return
	}
	secret, err := webhook.GenerateSecret()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	e := &db.WebhookEndpoint{ID: db.NewID(db.PrefixWebhookEndpoint), ProjectID: p.ID, URL: req.URL, Events: events, CreatedAt: s.now()}
	if e.SecretEnc, err = webhook.EncryptSecret(s.cfg.Keyring, e.ID, secret); err != nil {
		s.internalError(w, r, err)
		return
	}
	err = s.store.InTx(r.Context(), func(tx *db.Store) error {
		if err := tx.CreateWebhookEndpoint(r.Context(), e); err != nil {
			return err
		}
		return s.audit(r, tx, "webhook.create", &p.ID, &e.ID, map[string]any{"url": e.URL, "events": e.Events})
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"webhook": toWebhookEndpointDTO(e), "secret": secret})
}

func (s *Server) loadWebhook(w http.ResponseWriter, r *http.Request) (*db.WebhookEndpoint, bool) {
	e, err := s.store.GetWebhookEndpoint(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, db.ErrNotFound) {
		notFound(w, "webhook endpoint")
		return nil, false
	}
	if err != nil {
		s.internalError(w, r, err)
		return nil, false
	}
	return e, true
}

func (s *Server) handleDeleteWebhook(w http.ResponseWriter, r *http.Request) {
	e, ok := s.loadWebhook(w, r)
	if !ok {
		return
	}
	err := s.store.InTx(r.Context(), func(tx *db.Store) error {
		if err := tx.DeleteWebhookEndpoint(r.Context(), e.ID); err != nil {
			return err
		}
		return s.audit(r, tx, "webhook.delete", &e.ProjectID, &e.ID, map[string]any{"url": e.URL})
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleTestWebhook records a "webhook.test" event with a single delivery to
// this endpoint and attempts it synchronously. The result is in the
// returned delivery (failed attempts are retried on the normal schedule).
func (s *Server) handleTestWebhook(w http.ResponseWriter, r *http.Request) {
	e, ok := s.loadWebhook(w, r)
	if !ok {
		return
	}
	if s.cfg.Webhooks == nil {
		httpx.WriteProblem(w, http.StatusServiceUnavailable, "unavailable", "webhook delivery is not running")
		return
	}
	now := s.now()
	ev := &db.Event{ID: db.NewID(db.PrefixEvent), ProjectID: e.ProjectID, Type: webhook.EventTest, CreatedAt: now}
	del := &db.Delivery{ID: db.NewID(db.PrefixDelivery), EventID: ev.ID, EndpointID: e.ID, NextAttemptAt: now}
	body, err := json.Marshal(webhook.Envelope{Type: webhook.EventTest, Timestamp: ts(now),
		Data: map[string]any{"endpoint_id": e.ID, "message": "This is a test event from Kadr."}})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	ev.Payload = body
	err = s.store.InTx(r.Context(), func(tx *db.Store) error {
		if err := tx.InsertEvent(r.Context(), ev); err != nil {
			return err
		}
		if err := tx.InsertDelivery(r.Context(), del); err != nil {
			return err
		}
		return s.audit(r, tx, "webhook.test", &e.ProjectID, &e.ID, nil)
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	got, err := s.cfg.Webhooks.DeliverNow(r.Context(), del.ID)
	if errors.Is(err, db.ErrNotFound) {
		// A background worker claimed it first; report the current state.
		got, err = s.store.GetDelivery(r.Context(), del.ID)
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toDeliveryDTO(got))
}

var deliveryStatuses = map[string]bool{db.DeliveryPending: true, db.DeliveryDelivering: true, db.DeliveryDelivered: true, db.DeliveryDead: true}

func (s *Server) handleListDeliveries(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadProject(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := db.DeliveryFilter{ProjectID: p.ID, Status: q.Get("status"), Limit: 50}
	errs := map[string]string{}
	if f.Status != "" && !deliveryStatuses[f.Status] {
		errs["status"] = "must be pending, delivering, delivered or dead"
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			errs["limit"] = "must be between 1 and 200"
		}
		f.Limit = n
	}
	if c := q.Get("cursor"); c != "" {
		created, id, ok := decodeDeliveryCursor(c)
		if !ok {
			errs["cursor"] = "invalid cursor"
		}
		f.CursorCreated, f.CursorID = created, id
	}
	if len(errs) > 0 {
		validationFailed(w, errs)
		return
	}
	limit := f.Limit
	f.Limit++
	ds, err := s.store.ListDeliveries(r.Context(), f)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var next *string
	if len(ds) > limit {
		last := ds[limit-1]
		c := base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(last.EventCreatedAt.UnixMilli(), 10) + ":" + last.ID))
		next = &c
		ds = ds[:limit]
	}
	out := make([]deliveryDTO, 0, len(ds))
	for _, d := range ds {
		out = append(out, toDeliveryDTO(d))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": out, "next_cursor": next})
}

func decodeDeliveryCursor(c string) (int64, string, bool) {
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return 0, "", false
	}
	tsPart, id, ok := strings.Cut(string(b), ":")
	n, err := strconv.ParseInt(tsPart, 10, 64)
	return n, id, ok && err == nil && id != ""
}

// handleRetryDelivery re-sends a delivery (typically a dead one) with a fresh
// attempt budget. The attempt runs in the background.
func (s *Server) handleRetryDelivery(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	d, err := s.store.GetDelivery(r.Context(), id)
	if errors.Is(err, db.ErrNotFound) {
		notFound(w, "delivery")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if d.Status == db.DeliveryDelivering {
		httpx.WriteProblem(w, http.StatusConflict, "in_progress", "the delivery is being attempted right now")
		return
	}
	ep, err := s.store.GetWebhookEndpoint(r.Context(), d.EndpointID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	err = s.store.InTx(r.Context(), func(tx *db.Store) error {
		if _, err := tx.RetryDelivery(r.Context(), d.ID, s.now()); err != nil {
			return err
		}
		return s.audit(r, tx, "delivery.retry", &ep.ProjectID, &d.ID, map[string]any{"event_id": d.EventID, "previous_status": d.Status})
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if s.cfg.Webhooks != nil {
		s.cfg.Webhooks.Notify()
	}
	d, err = s.store.GetDelivery(r.Context(), id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toDeliveryDTO(d))
}
