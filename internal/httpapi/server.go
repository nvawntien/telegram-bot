// Package httpapi provides the API process HTTP transport and lifecycle.
package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nvawntien/telegram-bot/internal/observability"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const maxRequestIDLength = 128

// ReadinessChecker represents a required dependency checked by /health/ready.
type ReadinessChecker interface {
	Check(context.Context) error
}

// ServerConfig defines HTTP listener and timeout policy.
type ServerConfig struct {
	Address           string
	Environment       string
	PrometheusEnabled bool
}

// Server owns one http.Server and its routes.
type Server struct {
	httpServer *http.Server
	logger     *slog.Logger
}

// NewServer builds a server without opening a listener.
func NewServer(
	cfg ServerConfig,
	checker ReadinessChecker,
	metrics *observability.HTTPMetrics,
	gatherer prometheus.Gatherer,
	logger *slog.Logger,
) *Server {
	mode := gin.ReleaseMode
	if cfg.Environment == "test" {
		mode = gin.TestMode
	}
	gin.SetMode(mode)
	router := gin.New()
	router.Use(
		requestIDMiddleware(),
		requestLogMiddleware(logger),
		metricsMiddleware(metrics),
		recoverMiddleware(logger),
	)
	router.GET("/health/live", liveHandler())
	router.GET("/health/ready", readyHandler(checker, logger))
	if cfg.PrometheusEnabled {
		router.GET("/metrics", gin.WrapH(promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{})))
	}
	router.NoRoute(func(c *gin.Context) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
	})
	router.NoMethod(func(c *gin.Context) {
		c.JSON(http.StatusMethodNotAllowed, gin.H{"error": "method not allowed"})
	})
	router.HandleMethodNotAllowed = true

	return &Server{
		logger: logger,
		httpServer: &http.Server{
			Addr:              cfg.Address,
			Handler:           router,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       60 * time.Second,
			MaxHeaderBytes:    1 << 20,
		},
	}
}

// Run blocks until the server stops. Graceful shutdown is a normal exit.
func (s *Server) Run() error {
	s.logger.Info("HTTP server started", "address", s.httpServer.Addr)
	err := s.httpServer.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown drains active requests until ctx expires.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

type requestIDKey struct{}

func requestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := c.GetHeader("X-Request-ID")
		if requestID == "" || len(requestID) > maxRequestIDLength {
			requestID = newRequestID()
		}
		c.Header("X-Request-ID", requestID)
		c.Set("request_id", requestID)
		ctx := context.WithValue(c.Request.Context(), requestIDKey{}, requestID)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

func requestLogMiddleware(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		started := time.Now()
		c.Next()
		logger.InfoContext(c.Request.Context(), "HTTP request completed",
			"request_id", requestIDFromContext(c.Request.Context()),
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"route", routeLabel(c),
			"status", c.Writer.Status(),
			"duration_ms", time.Since(started).Milliseconds(),
		)
	}
}

func recoverMiddleware(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.ErrorContext(c.Request.Context(), "panic recovered",
					"request_id", requestIDFromContext(c.Request.Context()),
					"error", recovered,
					"stack", string(debug.Stack()),
				)
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
			}
		}()
		c.Next()
	}
}

func metricsMiddleware(metrics *observability.HTTPMetrics) gin.HandlerFunc {
	return func(c *gin.Context) {
		started := time.Now()
		c.Next()
		metrics.Observe(c.Request.Method, routeLabel(c), c.Writer.Status(), time.Since(started))
	}
}

func routeLabel(c *gin.Context) string {
	if route := c.FullPath(); route != "" {
		return route
	}
	return "unmatched"
}

func requestIDFromContext(ctx context.Context) string {
	requestID, _ := ctx.Value(requestIDKey{}).(string)
	return requestID
}

func newRequestID() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "request-id-unavailable"
	}
	return hex.EncodeToString(bytes)
}
