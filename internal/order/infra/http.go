package infra

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/xrodriguezb/fulcrum/internal/order/app"
	"github.com/xrodriguezb/fulcrum/internal/order/domain"
	"github.com/xrodriguezb/fulcrum/internal/platform/errs"
	"github.com/xrodriguezb/fulcrum/internal/platform/httpx"
	"github.com/xrodriguezb/fulcrum/internal/platform/logging"
)

// Pagination bounds. A caller that asks for everything gets the default page,
// because an unbounded list is a denial of service with a friendly name.
const (
	defaultPageSize = 20
	maxPageSize     = 100
)

// OrderCreator is the use case this handler drives. The interface is declared
// here, where it is consumed, so a test can supply a double without the use case
// knowing that transport exists.
type OrderCreator interface {
	Handle(ctx context.Context, cmd app.CreateOrderCommand) (app.CreateOrderResult, error)
}

// OrderReader loads orders for the read endpoints.
type OrderReader interface {
	ByID(ctx context.Context, id domain.OrderID) (*domain.Order, error)
	Page(ctx context.Context, limit, offset int) ([]*domain.Order, int, error)
}

// Handlers serves the order endpoints.
type Handlers struct {
	creator OrderCreator
	reader  OrderReader
	logger  *slog.Logger
}

// NewHandlers wires the order endpoints.
func NewHandlers(creator OrderCreator, reader OrderReader, logger *slog.Logger) *Handlers {
	return &Handlers{creator: creator, reader: reader, logger: logger}
}

// createOrderRequest is the wire shape of a create request. It is separate from
// the command so that an unknown field is rejected here rather than ignored.
type createOrderRequest struct {
	CustomerID string            `json:"customer_id"`
	Lines      []app.CommandLine `json:"lines"`
}

// pageResponse is the envelope of a list endpoint.
type pageResponse struct {
	Items  []app.OrderView `json:"items"`
	Total  int             `json:"total"`
	Limit  int             `json:"limit"`
	Offset int             `json:"offset"`
}

// Create handles POST /api/v1/orders.
func (h *Handlers) Create(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		// A body that exceeds the limit fails here, and the limit middleware has
		// already decided what that means, so only the classification is added.
		var maxBytes *http.MaxBytesError
		if errors.As(err, &maxBytes) {
			httpx.WriteProblem(w, r, errs.E(errs.KindTooLarge, errs.CodeRequestTooLarge,
				"The request body is larger than the limit.", err), h.logger)
			return
		}
		httpx.WriteProblem(w, r, errs.Validation(errs.CodeValidationFailed,
			"The request body could not be read.", err), h.logger)
		return
	}

	parsed, err := decodeStrict[createOrderRequest](body)
	if err != nil {
		httpx.WriteProblem(w, r, err, h.logger)
		return
	}
	if err := validateCreateRequest(parsed); err != nil {
		httpx.WriteProblem(w, r, err, h.logger)
		return
	}

	ctx := r.Context()
	result, err := h.creator.Handle(ctx, app.CreateOrderCommand{
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
		RawBody:        body,
		CustomerID:     parsed.CustomerID,
		Lines:          parsed.Lines,
		CorrelationID:  logging.CorrelationID(ctx),
		TraceID:        logging.TraceID(ctx),
	})
	if err != nil {
		httpx.WriteProblem(w, r, err, h.logger)
		return
	}

	if result.Replayed {
		// The stored bytes are returned unchanged. Re-encoding the view would
		// risk answering a retry with a different document than the original.
		w.Header().Set("Idempotent-Replay", "true")
		httpx.WriteRaw(w, r, result.Status, result.Body, h.logger)
		return
	}
	httpx.WriteRaw(w, r, result.Status, result.Body, h.logger)
}

// Get handles GET /api/v1/orders/{id}.
func (h *Handlers) Get(w http.ResponseWriter, r *http.Request) {
	id, err := domain.NewOrderID(r.PathValue("id"))
	if err != nil {
		httpx.WriteProblem(w, r, errs.Validation(errs.CodeValidationFailed,
			"The order id is not a valid identifier.", err), h.logger)
		return
	}

	order, err := h.reader.ByID(r.Context(), id)
	if err != nil {
		httpx.WriteProblem(w, r, err, h.logger)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, app.ViewOf(order), h.logger)
}

// List handles GET /api/v1/orders.
func (h *Handlers) List(w http.ResponseWriter, r *http.Request) {
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

	orders, total, err := h.reader.Page(r.Context(), limit, offset)
	if err != nil {
		httpx.WriteProblem(w, r, err, h.logger)
		return
	}

	items := make([]app.OrderView, 0, len(orders))
	for _, order := range orders {
		items = append(items, app.ViewOf(order))
	}
	httpx.WriteJSON(w, r, http.StatusOK, pageResponse{
		Items: items, Total: total, Limit: limit, Offset: offset,
	}, h.logger)
}

// decodeStrict parses a body and refuses anything the contract does not
// describe: unknown fields, and a second document after the first.
func decodeStrict[T any](body []byte) (T, error) {
	var value T

	decoder := json.NewDecoder(newReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, errs.Validation(errs.CodeValidationFailed,
			"The request body is not valid json for this endpoint.", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return value, errs.Validation(errs.CodeValidationFailed,
			"The request body carries content after the json document.", nil)
	}
	return value, nil
}

func validateCreateRequest(request createOrderRequest) error {
	if _, err := domain.NewCustomerID(request.CustomerID); err != nil {
		return errs.Validation(errs.CodeValidationFailed, "The customer id is not a valid identifier.", err)
	}
	if len(request.Lines) == 0 {
		return errs.Validation(errs.CodeValidationFailed, "An order needs at least one line.", nil)
	}
	if len(request.Lines) > domain.MaxOrderLines {
		return errs.Validation(errs.CodeValidationFailed, "The order has too many lines.", nil)
	}

	seen := make(map[string]struct{}, len(request.Lines))
	for _, line := range request.Lines {
		sku, err := domain.NewSKU(line.SKU)
		if err != nil {
			return errs.Validation(errs.CodeValidationFailed, "A line carries an invalid sku.", err)
		}
		if _, err := domain.NewQuantity(line.Quantity); err != nil {
			return errs.Validation(errs.CodeValidationFailed, "A line carries an invalid quantity.", err)
		}
		if _, duplicate := seen[sku.String()]; duplicate {
			return errs.Validation(errs.CodeValidationFailed, "The same sku appears more than once.", nil)
		}
		seen[sku.String()] = struct{}{}
	}
	return nil
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
