ALTER TABLE workflow_register_payment.register_payment_saga
    DROP CONSTRAINT IF EXISTS chk_register_payment_saga_on_behalf_of,
    DROP CONSTRAINT IF EXISTS chk_register_payment_saga_concept,
    DROP CONSTRAINT IF EXISTS chk_register_payment_saga_amount_cents,
    DROP CONSTRAINT IF EXISTS chk_register_payment_saga_payment_medium,
    DROP COLUMN IF EXISTS cash_movement_id,
    DROP COLUMN IF EXISTS concept,
    DROP COLUMN IF EXISTS business_date,
    DROP COLUMN IF EXISTS amount_cents,
    DROP COLUMN IF EXISTS payment_medium,
    DROP COLUMN IF EXISTS payment_id,
    DROP COLUMN IF EXISTS reservation_id,
    DROP COLUMN IF EXISTS on_behalf_of;
