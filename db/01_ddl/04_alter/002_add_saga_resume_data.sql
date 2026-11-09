-- What a restart needs to carry a saga on (HU-WFL-002): who asked, the payment to record and the cash movement of step 1,
-- which the compensation needs. All nullable. Only ids, amounts and a concept: no name, e-mail or document of the customer.
-- Guarded, so applying the changeset again does nothing.
ALTER TABLE workflow_register_payment.register_payment_saga
    ADD COLUMN IF NOT EXISTS on_behalf_of     text,
    ADD COLUMN IF NOT EXISTS reservation_id   uuid,
    ADD COLUMN IF NOT EXISTS payment_id       uuid,
    ADD COLUMN IF NOT EXISTS payment_medium   text,
    ADD COLUMN IF NOT EXISTS amount_cents     bigint,
    ADD COLUMN IF NOT EXISTS business_date    date,
    ADD COLUMN IF NOT EXISTS concept          text,
    ADD COLUMN IF NOT EXISTS cash_movement_id uuid;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_register_payment_saga_payment_medium') THEN
        ALTER TABLE workflow_register_payment.register_payment_saga
            ADD CONSTRAINT chk_register_payment_saga_payment_medium CHECK (payment_medium IN ('CASH', 'TRANSFER'));
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_register_payment_saga_amount_cents') THEN
        ALTER TABLE workflow_register_payment.register_payment_saga
            ADD CONSTRAINT chk_register_payment_saga_amount_cents CHECK (amount_cents > 0);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_register_payment_saga_on_behalf_of') THEN
        ALTER TABLE workflow_register_payment.register_payment_saga
            ADD CONSTRAINT chk_register_payment_saga_on_behalf_of CHECK (char_length(on_behalf_of) BETWEEN 1 AND 128);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_register_payment_saga_concept') THEN
        ALTER TABLE workflow_register_payment.register_payment_saga
            ADD CONSTRAINT chk_register_payment_saga_concept CHECK (char_length(concept) <= 200);
    END IF;
END
$$;
