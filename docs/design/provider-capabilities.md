# Payment provider capabilities

Payment providers are composed at process startup through an immutable
`payment.Registry`. A provider declares its typed name, enabled state,
environment, and three independent capabilities: webhook, reconciliation, and
test mode. The registry rejects invalid names, invalid environments, duplicate
names, and providers with no useful capability.

`WebhookProvider` verifies/authenticates a raw request, parses only the official
provider DTO, emits `app.NormalizedPaymentEvent`, and returns a static bounded
acknowledgement. `TransactionAPIProvider` fetches a bounded page using an opaque
cursor. A provider implements either interface or both; typed registry lookups
prevent capability assertions from spreading through handlers and workers.

The registry has no global mutable state and stores no credential. Disabled or
unknown providers are rejected before body parsing or API work. Provider names
used as metric labels are therefore bounded by startup registration.

The three supported shapes are:

| Shape | Real-time path | Missing-event recovery | Checkpoint |
|---|---|---|---|
| Webhook only | Yes | Provider webhook retry only | None |
| API only | Poll interval | Yes | Durable per account |
| Webhook + API | Yes | Yes | Durable per account |

`signed_json` is a webhook-only reference adapter for development, tests, and
controlled private integration. Configuration and constructor guards reject it
in the production provider environment.
