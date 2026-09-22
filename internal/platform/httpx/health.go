package httpx

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/xrodriguezb/fulcrum/internal/platform/errs"
)

// Check is one readiness dependency.
type Check struct {
	Name  string
	Probe func(ctx context.Context) error
}

// healthResponse is the body of both probes.
type healthResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks,omitempty"`
}

// Liveness answers whether the process is running, and nothing else.
//
// It deliberately checks no dependency. A liveness probe that fails when the
// database is unreachable makes an orchestrator restart a healthy process during
// a database incident, which turns a degradation into an outage.
func Liveness(logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, r, http.StatusOK, healthResponse{Status: "ok"}, logger)
	}
}

// Readiness answers whether the process can serve traffic, which means every
// dependency it needs to answer a request is reachable.
func Readiness(checks []Check, budget time.Duration, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), budget)
		defer cancel()

		results := make(map[string]string, len(checks))
		healthy := true

		for _, check := range checks {
			if err := check.Probe(ctx); err != nil {
				healthy = false
				// The reason is named but not described: "unavailable" is what a
				// load balancer needs, and the detail belongs in the log.
				results[check.Name] = "unavailable"
				logger.WarnContext(ctx, "readiness check failed",
					slog.String("check", check.Name),
					slog.String("error", err.Error()))
				continue
			}
			results[check.Name] = "ok"
		}

		if !healthy {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusServiceUnavailable)
			problem := Problem{
				Type:     problemBaseURI + slug(errs.CodeServiceUnavailable),
				Title:    "Service unavailable",
				Status:   http.StatusServiceUnavailable,
				Code:     errs.CodeServiceUnavailable,
				Detail:   "A dependency this service needs is not reachable.",
				Instance: r.URL.Path,
			}
			WriteJSON(w, r, http.StatusServiceUnavailable, problem, logger)
			return
		}

		WriteJSON(w, r, http.StatusOK, healthResponse{Status: "ok", Checks: results}, logger)
	}
}
