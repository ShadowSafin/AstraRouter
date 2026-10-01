package domain

import (
	"errors"
	"fmt"
	"net/http"
)

// ErrorCode is the normalized, provider-agnostic failure classification.
//
// Adapters translate vendor-specific failures into these codes so that routing,
// fallback and retry decisions never branch on provider-specific strings. Adding
// a provider therefore cannot change routing behaviour by accident.
type ErrorCode string

const (
	// ErrCodeInvalidRequest means the client sent something unusable. It is
	// never retried and never triggers fallback: every provider would fail.
	ErrCodeInvalidRequest ErrorCode = "invalid_request"
	// ErrCodeAuthentication means the gateway or upstream credentials failed.
	ErrCodeAuthentication ErrorCode = "authentication_error"
	// ErrCodePermission means the credentials are valid but lack entitlement.
	ErrCodePermission ErrorCode = "permission_error"
	// ErrCodeNotFound means the requested model or route does not exist.
	ErrCodeNotFound ErrorCode = "not_found"
	// ErrCodeRateLimited is a recoverable upstream throttle.
	ErrCodeRateLimited ErrorCode = "rate_limited"
	// ErrCodeQuotaExceeded is a hard upstream or internal budget block.
	ErrCodeQuotaExceeded ErrorCode = "quota_exceeded"
	// ErrCodeTimeout covers connect, header and stream-idle timeouts.
	ErrCodeTimeout ErrorCode = "timeout"
	// ErrCodeUpstream covers 5xx and malformed upstream responses.
	ErrCodeUpstream ErrorCode = "upstream_error"
	// ErrCodeContextLength means the prompt exceeded the model window.
	ErrCodeContextLength ErrorCode = "context_length_exceeded"
	// ErrCodeContentFiltered means the provider refused the content.
	ErrCodeContentFiltered ErrorCode = "content_filtered"
	// ErrCodeUnavailable means the provider is known to be unhealthy.
	ErrCodeUnavailable ErrorCode = "provider_unavailable"
	// ErrCodeCanceled means the client disconnected mid-request.
	ErrCodeCanceled ErrorCode = "request_canceled"
	// ErrCodeInternal is an unexpected gateway fault.
	ErrCodeInternal ErrorCode = "internal_error"
	// ErrCodeNotImplemented marks surface area that is planned but absent.
	ErrCodeNotImplemented ErrorCode = "not_implemented"
)

// Error is CoreRouter's normalized error. It carries enough context to make
// fallback decisions, populate audit records and render an OpenAI-compatible
// error body without the caller inspecting provider payloads.
type Error struct {
	Code    ErrorCode
	Message string
	// Provider and Model identify the attempt that failed, when applicable.
	Provider string
	Model    string
	// Status is the HTTP status observed upstream, if any.
	Status int
	// Retryable indicates the same provider may succeed on a later attempt.
	Retryable bool
	// FallbackEligible indicates another provider may succeed where this failed.
	FallbackEligible bool
	// Param mirrors the OpenAI error "param" field.
	Param string
	// Type is the OpenAI error "type" field, defaulted per code.
	Type string
	// Attempt is the 1-based attempt index that produced the error.
	Attempt int
	// Cause is the underlying error, preserved for logs via errors.Unwrap.
	Cause error
}

// Error implements the error interface.
func (e *Error) Error() string {
	base := fmt.Sprintf("%s: %s", e.Code, e.Message)
	if e.Provider != "" {
		base = fmt.Sprintf("%s (provider=%s model=%s)", base, e.Provider, e.Model)
	}
	if e.Attempt > 0 {
		base = fmt.Sprintf("%s (attempt=%d)", base, e.Attempt)
	}
	return base
}

// Unwrap exposes the underlying cause to errors.Is / errors.As.
func (e *Error) Unwrap() error { return e.Cause }

