// Package postgres is the saga store over the schema workflow_register_payment of the single PostgreSQL instance.
package postgres

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/code-corhuila/multi-tour-workflow-register-payment/internal/application/port/out"
	"github.com/code-corhuila/multi-tour-workflow-register-payment/internal/domain/saga"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var _ out.Store = (*Store)(nil)

// Store keeps only what the tables hold: ids, an amount, a concept, status, steps and a sanitized failure; no credential
// and no personal data of the customer. Ids go in as text and are cast in SQL, so the driver never parses them. The
// approver of the payment is the person who started the saga (the commercial contract takes it from the person the saga acts
// for), so it has no column of its own: it is read back from on_behalf_of.
type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

const (
	sagaTable = "workflow_register_payment.register_payment_saga"
	stepTable = "workflow_register_payment.register_payment_saga_step"

	sagaColumns = `saga_id::text, idempotency_key, saga_status, coalesce(tenant_id::text, ''),
		coalesce(failed_step, ''), coalesce(failure_detail, ''), created_at, updated_at, coalesce(on_behalf_of, ''),
		coalesce(reservation_id::text, ''), coalesce(payment_id::text, ''), coalesce(payment_medium, ''), coalesce(amount_cents, 0),
		coalesce(business_date::text, ''), coalesce(concept, ''), coalesce(cash_movement_id::text, ''), version, coalesce(reason, '')`
)

// holdsPayment is saga.Saga.HoldsPayment in SQL: running, completed, compensated, or failed after the income may exist.
const holdsPayment = `(saga_status IN ('RUNNING', 'COMPLETED', 'COMPENSATED') OR (saga_status = 'FAILED' AND failed_step <> 'RECORD_CASH_INCOME'))`

