# Ambiguous Telegram delivery

Transport completion is not equivalent to delivery certainty. A reset,
timeout, malformed success response, response loss after request write, invalid
success evidence, or database finalization failure after Telegram success may
mean the credential reached the customer. These outcomes are conservative:

- set the job to `ambiguous` and clear its automatic schedule/lease;
- retain exact inventory as `reserved` and the order as `delivering`;
- append safe attempt/audit metadata, never the message or credential;
- do not reclaim or resend automatically;
- expose redacted evidence to the admin review queue.

The worker persists `sending` before the request. Therefore a crash after that
boundary is ambiguous even when no result row was written. A crash while merely
`claimed` is safe to retry. When Telegram returns a message ID but finalization
fails, a separate best-effort transaction preserves that evidence. If the
database remains unavailable, stale `sending` recovery still chooses review,
not retry.

The adapter only auto-retries explicit 429/5xx responses and connect failures
known to occur before request write. It cannot prove the outcome of every
network error; preventing duplicate credential delivery takes precedence over
blind retry.
