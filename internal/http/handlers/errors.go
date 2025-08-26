// Package handlers defines HTTP-layer error codes used across all API endpoints.
//
// This file centralizes symbolic error code constants that are mapped to HTTP responses
// (via the `fail()` helper in this package). These codes provide clients with a stable,
// machine-readable error taxonomy that supplements human-readable messages.
//
// Conventions:
//   - Codes are lowercase, snake_case, and domain-agnostic unless explicitly noted.
//   - Generic codes (e.g., bad_request, unauthorized, conflict) mirror common HTTP
//     status semantics to aid interoperability.
//   - Domain-specific codes (e.g., answer_failed, create_failed) are reserved for
//     business logic errors that cannot be conveyed by status alone.
//   - All error responses must include both an HTTP status and one of these codes.
//
// Usage:
//   - Handlers select the most specific matching code and pass it to `fail()` along
//     with the corresponding HTTP status and message.
//   - Clients are expected to branch on these codes for programmatic error handling.
//
// Example response:
//   {
//     "request_id": "e1b9be03-4999-4289-9f03-999b042d65d6",
//     "code": "conflict",
//     "message": "feedback already exists"
//   }

package handlers

const (
	ErrCodeBadRequest   = "bad_request"
	ErrCodeUnauthorized = "unauthorized"
	ErrCodeForbidden    = "forbidden"
	ErrCodeNotFound     = "not_found"
	ErrCodeConflict     = "conflict"
	ErrCodeRateLimited  = "too_many_requests"
	ErrCodeInternal     = "internal_error"

	// Domain-specific:
	ErrCodeAnswerFailed     = "answer_failed"
	ErrCodeCreateFailed     = "create_failed"
	ErrCodeListFailed       = "list_failed"
	ErrCodeMethodNotAllowed = "method_not_allowed"
)
