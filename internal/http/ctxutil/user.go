package ctxutil

import "github.com/gin-gonic/gin"

// UserID returns the caller identity stored on the Gin context. Upstream
// authentication middleware is expected to stash it under the "userID" key.
//
// When no identity is available (e.g. in local development), the function
// falls back to the demo user that our middleware injects.
func UserID(c *gin.Context) string {
	if c == nil {
		return "demo-user"
	}
	if v, ok := c.Get("userID"); ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return "demo-user"
}
