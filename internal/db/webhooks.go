package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// ID prefixes of the webhook outbox.
const (
	PrefixWebhookEndpoint = "whe"
	PrefixEvent           = "evt"
	PrefixDelivery        = "whd"
)

// Delivery statuses (webhook_deliveries.status).
const (
	DeliveryPending    = "pending"
	DeliveryDelivering = "delivering"
	DeliveryDelivered  = "delivered"
	DeliveryDead       = "dead"
)

// WebhookEndpoint is a receiver of project events.
type WebhookEndpoint struct {
	ID         string
	ProjectID  string
	URL        string
	SecretEnc  []byte
	Events     []string // ["*"] = all
	DisabledAt *time.Time
	CreatedAt  time.Time
}

const webhookEndpointCols = `id, project_id, url, secret_enc, events, disabled_at, created_at`

func scanWebhookEndpoint(row interface{ Scan(...any) error }) (*WebhookEndpoint, error) {
	var e WebhookEndpoint
	var events string
	var disabled sql.NullInt64
	var created int64
	if err := row.Scan(&e.ID, &e.ProjectID, &e.URL, &e.SecretEnc, &events, &disabled, &created); err != nil {
		return nil, mapErr(err)
	}
	if err := json.Unmarshal([]byte(events), &e.Events); err != nil {
		return nil, fmt.Errorf("webhook endpoint %s: events: %w", e.ID, err)
	}
	e.DisabledAt = fromNullMs(disabled)
	e.CreatedAt = fromMs(created)
	return &e, nil
}

