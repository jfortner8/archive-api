// Package httpapi is the HTTP surface: routing, middleware, and the
// translation between wire shapes and domain types.
//
// Nothing here imports the AWS SDK. The store and the presigner are reached
// through the narrow interfaces in ports.go, so handlers can be tested
// without any of it.
package httpapi

import (
	"errors"
	"net/http"

	"github.com/jfortner8/archive-api/internal/domain"
	"github.com/jfortner8/archive-api/internal/httpapi/gen"
	"github.com/jfortner8/archive-api/internal/store"
)

// apiError is an error with a wire representation. Handlers return these
// rather than writing responses directly, so every failure leaves through
// one place and cannot accidentally be a bare string or a plaintext body.
type apiError struct {
	Status  int
	Code    gen.ErrorCode
	Message string
	Details []gen.FieldError
}

func (e *apiError) Error() string { return e.Message }

func errUnauthenticated(msg string) *apiError {
	return &apiError{Status: http.StatusUnauthorized, Code: gen.ErrorCodeUnauthenticated, Message: msg}
}

// errNotFound is deliberately the same response for "no such record" and
// "you are not a member of that archive". Distinguishing them would let
// anyone confirm an archive exists by probing ids.
func errNotFound() *apiError {
	return &apiError{Status: http.StatusNotFound, Code: gen.ErrorCodeNotFound, Message: "not found"}
}

// errForbidden is for a member whose role is too low. Unlike errNotFound
// this does disclose that the archive exists - which is fine, because they
// already knew.
func errForbidden(msg string) *apiError {
	return &apiError{Status: http.StatusForbidden, Code: gen.ErrorCodeForbidden, Message: msg}
}

func errBadRequest(msg string) *apiError {
	return &apiError{Status: http.StatusBadRequest, Code: gen.ErrorCodeValidationFailed, Message: msg}
}

func errConflict(msg string) *apiError {
	return &apiError{Status: http.StatusConflict, Code: gen.ErrorCodeConflict, Message: msg}
}

func errPreconditionFailed() *apiError {
	return &apiError{
		Status:  http.StatusPreconditionFailed,
		Code:    gen.ErrorCodePreconditionFailed,
		Message: "this record changed since you last read it; re-read it and try again",
	}
}

func errInternal() *apiError {
	return &apiError{
		Status:  http.StatusInternalServerError,
		Code:    gen.ErrorCodeInternal,
		Message: "internal error",
	}
}

// errValidation turns per-field domain errors into a response a form can act
// on, rather than one sentence a user has to guess their way through.
func errValidation(errs domain.ValidationErrors) *apiError {
	details := make([]gen.FieldError, 0, len(errs))
	for _, e := range errs {
		details = append(details, gen.FieldError{Field: e.Field, Code: e.Code, Message: e.Message})
	}
	return &apiError{
		Status:  http.StatusUnprocessableEntity,
		Code:    gen.ErrorCodeValidationFailed,
		Message: "the request was well formed but invalid",
		Details: details,
	}
}

// toAPIError maps anything a handler can return onto a response.
//
// Unrecognised errors become a bare 500: an internal failure should never
// leak its message, which may name a table, a key, or an AWS account.
func toAPIError(err error) *apiError {
	var apiErr *apiError
	if errors.As(err, &apiErr) {
		return apiErr
	}

	var validation domain.ValidationErrors
	if errors.As(err, &validation) {
		return errValidation(validation)
	}

	switch {
	case errors.Is(err, store.ErrNotFound):
		return errNotFound()
	case errors.Is(err, store.ErrVersionConflict):
		return errPreconditionFailed()
	case errors.Is(err, store.ErrAlreadyExists):
		return errConflict("already exists")
	case errors.Is(err, store.ErrBadCursor):
		return errBadRequest("this cursor is not valid for these filters; start from the first page")
	}

	return errInternal()
}
