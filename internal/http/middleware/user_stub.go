package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// DemoUser injects a development-friendly user identity when upstream
// authentication is not present. It preserves any identity that was already
// established earlier in the chain and falls back to the X-User-ID header
// (used by tests and local tooling) before defaulting to "demo-user".
func DemoUser() gin.HandlerFunc {
	return func(c *gin.Context) {
		if v, ok := c.Get("userID"); ok {
			if s, ok := v.(string); ok && s != "" {
				c.Next()
				return
			}
		}
		uid := strings.TrimSpace(c.GetHeader("X-User-ID"))
		if uid == "" {
			uid = "demo-user"
		}
		c.Set("userID", uid)
		c.Next()
	}
}
