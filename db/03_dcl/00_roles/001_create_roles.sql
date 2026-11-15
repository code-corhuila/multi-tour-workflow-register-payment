-- NOLOGIN roles with no password. The login user workflow_register_payment_app is created by multi-tour-infra-postgres
-- from a secret; this repository only grants it the writer role (Annex J, J.7).
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'workflow_register_payment_reader') THEN
        CREATE ROLE workflow_register_payment_reader NOLOGIN;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'workflow_register_payment_writer') THEN
        CREATE ROLE workflow_register_payment_writer NOLOGIN;
    END IF;
END
$$;
