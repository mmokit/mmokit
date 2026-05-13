package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zenion/mmoserver/pkg/persist"
)

type adminOperatorRepo struct {
	pool *pgxpool.Pool
}

var _ persist.AdminOperatorRepository = (*adminOperatorRepo)(nil)

const adminOperatorSelectColumns = `username, password_hash, grants, created_at, updated_at`

func (r *adminOperatorRepo) GetByUsername(ctx context.Context, username string) (*persist.AdminOperator, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+adminOperatorSelectColumns+` FROM engine.admin_operators WHERE username = $1`,
		username,
	)
	op, err := scanAdminOperator(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, persist.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("adminOperatorRepo.GetByUsername %q: %w", username, err)
	}
	return op, nil
}

func (r *adminOperatorRepo) Create(ctx context.Context, op *persist.AdminOperator) error {
	grantsJSON, err := marshalGrants(op.Grants)
	if err != nil {
		return fmt.Errorf("adminOperatorRepo.Create marshal grants: %w", err)
	}
	_, err = r.pool.Exec(ctx,
		`INSERT INTO engine.admin_operators (username, password_hash, grants) VALUES ($1, $2, $3::jsonb)`,
		op.Username, op.PasswordHash, grantsJSON,
	)
	if err != nil {
		return fmt.Errorf("adminOperatorRepo.Create %q: %w", op.Username, err)
	}
	return nil
}

func (r *adminOperatorRepo) List(ctx context.Context) ([]*persist.AdminOperator, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+adminOperatorSelectColumns+` FROM engine.admin_operators ORDER BY username`,
	)
	if err != nil {
		return nil, fmt.Errorf("adminOperatorRepo.List: %w", err)
	}
	defer rows.Close()

	var out []*persist.AdminOperator
	for rows.Next() {
		op, err := scanAdminOperator(rows)
		if err != nil {
			return nil, fmt.Errorf("adminOperatorRepo.List scan: %w", err)
		}
		out = append(out, op)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("adminOperatorRepo.List rows: %w", err)
	}
	return out, nil
}

func (r *adminOperatorRepo) Delete(ctx context.Context, username string) error {
	if _, err := r.pool.Exec(ctx,
		`DELETE FROM engine.admin_operators WHERE username = $1`, username,
	); err != nil {
		return fmt.Errorf("adminOperatorRepo.Delete %q: %w", username, err)
	}
	return nil
}

func (r *adminOperatorRepo) UpdatePasswordHash(ctx context.Context, username, hash string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE engine.admin_operators SET password_hash = $1, updated_at = NOW() WHERE username = $2`,
		hash, username,
	)
	if err != nil {
		return fmt.Errorf("adminOperatorRepo.UpdatePasswordHash %q: %w", username, err)
	}
	if tag.RowsAffected() == 0 {
		return persist.ErrNotFound
	}
	return nil
}

func (r *adminOperatorRepo) Count(ctx context.Context) (int, error) {
	var n int
	if err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM engine.admin_operators`).Scan(&n); err != nil {
		return 0, fmt.Errorf("adminOperatorRepo.Count: %w", err)
	}
	return n, nil
}

func scanAdminOperator(scanner interface {
	Scan(dest ...any) error
}) (*persist.AdminOperator, error) {
	var op persist.AdminOperator
	var grantsBytes []byte
	if err := scanner.Scan(
		&op.Username,
		&op.PasswordHash,
		&grantsBytes,
		&op.CreatedAt,
		&op.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if len(grantsBytes) > 0 {
		if err := json.Unmarshal(grantsBytes, &op.Grants); err != nil {
			return nil, fmt.Errorf("unmarshal grants %q: %w", op.Username, err)
		}
	}
	return &op, nil
}

// marshalGrants returns "[]" for nil/empty input so the JSONB column
// always holds a valid array (matching the schema's DEFAULT '[]'::jsonb).
func marshalGrants(grants []string) ([]byte, error) {
	if len(grants) == 0 {
		return []byte(`[]`), nil
	}
	b, err := json.Marshal(grants)
	if err != nil {
		return nil, err
	}
	if len(b) == 0 || string(b) == "null" {
		return []byte(`[]`), nil
	}
	return b, nil
}
