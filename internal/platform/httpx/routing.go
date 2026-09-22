package httpx

import (
	"log/slog"
	"net/http"

	"github.com/xrodriguezb/fulcrum/internal/platform/errs"
)

// RoutingProblems turns the router's own rejections into problem documents.
//
// ServeMux answers an unknown path with 404 and a wrong method with 405, both as
// plain text. That makes routing the one place where this API does not speak its
// own error contract, and a client that parses problem documents has to special
// case it. This wrapper keeps the statuses and replaces the bodies.
func RoutingProblems(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			interceptor := &routingInterceptor{ResponseWriter: w}
			next.ServeHTTP(interceptor, r)

			if !interceptor.intercepted {
				return
			}

			switch interceptor.status {
			case http.StatusNotFound:
				WriteProblem(w, r, errs.NotFound(errs.CodeValidationFailed,
					"No endpoint matches this path.", nil), logger)
			case http.StatusMethodNotAllowed:
				WriteProblem(w, r, errs.E(errs.KindMethodNotAllowed, errs.CodeMethodNotAllowed,
					"That method is not allowed on this path.", nil), logger)
			}
		})
	}
}

// routingInterceptor swallows the router's own 404 and 405 bodies, and passes
// everything else straight through. Only a response the router wrote itself is
// intercepted: a handler that deliberately returns 404 has already written a
// problem document, and that body is what identifies it.
type routingInterceptor struct {
	http.ResponseWriter
	status      int
	intercepted bool
	wroteHeader bool
}

func (i *routingInterceptor) WriteHeader(status int) {
	if i.wroteHeader {
		return
	}
	i.wroteHeader = true
	i.status = status

	if status == http.StatusNotFound || status == http.StatusMethodNotAllowed {
		// The status is written by the problem writer instead, once the body is
		// known. Anything the router wanted to write is dropped.
		if i.Header().Get("Content-Type") == "text/plain; charset=utf-8" || i.Header().Get("Content-Type") == "" {
			i.intercepted = true
			i.Header().Del("Content-Type")
			return
		}
	}
	i.ResponseWriter.WriteHeader(status)
}

func (i *routingInterceptor) Write(b []byte) (int, error) {
	if i.intercepted {
		// Pretend the write succeeded. The body is replaced by the problem
		// document the wrapper writes afterwards.
		return len(b), nil
	}
	if !i.wroteHeader {
		i.wroteHeader = true
		i.status = http.StatusOK
	}
	n, err := i.ResponseWriter.Write(b)
	return n, err //nolint:wrapcheck // a ResponseWriter returns the underlying error unchanged
}

// Flush forwards to the wrapped writer, which the event stream depends on.
func (i *routingInterceptor) Flush() {
	if flusher, ok := i.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}
