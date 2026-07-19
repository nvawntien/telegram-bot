# Delivery state machine

Delivery job, attempt, order, and inventory states remain separate.

```text
pending -> processing(claimed) -> processing(sending) -> completed
   ^              |                       |
   |              +-> retryable_failed <--+ definite temporary failure
   |                                      +-> ambiguous
   +------ audited manual retry -----------+-> permanent_failed
                                          +-> manual_review
```

`completed`, `cancelled`, and `permanent_failed` are terminal for automatic
workers. `ambiguous` and `manual_review` require an operator. Manual verified
not-delivered resolution moves the same durable job to `pending`; verified
delivered resolution moves it to `completed` without a Telegram call.

An attempt first appends `started`. The result appends `succeeded`,
`retryable_failed`, `ambiguous`, `permanent_failed`, or `manual_resolution`.
Attempt rows cannot be updated or deleted. Order remains `delivering` during
retry/review, becomes `delivery_failed` on permanent/exhausted failure, and
becomes `delivered` only with trusted message evidence and exact inventory sale.
