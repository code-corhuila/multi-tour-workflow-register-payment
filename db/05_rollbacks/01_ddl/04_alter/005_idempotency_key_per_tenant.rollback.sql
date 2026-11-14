-- Back to one key across all tenants. It fails, and changes nothing, if two tenants already hold the same key: those
-- sagas must be looked at by a person before the rollback.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'uq_register_payment_saga_idempotency_key') THEN
        ALTER TABLE workflow_register_payment.register_payment_saga
            ADD CONSTRAINT uq_register_payment_saga_idempotency_key UNIQUE (idempotency_key);
    END IF;
END
$$;

ALTER TABLE workflow_register_payment.register_payment_saga
    DROP CONSTRAINT IF EXISTS uq_register_payment_saga_tenant_idempotency_key;
