package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/UdjinStolboviy/kadr-madia/internal/db"
)

// Event types (docs/API.md "Webhooks").
const (
	EventAssetReady      = "asset.ready"
	EventAssetFailed     = "asset.failed"
	EventAssetUpdated    = "asset.updated"
	EventAssetDeleted    = "asset.deleted"
	EventUploadExpired   = "upload.expired"
	EventQuotaWarning    = "quota.warning"
	EventQuotaExceeded   = "quota.exceeded"
	EventNamespacePurged = "namespace.purged"
	// EventTest is sent by the admin "test" action only.
	EventTest = "webhook.test"
)

// Envelope is the JSON body of every webhook.
type Envelope struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Data      any    `json:"data"`
}

// Emit writes an event and one pending delivery per enabled endpoint of the
// project subscribed to its type. tx should be the transaction that commits
// the state change the event describes (transactional outbox). Call the
// dispatcher's Notify after the commit for immediate delivery.
func Emit(ctx context.Context, tx *db.Store, projectID, typ string, data any, now time.Time) (*db.Event, error) {
	body, err := json.Marshal(Envelope{Type: typ, Timestamp: now.UTC().Format(time.RFC3339), Data: data})
	if err != nil {
		return nil, fmt.Errorf("webhook: marshal %s: %w", typ, err)
	}
	ev := &db.Event{ID: db.NewID(db.PrefixEvent), ProjectID: projectID, Type: typ, Payload: body, CreatedAt: now}
	if err := tx.InsertEvent(ctx, ev); err != nil {
		return nil, err
	}
	endpoints, err := tx.ListWebhookEndpoints(ctx, projectID)
	if err != nil {
		return nil, err
	}
	for _, e := range endpoints {
		if e.DisabledAt != nil || !Subscribed(e.Events, typ) {
			continue
		}
		if err := tx.InsertDelivery(ctx, &db.Delivery{ID: db.NewID(db.PrefixDelivery), EventID: ev.ID, EndpointID: e.ID, NextAttemptAt: now}); err != nil {
			return nil, err
		}
	}
	return ev, nil
}

// Subscribed reports whether an endpoint filter matches an event type.
// Filters are exact types, "*" (everything) or a prefix wildcard "asset.*".
func Subscribed(filters []string, typ string) bool {
	for _, f := range filters {
		switch {
		case f == "*" || f == typ:
			return true
		case strings.HasSuffix(f, ".*") && strings.HasPrefix(typ, strings.TrimSuffix(f, "*")):
			return true
		}
	}
	return false
}

// ValidFilter reports whether f is an acceptable endpoint event filter.
func ValidFilter(f string) bool {
	if f == "*" {
		return true
	}
	for _, t := range []string{EventAssetReady, EventAssetFailed, EventAssetUpdated, EventAssetDeleted, EventUploadExpired,
		EventQuotaWarning, EventQuotaExceeded, EventNamespacePurged} {
		if f == t {
			return true
		}
	}
	switch f {
	case "asset.*", "upload.*", "quota.*", "namespace.*":
		return true
	}
	return false
}
