GRANT USAGE ON SCHEMA workflow_register_payment TO workflow_register_payment_reader, workflow_register_payment_writer;
GRANT SELECT ON ALL TABLES IN SCHEMA workflow_register_payment TO workflow_register_payment_reader;
-- No DELETE: a saga is never removed by the service, the state is the audit of the process
GRANT SELECT, INSERT, UPDATE ON ALL TABLES IN SCHEMA workflow_register_payment TO workflow_register_payment_writer;
ALTER DEFAULT PRIVILEGES IN SCHEMA workflow_register_payment GRANT SELECT ON TABLES TO workflow_register_payment_reader;
ALTER DEFAULT PRIVILEGES IN SCHEMA workflow_register_payment GRANT SELECT, INSERT, UPDATE ON TABLES TO workflow_register_payment_writer;
GRANT workflow_register_payment_writer TO workflow_register_payment_app;
