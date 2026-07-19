package payment

import (
	"context"
	"net/http"

	"github.com/nvawntien/telegram-bot/internal/app"
)

type WebhookVerifier interface {
	VerifyAndNormalize(context.Context, http.Header, []byte) (app.NormalizedPaymentEvent, error)
}

type Registry struct {
	providers map[string]WebhookVerifier
}

func NewRegistry(providers map[string]WebhookVerifier) *Registry {
	copyOfProviders := make(map[string]WebhookVerifier, len(providers))
	for name, provider := range providers {
		if name != "" && provider != nil {
			copyOfProviders[name] = provider
		}
	}
	return &Registry{providers: copyOfProviders}
}

func (r *Registry) Provider(name string) (WebhookVerifier, bool) {
	provider, ok := r.providers[name]
	return provider, ok
}
