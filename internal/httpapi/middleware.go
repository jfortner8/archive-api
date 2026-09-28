package httpapi

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/jfortner8/archive-api/internal/domain"
)

// Middleware wraps a handler.
type Middleware func(http.Handler) http.Handler

// chain applies middleware so the first listed is outermost.
func chain(h http.Handler, middleware ...Middleware) http.Handler {
	for i := len(middleware) - 1; i >= 0; i-- {
		h = middleware[i](h)
	}
	return h
}

type ctxKey int

const (
	ctxRequestID ctxKey = iota
	ctxAccountID
	ctxMember
)

func requestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(ctxRequestID).(string)
	return id
}

func accountIDFrom(ctx context.Context) (domain.AccountID, bool) {
	id, ok := ctx.Value(ctxAccountID).(domain.AccountID)
	return id, ok
}

// memberFrom returns the caller's membership of the archive named in the
// path. Present on every archive-scoped route, because the middleware
// resolves it before routing.
func memberFrom(ctx context.Context) (domain.Member, bool) {
	member, ok := ctx.Value(ctxMember).(domain.Member)
	return member, ok
}

func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if id == "" {
			id = uuid.NewString()
		}
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxRequestID, id)))
	})
}

// withRecovery turns a panic into a 500 with the usual envelope rather than
// a dropped connection, and logs the cause.
func withRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.Error("panic serving request",
					"panic", recovered,
					"requestId", requestIDFrom(r.Context()),
					"method", r.Method,
					"path", r.URL.Path,
				)
				writeError(w, r, errInternal())
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// statusRecorder remembers the status so it can be logged, and so the
// JSON-error middleware can tell whether ServeMux answered for itself.
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written bool
}

func (s *statusRecorder) WriteHeader(status int) {
	if s.written {
		return
	}
	s.status = status
	s.written = true
	s.ResponseWriter.WriteHeader(status)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.written {
		s.WriteHeader(http.StatusOK)
	}
	return s.ResponseWriter.Write(b)
}

func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		slog.Info("request",
			"requestId", requestIDFrom(r.Context()),
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"durationMs", time.Since(start).Milliseconds(),
		)
	})
}

// withAPIKey checks the shared key identifying our own proxy.
//
// This is the weakest of the three layers guarding this service and must
// never be the only thing between a new route and the internet - that is
// what applying this chain to a whole mounted subtree is for.
func (s *Server) withAPIKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided := r.Header.Get("X-API-Key")
		if subtle.ConstantTimeCompare([]byte(provided), []byte(s.APIKey)) != 1 {
			writeError(w, r, errUnauthenticated("invalid or missing API key"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// withAuth verifies the Cognito access token and puts the caller's account
// id in the request context.
//
// The token travels in X-User-Token rather than Authorization, which is not
// a stylistic choice: archives-ui signs every request with AWS SigV4 to
// satisfy the Function URL, and SigV4 libraries own the Authorization header
// outright, overwriting whatever was there. Giving the account token its own
// header sidesteps the collision instead of fighting it.
func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimSpace(r.Header.Get("X-User-Token"))
		if token == "" {
			writeError(w, r, errUnauthenticated("invalid or missing token"))
			return
		}

		claims, err := s.Verifier.Verify(r.Context(), token)
		if err != nil || claims.Sub == "" {
			writeError(w, r, errUnauthenticated("invalid or missing token"))
			return
		}

		ctx := context.WithValue(r.Context(), ctxAccountID, domain.AccountID(claims.Sub))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// withArchiveAccess resolves the archive named in the path, confirms the
// caller is a member, and checks their role against what the method implies.
//
// It reads the id out of the path rather than using PathValue, because
// middleware runs before ServeMux has matched a pattern. Doing it here rather
// than in each handler is the point: a route added later cannot forget the
// membership check, and cannot forget that a viewer may not write.
func (s *Server) withArchiveAccess(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		archiveID, ok := archiveIDFromPath(r.URL.Path)
		if !ok {
			// Not an archive-scoped route (/v1/me, /v1/archives, ...).
			next.ServeHTTP(w, r)
			return
		}

		account, ok := accountIDFrom(r.Context())
		if !ok {
			writeError(w, r, errInternal())
			return
		}

		member, err := s.Store.GetMember(r.Context(), archiveID, account)
		if err != nil {
			// A non-member gets the same answer as a non-existent archive,
			// so ids cannot be probed.
			writeError(w, r, toAPIError(err))
			return
		}

		// Safe by default: anything that is not a read requires write access,
		// so a mutating route added later is protected without anyone having
		// to remember. Routes needing more than this (member management)
		// check for themselves.
		action := domain.ActionRead
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			action = domain.ActionWrite
		}
		if !member.Role.Can(action) {
			writeError(w, r, errForbidden("your role in this archive does not permit this"))
			return
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxMember, member)))
	})
}

// archiveIDFromPath pulls the id out of /v1/archives/{id}/... . It returns
// false for /v1/archives itself, which is a collection rather than one
// archive.
func archiveIDFromPath(path string) (domain.ArchiveID, bool) {
	rest, ok := strings.CutPrefix(path, "/v1/archives/")
	if !ok || rest == "" {
		return "", false
	}
	if idx := strings.IndexByte(rest, '/'); idx >= 0 {
		rest = rest[:idx]
	}
	if rest == "" {
		return "", false
	}
	return domain.ArchiveID(rest), true
}

// withJSONErrors rewrites the plaintext 404 and 405 ServeMux produces into
// the standard envelope.
//
// Go offers no hook for either, and the old API leaked both - so a client
// hitting a mistyped path got `404 page not found` in plain text while every
// other error was JSON. Buffering is confined to those two cases; a normal
// response passes straight through.
func withJSONErrors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		interceptor := &muxErrorInterceptor{ResponseWriter: w, request: r}
		next.ServeHTTP(interceptor, r)
		interceptor.finish()
	})
}

type muxErrorInterceptor struct {
	http.ResponseWriter
	request   *http.Request
	status    int
	suppress  bool
	wroteBody bool
}

func (m *muxErrorInterceptor) WriteHeader(status int) {
	m.status = status
	// ServeMux answers these two itself, in plain text, and sets no content
	// type of our own - so they are the ones worth replacing.
	if status == http.StatusNotFound || status == http.StatusMethodNotAllowed {
		if m.Header().Get("Content-Type") != "application/json" {
			m.suppress = true
			return
		}
	}
	m.ResponseWriter.WriteHeader(status)
}

func (m *muxErrorInterceptor) Write(b []byte) (int, error) {
	if m.suppress {
		// Swallow ServeMux's plaintext body; finish() writes ours.
		return len(b), nil
	}
	if m.status == 0 {
		m.WriteHeader(http.StatusOK)
	}
	m.wroteBody = true
	return m.ResponseWriter.Write(b)
}

func (m *muxErrorInterceptor) finish() {
	if !m.suppress {
		return
	}

	err := errNotFound()
	if m.status == http.StatusMethodNotAllowed {
		err = &apiError{
			Status:  http.StatusMethodNotAllowed,
			Code:    "not_found",
			Message: "that method is not allowed on this path",
		}
	}
	// The Allow header ServeMux set is still useful; leave it in place.
	m.suppress = false
	writeError(m.ResponseWriter, m.request, err)
}
