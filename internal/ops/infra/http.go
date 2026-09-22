package infra

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	inventoryapp "github.com/xrodriguezb/fulcrum/internal/inventory/app"
	"github.com/xrodriguezb/fulcrum/internal/ops/app"
	"github.com/xrodriguezb/fulcrum/internal/platform/errs"
	"github.com/xrodriguezb/fulcrum/internal/platform/httpx"
)

const (
	defaultPageSize = 20
	maxPageSize     = 100

	// streamInterval is how often the stream re-reads the snapshot. It is a poll
	// behind an event interface: the alternative, a database notification
	// channel, would add a dependency for a console that refreshes once a
	// second anyway.
	streamInterval = time.Second
	// heartbeatInterval keeps proxies from closing an idle stream.
	heartbeatInterval = 15 * time.Second
)

// Handlers serves the operational endpoints.
type Handlers struct {
	reader    app.Reader
	inventory inventoryapp.Reader
	logger    *slog.Logger
}

// NewHandlers wires the operational endpoints.
func NewHandlers(reader app.Reader, inventory inventoryapp.Reader, logger *slog.Logger) *Handlers {
	return &Handlers{reader: reader, inventory: inventory, logger: logger}
}

// Outbox handles GET /api/v1/ops/outbox.
func (h *Handlers) Outbox(w http.ResponseWriter, r *http.Request) {
	snapshot, err := h.reader.Snapshot(r.Context())
	if err != nil {
		httpx.WriteProblem(w, r, err, h.logger)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, snapshot, h.logger)
}

// DeadLetters handles GET /api/v1/ops/dead-letters.
func (h *Handlers) DeadLetters(w http.ResponseWriter, r *http.Request) {
	limit, err := intParam(r, "limit", defaultPageSize, 1, maxPageSize)
	if err != nil {
		httpx.WriteProblem(w, r, err, h.logger)
		return
	}
	offset, err := intParam(r, "offset", 0, 0, 1_000_000)
	if err != nil {
		httpx.WriteProblem(w, r, err, h.logger)
		return
	}

	entries, total, err := h.reader.DeadLetters(r.Context(), limit, offset)
	if err != nil {
		httpx.WriteProblem(w, r, err, h.logger)
		return
	}

	httpx.WriteJSON(w, r, http.StatusOK, struct {
		Items  []app.DeadLetter `json:"items"`
		Total  int              `json:"total"`
		Limit  int              `json:"limit"`
		Offset int              `json:"offset"`
	}{Items: entries, Total: total, Limit: limit, Offset: offset}, h.logger)
}

// Inventory handles GET /api/v1/inventory.
func (h *Handlers) Inventory(w http.ResponseWriter, r *http.Request) {
	items, err := h.inventory.List(r.Context())
	if err != nil {
		httpx.WriteProblem(w, r, err, h.logger)
		return
	}

	views := make([]inventoryView, 0, len(items))
	for _, item := range items {
		views = append(views, inventoryView{
			SKU:            item.SKU,
			Available:      item.Available,
			Reserved:       item.Reserved,
			UnitPriceCents: item.UnitPriceCents,
			Currency:       item.Currency,
			Version:        item.Version,
		})
	}
	httpx.WriteJSON(w, r, http.StatusOK, struct {
		Items []inventoryView `json:"items"`
	}{Items: views}, h.logger)
}

type inventoryView struct {
	SKU            string `json:"sku"`
	Available      int    `json:"available"`
	Reserved       int    `json:"reserved"`
	UnitPriceCents int64  `json:"unit_price_cents"`
	Currency       string `json:"currency"`
	Version        int    `json:"version"`
}

// Stream handles GET /api/v1/ops/stream as server sent events.
//
// The stream sends a snapshot only when it differs from the last one sent, plus
// a heartbeat comment on a slower timer. A console that is told nothing changed
// sixty times a minute is a console that costs bandwidth to say nothing.
func (h *Handlers) Stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		httpx.WriteProblem(w, r, errs.Internal("streaming is not supported by this writer", nil), h.logger)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	// Proxies that buffer would defeat the point of a stream.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	ctx := r.Context()
	ticker := time.NewTicker(streamInterval)
	defer ticker.Stop()
	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()

	var last string
	send := func() bool {
		snapshot, err := h.reader.Snapshot(ctx)
		if err != nil {
			// A failed read is not a reason to drop the connection: the next
			// tick may succeed, and the console shows the stream as degraded.
			h.logger.WarnContext(ctx, "operational snapshot failed", slog.String("error", err.Error()))
			return true
		}
		payload := formatSnapshot(snapshot)
		if payload == last {
			return true
		}
		last = payload
		if _, writeErr := fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", payload); writeErr != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	if !send() {
		return
	}

	for {
		select {
		case <-ctx.Done():
			// The client disconnected or the server is shutting down. Both mean
			// the same thing here: stop, release the goroutine.
			return
		case <-ticker.C:
			if !send() {
				return
			}
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// formatSnapshot renders the payload and doubles as the change detector, so the
// comparison is made on exactly what would be sent.
func formatSnapshot(snapshot app.Snapshot) string {
	return fmt.Sprintf(
		`{"outbox":{"pending":%d,"failing":%d,"published":%d,"oldest_unpublished_seconds":%.3f},`+
			`"dead_letters":%d,"stuck_idempotency_keys":%d}`,
		snapshot.Outbox.Pending, snapshot.Outbox.Failing, snapshot.Outbox.Published,
		snapshot.Outbox.OldestUnpublishedSec, snapshot.DeadLetters, snapshot.StuckIdempotencyKeys)
}

func intParam(r *http.Request, name string, fallback, minimum, maximum int) (int, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, errs.Validation(errs.CodeValidationFailed, "The "+name+" parameter must be an integer.", err)
	}
	if value < minimum || value > maximum {
		return 0, errs.Validation(errs.CodeValidationFailed, "The "+name+" parameter is out of range.", nil)
	}
	return value, nil
}
