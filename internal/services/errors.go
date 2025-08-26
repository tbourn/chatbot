// Package services defines the business logic for chats, messages, and feedback.
// This file centralizes common service-level error values so that they can be
// consistently returned by service methods and checked by callers.
//
// These errors are intended for internal use by the service layer and translation
// into user-facing messages or HTTP status codes should be performed at the
// handler/controller layer.
package services

import "errors"

// Chat-related errors.
var (
	// ErrChatNotFound indicates that the requested chat does not exist or is not
	// accessible to the current user.
	ErrChatNotFound = errors.New("chat not found")

	// ErrEmptyPrompt is returned when a request to create a message contains
	// an empty prompt.
	ErrEmptyPrompt = errors.New("prompt is empty")

	// ErrTooLong is returned when a request to create a message exceeds the
	// maximum configured length limit.
	ErrTooLong = errors.New("prompt too long")

	// ErrInvalidFeedback is returned when a feedback value is outside the
	// allowed set (currently -1 or 1).
	ErrInvalidFeedback = errors.New("feedback value must be -1 or 1")

	// ErrMessageNotFound indicates that the requested message does not exist
	// or is not accessible to the current user.
	ErrMessageNotFound = errors.New("message not found")

	// ErrForbiddenFeedback is returned when a user attempts to leave feedback
	// on a message they are not permitted to rate.
	ErrForbiddenFeedback = errors.New("cannot leave feedback on this message")

	// ErrDuplicateFeedback is returned when a user attempts to leave feedback
	// on a message that they have already rated.
	ErrDuplicateFeedback = errors.New("feedback already exists")
)
