# Add a payment provider

Do not start implementation until the provider supplies official documentation
covering all applicable items:

1. Authentication contract and credential lifecycle.
2. Webhook payload and content type.
3. Required webhook acknowledgement.
4. Webhook retry and duplicate semantics.
5. Stable event ID.
6. Stable transaction ID.
7. Explicit transfer direction.
8. Integer amount and currency representation.
9. Transfer-content field.
10. Exact destination-account identity.
11. Timestamp format and timezone.
12. Transaction API endpoint and authentication, if supported.
13. Pagination/cursor stability and ordering, if supported.
14. Rate limits, retry-after, temporary errors, and permanent auth errors.
15. Sandbox/test mode and production endpoint security.
16. Adapter implementation for only documented capabilities.
17. Contract fixtures and authentication/acknowledgement/pagination tests.
18. Explicit same-environment provider-account mapping.
19. Staging webhook/API, duplicate, outage, and missed-webhook tests.
20. Reviewed production enablement, metrics, alerting, and rollback plan.

Keep credentials in the adapter's environment namespace or secret manager, not
PostgreSQL, Telegram sessions, fixtures, logs, or Git. Register a fully validated
adapter at composition roots; never partially expose a misconfigured adapter.
