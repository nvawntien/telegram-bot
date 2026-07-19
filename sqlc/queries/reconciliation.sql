-- name: GetFinancialReconciliation :one
SELECT
    (SELECT count(*)::bigint
     FROM payment_events AS event
     WHERE event.processing_status = 'completed'
       AND event.provider_transaction_id IS NOT NULL
       AND NOT EXISTS (
           SELECT 1 FROM payments AS payment
           WHERE payment.provider = event.provider
             AND payment.provider_transaction_id = event.provider_transaction_id
       )) AS completed_events_without_payment,
    (SELECT count(*)::bigint
     FROM payments AS payment
     WHERE payment.status = 'confirmed'
       AND NOT EXISTS (SELECT 1 FROM payment_allocations AS allocation WHERE allocation.payment_id = payment.id)
       AND NOT EXISTS (SELECT 1 FROM payment_review_cases AS review WHERE review.payment_id = payment.id AND review.status IN ('open', 'held'))
    ) AS confirmed_payments_without_outcome,
    (SELECT count(*)::bigint
     FROM wallet_accounts AS wallet
     WHERE wallet.balance_vnd <> (
         SELECT COALESCE(sum(ledger.amount_vnd), 0)::bigint
         FROM wallet_ledger_entries AS ledger WHERE ledger.account_id = wallet.id
     )) AS wallet_balance_mismatches,
    (SELECT count(*)::bigint
     FROM orders AS order_row
     JOIN LATERAL (
         SELECT COALESCE(sum(item.quantity), 0)::bigint AS expected
         FROM order_items AS item WHERE item.order_id = order_row.id
     ) AS expected ON true
     WHERE order_row.status = 'reserving'
       AND expected.expected <> (
           SELECT count(*)::bigint FROM order_inventory_items AS mapping
           WHERE mapping.order_id = order_row.id AND mapping.status = 'active'
       )) AS reservation_count_mismatches,
    (SELECT count(*)::bigint FROM payment_review_cases WHERE btrim(reason) = '') AS review_cases_without_reason;
