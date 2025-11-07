package ctxutil

import (
	"strconv"

	"github.com/gin-gonic/gin"
)

// PaginationConfig defines sane defaults and limits for paginated endpoints.
type PaginationConfig struct {
	DefaultPage     int
	DefaultPageSize int
	MaxPageSize     int
}

// PaginationParams reads page/page_size query params, applies defaults and
// bounds, and returns the sanitized values.
func PaginationParams(c *gin.Context, cfg PaginationConfig) (page, pageSize int) {
	if cfg.DefaultPage <= 0 {
		cfg.DefaultPage = 1
	}
	if cfg.DefaultPageSize <= 0 {
		cfg.DefaultPageSize = 20
	}
	if cfg.MaxPageSize <= 0 {
		cfg.MaxPageSize = 100
	}
	page = cfg.DefaultPage
	pageSize = cfg.DefaultPageSize
	if c != nil {
		if p := c.Query("page"); p != "" {
			if v, err := strconv.Atoi(p); err == nil && v > 0 {
				page = v
			}
		}
		if ps := c.Query("page_size"); ps != "" {
			if v, err := strconv.Atoi(ps); err == nil && v > 0 {
				pageSize = v
			}
		}
	}
	if pageSize > cfg.MaxPageSize {
		pageSize = cfg.MaxPageSize
	}
	return
}