// HTTPStatus maps the normalized code to the status CoreRouter returns.
// Upstream 4xx codes are passed through when they are informative, because
// clients are written against OpenAI's status conventions.
func (e *Error) HTTPStatus() int {
	if e.Status >= 400 && e.Status < 600 {
		switch e.Status {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound,
			http.StatusTooManyRequests, http.StatusBadRequest, http.StatusRequestTimeout,
			http.StatusUnprocessableEntity, http.StatusServiceUnavailable,
			http.StatusConflict:
			return e.Status
		}
	}
	switch e.Code {
	case ErrCodeInvalidRequest, ErrCodeContextLength:
		return http.StatusBadRequest
	case ErrCodeAuthentication:
		return http.StatusUnauthorized
	case ErrCodePermission:
		return http.StatusForbidden
	case ErrCodeNotFound:
		return http.StatusNotFound
	case ErrCodeRateLimited:
		return http.StatusTooManyRequests
	case ErrCodeQuotaExceeded:
		return http.StatusPaymentRequired
	case ErrCodeTimeout:
		return http.StatusGatewayTimeout
	case ErrCodeCanceled:
		// 499 is nginx's client-closed-request code and is widely understood.
		return 499
	case ErrCodeUpstream, ErrCodeUnavailable:
		return http.StatusBadGateway
	case ErrCodeContentFiltered:
		return http.StatusBadRequest
	case ErrCodeNotImplemented:
		return http.StatusNotImplemented
	default:
		return http.StatusInternalServerError
	}
}

// ErrorType returns the OpenAI-compatible "type" discriminator.
func (e *Error) ErrorType() string {
	if e.Type != "" {
		return e.Type
	}
	switch e.Code {
	case ErrCodeInvalidRequest, ErrCodeContextLength:
		return "invalid_request_error"
	case ErrCodeAuthentication:
		return "authentication_error"
	case ErrCodePermission:
		return "permission_error"
	case ErrCodeNotFound:
		return "not_found_error"
	case ErrCodeRateLimited:
		return "rate_limit_error"
	case ErrCodeQuotaExceeded:
		return "insufficient_quota"
	case ErrCodeTimeout:
		return "timeout_error"
	case ErrCodeUpstream, ErrCodeUnavailable:
		return "upstream_error"
	case ErrCodeContentFiltered:
		return "content_filter_error"
	default:
		return "server_error"
	}
}

// ErrorParam returns the OpenAI-compatible "param" discriminator.
func (e *Error) ErrorParam() string { return e.Param }

// NewError constructs a normalized error and applies code-appropriate defaults
// for retryability and fallback eligibility.
func NewError(code ErrorCode, message string) *Error {
	e := &Error{Code: code, Message: message}
	e.applyDefaults()
	return e
}

// Errorf constructs a normalized error with a formatted message.
func Errorf(code ErrorCode, format string, args ...any) *Error {
	return NewError(code, fmt.Sprintf(format, args...))
}

// Wrap attaches a cause to a normalized error.
func (e *Error) Wrap(cause error) *Error {
	e.Cause = cause
	return e
}

// WithProvider annotates the error with the failing attempt.
func (e *Error) WithProvider(provider, model string, attempt int) *Error {
	e.Provider = provider
	e.Model = model
	e.Attempt = attempt
	return e
}

// WithStatus records the upstream HTTP status.
func (e *Error) WithStatus(status int) *Error {
	e.Status = status
	return e
}

func (e *Error) applyDefaults() {
	switch e.Code {
	case ErrCodeRateLimited, ErrCodeTimeout, ErrCodeUpstream, ErrCodeUnavailable:
		e.Retryable = true
		e.FallbackEligible = true
	case ErrCodeAuthentication, ErrCodePermission:
		// Credentials are wrong for this provider, but a sibling provider with
		// different credentials is a legitimate fallback.
		e.Retryable = false
		e.FallbackEligible = true
	case ErrCodeQuotaExceeded:
		e.Retryable = false
		e.FallbackEligible = true
	case ErrCodeContextLength:
		// A model with a larger window can legitimately serve the request.
		e.Retryable = false
		e.FallbackEligible = true
	case ErrCodeInvalidRequest, ErrCodeNotFound, ErrCodeCanceled, ErrCodeContentFiltered:
		e.Retryable = false
		e.FallbackEligible = false
	case ErrCodeInternal, ErrCodeNotImplemented:
		e.Retryable = false
		e.FallbackEligible = false
	}
}

// AsError extracts a normalized *Error from err, synthesizing one when err is
// nil, foreign or already wrapped. Callers can therefore rely on every failure
// path producing a fully populated normalized error.
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	// Unknown errors are treated as internal faults but keep their message.
	return NewError(ErrCodeInternal, err.Error()).Wrap(err)
}

// IsRetryable reports whether err is a normalized error that may succeed on
// retry against the same provider.
func IsRetryable(err error) bool {
	e := AsError(err)
	return e != nil && e.Retryable
}

// IsFallbackEligible reports whether err permits trying a different provider.
func IsFallbackEligible(err error) bool {
	e := AsError(err)
	return e != nil && e.FallbackEligible
}