var uuidShape = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Create inserts the saga as RUNNING. When the idempotency key exists, the insert does nothing and the existing saga is
// returned with created=false; the engine makes two concurrent callers with one key take turns, so only one inserts.
func (s *Store) Create(ctx context.Context, sg saga.Saga) (saga.Saga, bool, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return saga.Saga{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Starts over one payment take turns, so two keys racing for the same payment cannot both pass the check below.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text, 0))`, sg.TenantID+":"+sg.Payment.PaymentID); err != nil {
		return saga.Saga{}, false, err
	}
	var taken bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+sagaTable+` WHERE tenant_id = $1::text::uuid AND payment_id = $2::text::uuid
			AND idempotency_key <> $3 AND `+holdsPayment+`)`, sg.TenantID, sg.Payment.PaymentID, sg.IdempotencyKey).Scan(&taken); err != nil {
		return saga.Saga{}, false, err
	}
	if taken {
		return saga.Saga{}, false, saga.ErrPaymentTaken
	}
	tag, err := tx.Exec(ctx, `INSERT INTO `+sagaTable+` (saga_id, idempotency_key, saga_status, tenant_id, created_at, updated_at,
			on_behalf_of, reservation_id, payment_id, payment_medium, amount_cents, business_date, concept, reason)
		VALUES ($1::text::uuid, $2, 'RUNNING', $3::text::uuid, $4, $5, $6, $7::text::uuid, $8::text::uuid, $9, $10, $11::text::date, $12, nullif($13::text, ''))
		ON CONFLICT ON CONSTRAINT uq_register_payment_saga_idempotency_key DO NOTHING`,
		sg.ID, sg.IdempotencyKey, sg.TenantID, sg.CreatedAt, sg.UpdatedAt, sg.OnBehalfOf, sg.Payment.ReservationID,
		sg.Payment.PaymentID, string(sg.Payment.Medium), sg.Payment.AmountCents, sg.Payment.BusinessDate, sg.Payment.Concept, sg.Payment.Reason)
	if err != nil {
		return saga.Saga{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return saga.Saga{}, false, err
	}
	if tag.RowsAffected() == 1 {
		sg.Status, sg.Steps = saga.Running, nil
		return sg, true, nil
	}
	existing, err := s.load(ctx, `WHERE idempotency_key = $1`, sg.IdempotencyKey)
	if err != nil || len(existing) == 0 {
		return saga.Saga{}, false, errors.Join(err, errors.New("the saga of the idempotency key vanished"))
	}
	return existing[0], false, nil
}

// ForPayment returns the saga of the tenant that holds the payment: the SQL form of saga.Saga.HoldsPayment.
func (s *Store) ForPayment(ctx context.Context, tenantID, paymentID string) (saga.Saga, bool, error) {
	if !uuidShape.MatchString(tenantID) || !uuidShape.MatchString(paymentID) {
		return saga.Saga{}, false, nil
	}
	found, err := s.load(ctx, `WHERE tenant_id = $1::text::uuid AND payment_id = $2::text::uuid AND `+holdsPayment+`
		ORDER BY created_at LIMIT 1`, tenantID, paymentID)
	if err != nil || len(found) == 0 {
		return saga.Saga{}, false, err
	}
	return found[0], true, nil
}

// Save writes the status, the movement, the failure and the steps in one transaction. Only a RUNNING saga is written, and
// only from the copy that is still the stored one (compare and swap on the version): a saga that ended is never overwritten
// (saga.ErrEnded) and one moved by another actor is refused (saga.ErrConflict).
func (s *Store) Save(ctx context.Context, sg *saga.Saga) error {
	if !uuidShape.MatchString(sg.ID) {
		return saga.ErrNotFound
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `UPDATE `+sagaTable+` SET saga_status = $2, failed_step = nullif($3::text, ''),
		failure_detail = nullif($4::text, ''), updated_at = $5, cash_movement_id = nullif($6::text, '')::uuid, version = version + 1
		WHERE saga_id = $1::text::uuid AND saga_status = 'RUNNING' AND version = $7`,
		sg.ID, string(sg.Status), string(sg.FailedStep), sg.FailureDetail, sg.UpdatedAt, sg.CashMovementID, sg.Version)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		var status string
		err := tx.QueryRow(ctx, `SELECT saga_status FROM `+sagaTable+` WHERE saga_id = $1::text::uuid`, sg.ID).Scan(&status)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return saga.ErrNotFound
		case err != nil:
			return err
		case status != string(saga.Running):
			return saga.ErrEnded
		}
		return saga.ErrConflict
	}
	batch := &pgx.Batch{}
	for i, st := range sg.Steps {
		status := "COMPLETED"
		if st.Compensated {
			status = "COMPENSATED"
		}
		batch.Queue(`INSERT INTO `+stepTable+` (saga_id, step_number, step_name, step_status, compensated_at)
			VALUES ($1::text::uuid, $2, $3, $4::text, CASE WHEN $4::text = 'COMPENSATED' THEN now() END)
			ON CONFLICT ON CONSTRAINT pk_register_payment_saga_step DO UPDATE SET step_name = EXCLUDED.step_name,
				step_status = EXCLUDED.step_status,
				compensated_at = CASE WHEN EXCLUDED.step_status = 'COMPENSATED'
					THEN coalesce(`+stepTable+`.compensated_at, EXCLUDED.compensated_at) END`,
			sg.ID, int16(i+1), string(st.Name), status)
	}
	if err := tx.SendBatch(ctx, batch).Close(); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	sg.Version++
	return nil
}

func (s *Store) Get(ctx context.Context, id string) (saga.Saga, error) {
	if !uuidShape.MatchString(id) {
		return saga.Saga{}, saga.ErrNotFound
	}
	found, err := s.load(ctx, `WHERE saga_id = $1::text::uuid`, id)
	if err != nil {
		return saga.Saga{}, err
	}
	if len(found) == 0 {
		return saga.Saga{}, saga.ErrNotFound
	}
	return found[0], nil
}

// ClaimStale takes the RUNNING sagas whose updated_at is before the cutoff in one statement: it stamps them with now and
// raises their version, and rows another caller holds locked are skipped, so two callers never take the same saga.
func (s *Store) ClaimStale(ctx context.Context, before, now time.Time) ([]saga.Saga, error) {
	rows, err := s.pool.Query(ctx, `WITH picked AS (
			SELECT saga_id FROM `+sagaTable+` WHERE saga_status = 'RUNNING' AND updated_at < $1
			ORDER BY updated_at, saga_id FOR UPDATE SKIP LOCKED)
		UPDATE `+sagaTable+` t SET updated_at = $2, version = t.version + 1
		FROM picked WHERE t.saga_id = picked.saga_id RETURNING t.saga_id::text`, before, now)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	return s.load(ctx, `WHERE saga_id IN (SELECT unnest($1::text[])::uuid) ORDER BY updated_at, saga_id`, ids)
}

// load reads the sagas the WHERE clause selects and then their steps in step order.
func (s *Store) load(ctx context.Context, where string, args ...any) ([]saga.Saga, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+sagaColumns+` FROM `+sagaTable+` `+where, args...)
	if err != nil {
		return nil, err
	}
	sagas, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (saga.Saga, error) {
		var sg saga.Saga
		var status, failed, medium string
		err := r.Scan(&sg.ID, &sg.IdempotencyKey, &status, &sg.TenantID, &failed, &sg.FailureDetail, &sg.CreatedAt, &sg.UpdatedAt,
			&sg.OnBehalfOf, &sg.Payment.ReservationID, &sg.Payment.PaymentID, &medium, &sg.Payment.AmountCents,
			&sg.Payment.BusinessDate, &sg.Payment.Concept, &sg.CashMovementID, &sg.Version, &sg.Payment.Reason)
		sg.Status, sg.FailedStep, sg.Payment.Medium = saga.Status(status), saga.Step(failed), saga.Medium(medium)
		sg.Payment.ApprovedBy = sg.OnBehalfOf
		return sg, err
	})
	if err != nil || len(sagas) == 0 {
		return nil, err
	}
	index := make(map[string]int, len(sagas))
	ids := make([]string, len(sagas))
	for i, sg := range sagas {
		index[sg.ID], ids[i] = i, sg.ID
	}
	steps, err := s.pool.Query(ctx, `SELECT saga_id::text, step_name, step_status = 'COMPENSATED' FROM `+stepTable+`
		WHERE saga_id IN (SELECT unnest($1::text[])::uuid) ORDER BY saga_id, step_number`, ids)
	if err != nil {
		return nil, err
	}
	defer steps.Close()
	for steps.Next() {
		var id, name string
		var compensated bool
		if err := steps.Scan(&id, &name, &compensated); err != nil {
			return nil, err
		}
		sg := &sagas[index[id]]
		sg.Steps = append(sg.Steps, saga.StepState{Name: saga.Step(name), Compensated: compensated})
	}
	return sagas, steps.Err()
}
