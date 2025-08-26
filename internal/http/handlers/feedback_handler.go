// Feedback HTTP handlers.
//
// This file exposes the REST endpoint for submitting feedback on assistant
// messages:
//   - POST /messages/{id}/feedback  (create feedback)
//
// Handlers in this file are transport-thin: they validate input, delegate to
// application services, and translate domain/service errors into HTTP results.
// Feedback values are constrained to {-1, +1} to represent negative/positive
// reactions respectively.
package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/tbourn/go-chat-backend/internal/services"
)

// LeaveFeedbackRequest is the JSON payload for creating feedback on a message.
//
// Value must be one of:
//   - +1 : positive feedback
//   - -1 : negative feedback
//
// The binding tag enforces the domain constraint at the transport layer.
type LeaveFeedbackRequest struct {
	// Value is the feedback signal: +1 (positive) or -1 (negative).
	Value   int     `json:"value" binding:"required,oneof=-1 1" example:"1"`
	Comment *string `json:"comment,omitempty" example:"Looks good"`
}

// LeaveFeedback godoc
// @ID          leaveFeedback
// @Summary     Leave feedback on a message
// @Description Records positive (+1) or negative (-1) feedback for an assistant message.
// @Tags        Feedback
// @Accept      json
// @Produce     json
//
// @Param       X-User-ID  header  string  false "User ID (demo header)"          example(user123)
// @Param       id         path    string  true  "Message ID (UUID)"              format(uuid) example(fa4dfbe0-c3bf-47bd-b32f-d7de221cf43b)
// @Param       body       body    handlers.LeaveFeedbackRequest true "Feedback payload"
//
// @Success     204  {string} string "No Content"
// @Failure     400  {object} handlers.ErrorResponse "Invalid payload"
// @Failure     403  {object} handlers.ErrorResponse "Not allowed to leave feedback"
// @Failure     404  {object} handlers.ErrorResponse "Message not found"
// @Failure     409  {object} handlers.ErrorResponse "Feedback already exists"
// @Failure     500  {object} handlers.ErrorResponse "Internal server error"
// @Router      /messages/{id}/feedback [post]
func (h *Handlers) LeaveFeedback(c *gin.Context) {
	var req LeaveFeedbackRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, ErrCodeBadRequest, "value must be -1 or 1")
		return
	}

	// Pull user from context → header → demo fallback (implemented in chat_handler.go)
	uid := userID(c)
	messageID := c.Param("id")

	if err := h.fbSvc.Leave(c.Request.Context(), uid, messageID, req.Value); err != nil {
		switch err {
		case services.ErrMessageNotFound:
			fail(c, http.StatusNotFound, ErrCodeNotFound, "message not found")
		case services.ErrInvalidFeedback:
			fail(c, http.StatusBadRequest, ErrCodeBadRequest, "value must be -1 or 1")
		case services.ErrForbiddenFeedback:
			fail(c, http.StatusForbidden, ErrCodeForbidden, "cannot leave feedback on this message")
		case services.ErrDuplicateFeedback:
			fail(c, http.StatusConflict, ErrCodeConflict, "feedback already exists")
		default:
			fail(c, http.StatusInternalServerError, ErrCodeInternal, err.Error())
		}
		return
	}

	noContent(c)
}
