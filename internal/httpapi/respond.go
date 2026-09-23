package httpapi

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/jfortner8/archive-api/internal/httpapi/gen"
)

// handlerFunc is what every route is written as. Returning an error rather
// than writing one means no handler can invent its own error shape, and the
// 500-vs-404 decision lives in exactly one place.
type handlerFunc func(http.ResponseWriter, *http.Request) error

func (s *Server) handle(h handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := h(w, r); err != nil {
			writeError(w, r, err)
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if body == nil {
		return nil
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// The status line is already sent, so there is nothing to do but
		// record it. Returning the error would try to write a second one.
		slog.Error("encoding response", "error", err, "requestId", "")
	}
	return nil
}

func writeError(w http.ResponseWriter, r *http.Request, err error) {
	apiErr := toAPIError(err)
	requestID := requestIDFrom(r.Context())

	// An internal failure is logged with its real cause and answered with a
	// generic message - the underlying text can name a table, a key or an
	// account id.
	if apiErr.Status >= 500 {
		slog.Error("request failed",
			"error", err,
			"requestId", requestID,
			"method", r.Method,
			"path", r.URL.Path,
		)
	}

	body := gen.Error{}
	body.Error.Code = apiErr.Code
	body.Error.Message = apiErr.Message
	if requestID != "" {
		body.Error.RequestId = &requestID
	}
	if len(apiErr.Details) > 0 {
		body.Error.Details = &apiErr.Details
	}

	_ = writeJSON(w, apiErr.Status, body)
}

// readJSON decodes a request body, rejecting unknown fields.
//
// Rejecting them is deliberate and worth the strictness while the contract is
// young: a client sending `titel` should be told, not have its typo silently
// discarded. Every request schema declares `additionalProperties: false`, so
// this matches the spec rather than surprising a generated client.
func readJSON(r *http.Request, dst any) error {
	if ct := r.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "application/json") {
		return errBadRequest("expected a JSON body")
	}

	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxRequestBody))
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		return errBadRequest(describeDecodeError(err))
	}
	if dec.More() {
		return errBadRequest("body contained more than one JSON value")
	}
	return nil
}

// maxRequestBody caps a request. Item metadata is small; file bytes never
// come through this service at all.
const maxRequestBody = 1 << 20

func describeDecodeError(err error) string {
	msg := err.Error()
	// json's own wording for this is "json: unknown field \"x\"", which is
	// clear enough to pass through but reads better without the prefix.
	if field, ok := strings.CutPrefix(msg, "json: unknown field "); ok {
		return "unknown field " + field
	}
	return "could not parse the request body"
}

// etagFor renders a record version as a weak ETag. Weak because the bytes
// may differ between responses - a presigned URL is regenerated every time -
// while the record they describe has not changed.
func etagFor(version int64) string {
	return `W/"` + strconv.FormatInt(version, 10) + `"`
}

// parseIfMatch reads an If-Match header into the version it names.
//
// A missing header means "I am not claiming to have read any particular
// version", and the write proceeds. A malformed one is rejected rather than
// ignored: silently treating a garbled precondition as no precondition would
// defeat the point of sending it.
func parseIfMatch(r *http.Request) (*int64, error) {
	raw := strings.TrimSpace(r.Header.Get("If-Match"))
	if raw == "" {
		return nil, nil
	}
	if raw == "*" {
		return nil, nil
	}

	trimmed := strings.TrimPrefix(raw, "W/")
	trimmed = strings.Trim(trimmed, `"`)

	version, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil {
		return nil, errBadRequest(fmt.Sprintf("If-Match %q is not an ETag this API issued", raw))
	}
	return &version, nil
}
