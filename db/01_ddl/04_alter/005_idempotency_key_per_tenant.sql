-- The Idempotency-Key of a start is unique per tenant, not across tenants (HU-WFL-002 AC-WF2-05 amended, DD-23): a key that
-- another tenant used must neither be refused nor reveal that saga. NULLS NOT DISTINCT keeps a row without tenant from
-- duplicating a key too. Guarded, so applying the changeset again does nothing.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'uq_register_payment_saga_tenant_idempotency_key') THEN
        ALTER TABLE workflow_register_payment.register_payment_saga
            ADD CONSTRAINT uq_register_payment_saga_tenant_idempotency_key UNIQUE NULLS NOT DISTINCT (tenant_id, idempotency_key);
    END IF;
END
$$;

ALTER TABLE workflow_register_payment.register_payment_saga
    DROP CONSTRAINT IF EXISTS uq_register_payment_saga_idempotency_key;
