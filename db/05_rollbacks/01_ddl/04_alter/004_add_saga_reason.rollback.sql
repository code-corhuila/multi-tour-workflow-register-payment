ALTER TABLE workflow_register_payment.register_payment_saga
    DROP CONSTRAINT IF EXISTS chk_register_payment_saga_reason,
    DROP COLUMN IF EXISTS reason;
