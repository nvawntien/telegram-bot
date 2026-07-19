# Provider webhook recovery

1. Check provider health for enabled state, environment, last webhook, pending
   events, reviews, and redacted error code.
2. Confirm the provider route has the webhook capability. An API-only provider
   intentionally returns not found on the webhook route.
3. Check ingress status and provider delivery/retry records without copying raw
   payload, signature, account number, or secret into logs or tickets.
4. If the provider also supports reconciliation, verify its mapping is active,
   enable the bounded worker, and observe checkpoint attempt/success and lag.
5. Never insert a synthetic successful event or advance a checkpoint manually.
6. Keep conflicts, unmatched references, wrong destinations, and late payments
   in review. Use manual confirmation only after independent evidence and the
   audited existing fallback.

A webhook-only provider has no generic missed-event recovery beyond the
provider's own retry contract. Restore ingress quickly and request a documented
provider replay if available.
