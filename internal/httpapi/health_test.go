package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestLiveHandler(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/health/live", nil)
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = request

	liveHandler()(context)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if body := strings.TrimSpace(response.Body.String()); body != `{"status":"ok"}` {
		t.Fatalf("body = %q", body)
	}
}

func TestReadyHandlerReturnsUnavailableWithoutLeakingError(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	response := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(response)
	ginContext.Request = request
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	checker := readinessCheckFunc(func(context.Context) error {
		return errors.New("password-authentication-internal-detail")
	})

	readyHandler(checker, logger)(ginContext)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
	if strings.Contains(response.Body.String(), "password") {
		t.Fatalf("response leaked dependency error: %s", response.Body.String())
	}
}
