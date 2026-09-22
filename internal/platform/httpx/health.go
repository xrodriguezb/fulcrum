package httpx

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/xrodriguezb/fulcrum/internal/platform/errs"
)

// Check is one readiness dependency.
//
// Critical decides what a failure means. A dependency the request path cannot
// work without makes the instance unready, and a load balancer should stop
// sending it traffic. A dependency the instance can survive without makes it
// degraded, and pulling it from rotation would cause the outage rather than
// avoid it.
type Check struct {
	Name     string
	Critical bool
	Probe    func(ctx context.Context) error
}

// healthResponse is the body of both probes.
type healthResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks,omitempty"`
}

// Readiness outcomes.
const (
	statusOK       = "ok"
	statusDegraded = "degraded"
)

// Liveness answers whether the process is running, and nothing else.
//
// It deliberately checks no dependency. A liveness probe that fails when the
// database is unreachable makes an orchestrator restart a healthy process during
// a database incident, which turns a degradation into an outage.
func Liveness(logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, r, http.StatusOK, healthResponse{Status: statusOK}, logger)
	}
}

// Readiness answers whether this instance can do its job.
//
// That is not the same as every dependency being reachable. The API accepts
// orders while the broker is down, because the outbox decouples acceptance from
// publication: that is the property the whole system is built around, and
// reporting unready would make a load balancer remove the instance and cause the
// outage the design exists to prevent. A broker failure is therefore degraded
// rather than unready, and the body says which dependency is affected.
//
// The worker makes the opposite call for the same reason: publishing is its job,
// so it cannot do it without the broker. Readiness is per binary because the two
// binaries have different jobs.
func Readiness(checks []Check, budget time.Duration, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), budget)
		defer cancel()

		results := make(map[string]string, len(checks))
		ready := true
		degraded := false

		for _, check := range checks {
			if err := check.Probe(ctx); err != nil {
				if check.Critical {
					ready = false
					results[check.Name] = "unavailable"
				} else {
					degraded = true
					results[check.Name] = statusDegraded
				}
				// The reason is named but not described: a load balancer needs
				// the verdict, and the detail belongs in the log.
				logger.WarnContext(ctx, "readiness check failed",
					slog.String("check", check.Name),
					slog.Bool("critical", check.Critical),
					slog.String("error", err.Error()))
				continue
			}
			results[check.Name] = statusOK
		}

		if !ready {
			// The unavailable answer goes through the same writer as every other
			// error, so it carries the same content type and status the rest of
			// the API uses. Writing the header here and again in the writer also
			// produced a superfluous WriteHeader call.
			WriteProblem(w, r, errs.Unavailable(
				"A dependency this service needs is not reachable.", nil), logger)
			return
		}

		status := statusOK
		if degraded {
			status = statusDegraded
		}
		WriteJSON(w, r, http.StatusOK, healthResponse{Status: status, Checks: results}, logger)
	}
}
