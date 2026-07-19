# Payment provider adapter boundary

An adapter owns transport facts only: official authentication, raw payload
verification, provider DTO parsing, field normalization, provider acknowledgement,
API communication, pagination, rate-limit classification, and sanitized errors.
It must not query or mutate orders, wallets, inventory, delivery, allocations,
or review cases.

Every normalized transaction contains provider and transaction identifiers,
environment, event type, inbound/outbound direction, integer amount, currency,
transfer content, exact destination identity, optional provider-account
identity, occurrence time, source, payload hash, business fingerprint, and
sanitized JSON metadata. Signatures, API tokens, raw responses, and banking
credentials are never persisted.

The generic webhook endpoint preserves the raw body once, applies a size limit,
uses a typed webhook lookup, delegates verification, durably ingests, validates
the adapter's bounded 2xx acknowledgement, then writes only allowlisted headers
and body. Provider input is never reflected in the acknowledgement.

The generic API worker lists transactions outside database transactions. It
forces the registered provider/environment/account context onto normalized
events and sends each through the same ingestion service as webhook traffic.
Checkpoint advancement happens only after every transaction in the page is
inserted or confirmed as an exact duplicate.

No commercial provider adapter is included because no official provider
contract was supplied. The core contains no guessed header, payload, signature,
pagination, endpoint, or acknowledgement contract.
