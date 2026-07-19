# Automatic bank payment confirmation

VietQR creates a transfer instruction only. Generating, opening, or scanning a
QR is never payment evidence. Automatic confirmation begins only with a
verified provider webhook or a transaction fetched from a supported provider
API.

```text
provider transport
  -> capability adapter
  -> normalized event
  -> durable payment_events row
  -> payment event worker
  -> strict reference and destination mapping
  -> existing PaymentAcceptanceService
  -> payment/allocation + exact inventory claim + delivery job
  -> Telegram delivery worker
```

Only inbound VND transactions can reach acceptance. Outbound rows are retained
and terminally ignored. Transfer content is scanned with finite tokenization:
the configured uppercase prefix plus the exact configured hexadecimal suffix,
clear Unicode-aware boundaries, a bounded input length, and no fuzzy repair,
amount-only lookup, sender-name lookup, or substring matching. No reference,
malformed reference, or multiple candidates becomes review.

Auto-acceptance also requires an active explicit mapping from exact provider
destination identity and environment to the same local bank account snapshotted
by the order/top-up. Test and production events, banks, mappings, orders,
payments, and top-ups cannot cross environments.

The acceptance transaction rechecks exact reference, amount, currency, target
state, destination bank, and provider transaction uniqueness. It reuses the
existing wallet ledger, inventory claim, allocation, history, audit, and
delivery handoff. Admin confirmation remains a fallback, not part of the happy
path.
