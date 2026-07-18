package httpapi

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
)

type healthResponse struct {
	Status string `json:"status"`
}

func liveHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, healthResponse{Status: "ok"})
	}
}

func readyHandler(checker ReadinessChecker, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := checker.Check(c.Request.Context()); err != nil {
			logger.WarnContext(c.Request.Context(), "readiness check failed", "error", err)
			c.JSON(http.StatusServiceUnavailable, healthResponse{Status: "unavailable"})
			return
		}
		c.JSON(http.StatusOK, healthResponse{Status: "ok"})
	}
}

// readinessCheckFunc helps compose health dependencies without exporting a
// transport-specific implementation.
type readinessCheckFunc func(context.Context) error

func (f readinessCheckFunc) Check(ctx context.Context) error { return f(ctx) }
