-- The optional reason of the approval when the verified amount differs from the one reported (HU-COM-010 DP-11, DP-06). It is
-- kept with the saga so a restart sends it again to step 2. Nullable: a saga without one stores NULL. Free text typed by the
-- reviewer, never personal data of the customer. Guarded, so applying the changeset again does nothing.
ALTER TABLE workflow_register_payment.register_payment_saga
    ADD COLUMN IF NOT EXISTS reason text;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_register_payment_saga_reason') THEN
        ALTER TABLE workflow_register_payment.register_payment_saga
            ADD CONSTRAINT chk_register_payment_saga_reason CHECK (char_length(btrim(reason)) BETWEEN 1 AND 500);
    END IF;
END
$$;