// CreateWebhookEndpoint inserts an endpoint.
func (s *Store) CreateWebhookEndpoint(ctx context.Context, e *WebhookEndpoint) error {
	events, err := json.Marshal(e.Events)
	if err != nil {
		return err
	}
	_, err = s.w.ExecContext(ctx,
		`INSERT INTO webhook_endpoints (id, project_id, url, secret_enc, events, disabled_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.ProjectID, e.URL, e.SecretEnc, string(events), nullMs(e.DisabledAt), ms(e.CreatedAt))
	if err != nil {
		return fmt.Errorf("create webhook endpoint: %w", mapErr(err))
	}
	return nil
}

// GetWebhookEndpoint returns an endpoint by id.
func (s *Store) GetWebhookEndpoint(ctx context.Context, id string) (*WebhookEndpoint, error) {
	return scanWebhookEndpoint(s.r.QueryRowContext(ctx, `SELECT `+webhookEndpointCols+` FROM webhook_endpoints WHERE id = ?`, id))
}

// ListWebhookEndpoints returns the endpoints of a project, oldest first.
func (s *Store) ListWebhookEndpoints(ctx context.Context, projectID string) ([]*WebhookEndpoint, error) {
	rows, err := s.r.QueryContext(ctx,
		`SELECT `+webhookEndpointCols+` FROM webhook_endpoints WHERE project_id = ? ORDER BY created_at, id`, projectID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []*WebhookEndpoint{}
	for rows.Next() {
		e, err := scanWebhookEndpoint(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, mapErr(rows.Err())
}

// DeleteWebhookEndpoint removes an endpoint and (by cascade) its deliveries.
func (s *Store) DeleteWebhookEndpoint(ctx context.Context, id string) error {
	res, err := s.w.ExecContext(ctx, `DELETE FROM webhook_endpoints WHERE id = ?`, id)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Event is one outbox event.
type Event struct {
	ID        string
	ProjectID string
	Type      string
	Payload   json.RawMessage // the full webhook body
	CreatedAt time.Time
}

// InsertEvent writes an event.
func (s *Store) InsertEvent(ctx context.Context, e *Event) error {
	_, err := s.w.ExecContext(ctx, `INSERT INTO events (id, project_id, type, payload_json, created_at) VALUES (?, ?, ?, ?, ?)`,
		e.ID, e.ProjectID, e.Type, string(e.Payload), ms(e.CreatedAt))
	if err != nil {
		return fmt.Errorf("insert event: %w", mapErr(err))
	}
	return nil
}

// GetEvent returns an event by id.
func (s *Store) GetEvent(ctx context.Context, id string) (*Event, error) {
	var e Event
	var payload string
	var created int64
	err := s.r.QueryRowContext(ctx, `SELECT id, project_id, type, payload_json, created_at FROM events WHERE id = ?`, id).
		Scan(&e.ID, &e.ProjectID, &e.Type, &payload, &created)
	if err != nil {
		return nil, mapErr(err)
	}
	e.Payload = json.RawMessage(payload)
	e.CreatedAt = fromMs(created)
	return &e, nil
}

// ListEventsByType returns events of a project with the given type, newest first.
func (s *Store) ListEventsByType(ctx context.Context, projectID, typ string, limit int) ([]*Event, error) {
	rows, err := s.r.QueryContext(ctx,
		`SELECT id, project_id, type, payload_json, created_at FROM events WHERE project_id = ? AND type = ?
		 ORDER BY created_at DESC, id DESC LIMIT ?`, projectID, typ, limit)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []*Event{}
	for rows.Next() {
		var e Event
		var payload string
		var created int64
		if err := rows.Scan(&e.ID, &e.ProjectID, &e.Type, &payload, &created); err != nil {
			return nil, mapErr(err)
		}
		e.Payload = json.RawMessage(payload)
		e.CreatedAt = fromMs(created)
		out = append(out, &e)
	}
	return out, mapErr(rows.Err())
}

// Delivery is one (event, endpoint) delivery with its retry state.
type Delivery struct {
	ID             string
	EventID        string
	EndpointID     string
	Status         string
	Attempts       int
	NextAttemptAt  time.Time
	LeaseUntil     *time.Time
	LastStatusCode *int64
	LastError      *string
	LastDurationMs *int64
	DeliveredAt    *time.Time

	// Joined columns (filled by List/Get).
	EventType      string
	EventCreatedAt time.Time
	EndpointURL    string
}

const deliveryCols = `d.id, d.event_id, d.endpoint_id, d.status, d.attempts, d.next_attempt_at, d.lease_until,
	d.last_status_code, d.last_error, d.last_duration_ms, d.delivered_at, e.type, e.created_at, w.url`

const deliveryFrom = ` FROM webhook_deliveries d JOIN events e ON e.id = d.event_id JOIN webhook_endpoints w ON w.id = d.endpoint_id`

func scanDelivery(row interface{ Scan(...any) error }) (*Delivery, error) {
	var d Delivery
	var next, evCreated int64
	var lease, code, dur, delivered sql.NullInt64
	var lastErr sql.NullString
	if err := row.Scan(&d.ID, &d.EventID, &d.EndpointID, &d.Status, &d.Attempts, &next, &lease, &code, &lastErr, &dur,
		&delivered, &d.EventType, &evCreated, &d.EndpointURL); err != nil {
		return nil, mapErr(err)
	}
	d.NextAttemptAt, d.EventCreatedAt = fromMs(next), fromMs(evCreated)
	d.LeaseUntil, d.DeliveredAt = fromNullMs(lease), fromNullMs(delivered)
	d.LastStatusCode, d.LastDurationMs = fromNullInt(code), fromNullInt(dur)
	d.LastError = fromNullStr(lastErr)
	return &d, nil
}

// InsertDelivery schedules an event for an endpoint.
func (s *Store) InsertDelivery(ctx context.Context, d *Delivery) error {
	_, err := s.w.ExecContext(ctx,
		`INSERT INTO webhook_deliveries (id, event_id, endpoint_id, status, attempts, next_attempt_at) VALUES (?, ?, ?, 'pending', 0, ?)`,
		d.ID, d.EventID, d.EndpointID, ms(d.NextAttemptAt))
	if err != nil {
		return fmt.Errorf("insert delivery: %w", mapErr(err))
	}
	return nil
}

// GetDelivery returns a delivery by id.
func (s *Store) GetDelivery(ctx context.Context, id string) (*Delivery, error) {
	return scanDelivery(s.r.QueryRowContext(ctx, `SELECT `+deliveryCols+deliveryFrom+` WHERE d.id = ?`, id))
}

// ClaimDelivery leases the next due delivery (pending and due, or delivering
// with an expired lease) and counts the attempt. Returns ErrNotFound when
// nothing is due. If id is non-empty only that delivery is claimed (and only
// when it is pending, regardless of next_attempt_at).
func (s *Store) ClaimDelivery(ctx context.Context, id string, now, leaseUntil time.Time) (*Delivery, error) {
	var claimed string
	var err error
	if id != "" {
		err = s.w.QueryRowContext(ctx,
			`UPDATE webhook_deliveries SET status = 'delivering', attempts = attempts + 1, lease_until = ?
			 WHERE id = ? AND status = 'pending' RETURNING id`, ms(leaseUntil), id).Scan(&claimed)
	} else {
		err = s.w.QueryRowContext(ctx,
			`UPDATE webhook_deliveries SET status = 'delivering', attempts = attempts + 1, lease_until = ?
			 WHERE id = (SELECT id FROM webhook_deliveries
			             WHERE (status = 'pending' AND next_attempt_at <= ?) OR (status = 'delivering' AND lease_until < ?)
			             ORDER BY next_attempt_at, id LIMIT 1)
			 RETURNING id`, ms(leaseUntil), ms(now), ms(now)).Scan(&claimed)
	}
	if err != nil {
		return nil, mapErr(err)
	}
	return scanDelivery(s.w.QueryRowContext(ctx, `SELECT `+deliveryCols+deliveryFrom+` WHERE d.id = ?`, claimed))
}

// DeliveryResult is the outcome of one attempt.
type DeliveryResult struct {
	Status        string // delivered | pending (retry) | dead
	StatusCode    *int64
	Error         *string
	DurationMs    int64
	NextAttemptAt time.Time
	At            time.Time
}

// FinishDeliveryAttempt records an attempt of a delivery in 'delivering'.
func (s *Store) FinishDeliveryAttempt(ctx context.Context, id string, r DeliveryResult) error {
	var delivered sql.NullInt64
	if r.Status == DeliveryDelivered {
		delivered = sql.NullInt64{Int64: ms(r.At), Valid: true}
	}
	_, err := s.w.ExecContext(ctx,
		`UPDATE webhook_deliveries SET status = ?, lease_until = NULL, last_status_code = ?, last_error = ?,
		   last_duration_ms = ?, next_attempt_at = ?, delivered_at = ?
		 WHERE id = ? AND status = 'delivering'`,
		r.Status, nullInt(r.StatusCode), nullStr(r.Error), r.DurationMs, ms(r.NextAttemptAt), delivered, id)
	return mapErr(err)
}

// RetryDelivery resets a delivery for manual re-send.
func (s *Store) RetryDelivery(ctx context.Context, id string, now time.Time) (bool, error) {
	return s.execOne(ctx,
		`UPDATE webhook_deliveries SET status = 'pending', attempts = 0, next_attempt_at = ?, lease_until = NULL
		 WHERE id = ? AND status IN ('dead', 'delivered', 'pending')`, ms(now), id)
}

// DeliveryFilter selects deliveries for ListDeliveries.
type DeliveryFilter struct {
	ProjectID     string
	Status        string
	CursorCreated int64
	CursorID      string
	Limit         int
}

// ListDeliveries returns deliveries of a project, newest event first.
func (s *Store) ListDeliveries(ctx context.Context, f DeliveryFilter) ([]*Delivery, error) {
	q := `SELECT ` + deliveryCols + deliveryFrom + ` WHERE w.project_id = ?`
	args := []any{f.ProjectID}
	if f.Status != "" {
		q += ` AND d.status = ?`
		args = append(args, f.Status)
	}
	if f.CursorID != "" {
		q += ` AND (e.created_at < ? OR (e.created_at = ? AND d.id < ?))`
		args = append(args, f.CursorCreated, f.CursorCreated, f.CursorID)
	}
	q += ` ORDER BY e.created_at DESC, d.id DESC LIMIT ?`
	args = append(args, f.Limit)
	rows, err := s.r.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []*Delivery{}
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, mapErr(rows.Err())
}

// ListDeliveriesByEvent returns the deliveries of an event.
func (s *Store) ListDeliveriesByEvent(ctx context.Context, eventID string) ([]*Delivery, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT `+deliveryCols+deliveryFrom+` WHERE d.event_id = ? ORDER BY d.id`, eventID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []*Delivery{}
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, mapErr(rows.Err())
}

// NextDeliveryDue returns the earliest next_attempt_at of pending deliveries.
func (s *Store) NextDeliveryDue(ctx context.Context) (*time.Time, error) {
	var v sql.NullInt64
	if err := s.r.QueryRowContext(ctx, `SELECT min(next_attempt_at) FROM webhook_deliveries WHERE status = 'pending'`).Scan(&v); err != nil {
		return nil, mapErr(err)
	}
	return fromNullMs(v), nil
}
