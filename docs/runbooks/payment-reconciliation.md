# Payment transaction reconciliation

Reconciliation runs only for enabled registered providers with the transaction
API capability and active same-environment account mappings. Configure a
positive interval, per-run and per-request timeouts, max pages, and page size;
the run timeout must exceed the request timeout.

Each account has one durable checkpoint and a leased owner/version. Provider API
calls occur without a database transaction. A fetched page advances its opaque
cursor only after every normalized transaction is durably inserted or confirmed
as an exact duplicate. Crash before advance rereads the page; ingestion
idempotency prevents duplicate effects. Failure records a bounded error code and
releases the lease, while worker/payment/delivery loops continue.

For investigation, inspect provider health, checkpoint age, last attempt,
success, last transaction time, pending events, and reviews. Do not edit the
cursor to skip a bad page. Resolve the adapter/data problem and rerun within the
configured bounds. A provider auth failure requires credential repair outside
Telegram and Git.
