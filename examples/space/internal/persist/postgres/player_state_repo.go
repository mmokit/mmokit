package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	gamepersist "github.com/zenion/mmokit/examples/space/internal/persist"
)

type playerStateRepo struct {
	pool *pgxpool.Pool
}

var _ gamepersist.PlayerStateRepository = (*playerStateRepo)(nil)

const playerStateSelectColumns = `user_id, currencies, cargo, bank, equipment`

// Load fetches one player's space-game state by user_id.
func (r *playerStateRepo) Load(ctx context.Context, userID uuid.UUID) (*gamepersist.PlayerStateSnapshot, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+playerStateSelectColumns+` FROM space.player_state WHERE user_id = $1`,
		userID,
	)
	snap, err := scanPlayerState(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, gamepersist.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("playerStateRepo.Load %s: %w", userID, err)
	}
	return snap, nil
}

// LoadAll streams every player_state row. Iteration order is
// unspecified (no ORDER BY) — the caller is the in-memory cache, which
// just builds a map keyed by user_id.
func (r *playerStateRepo) LoadAll(ctx context.Context, fn func(*gamepersist.PlayerStateSnapshot) error) error {
	rows, err := r.pool.Query(ctx, `SELECT `+playerStateSelectColumns+` FROM space.player_state`)
	if err != nil {
		return fmt.Errorf("playerStateRepo.LoadAll query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		snap, err := scanPlayerState(rows)
		if err != nil {
			return fmt.Errorf("playerStateRepo.LoadAll scan: %w", err)
		}
		if err := fn(snap); err != nil {
			return err
		}
	}
	return rows.Err()
}

// SaveBatchTx upserts every snapshot inside the caller-supplied
// transaction. Same sort-by-UserID contract as SaveBatch — callers
// must pre-sort. Used by the game-side PlayerFlusher to write engine
// identity and game state atomically under one pgx.Tx.
func (r *playerStateRepo) SaveBatchTx(ctx context.Context, tx pgx.Tx, snapshots []*gamepersist.PlayerStateSnapshot) error {
	if len(snapshots) == 0 {
		return nil
	}
	for i := 1; i < len(snapshots); i++ {
		if snapshots[i-1].UserID.String() > snapshots[i].UserID.String() {
			return errors.New("playerStateRepo.SaveBatchTx: snapshots not sorted by user_id (deadlock prevention contract)")
		}
	}

	batch := &pgx.Batch{}
	for _, s := range snapshots {
		currenciesJSON, err := marshalJSONOrEmptyObject(s.Currencies)
		if err != nil {
			return fmt.Errorf("marshal currencies %s: %w", s.UserID, err)
		}
		cargoJSON, err := marshalJSONOrEmptyObject(s.Cargo)
		if err != nil {
			return fmt.Errorf("marshal cargo %s: %w", s.UserID, err)
		}
		bankJSON, err := marshalJSONOrEmptyObject(s.Bank)
		if err != nil {
			return fmt.Errorf("marshal bank %s: %w", s.UserID, err)
		}
		equipmentJSON, err := json.Marshal(s.Equipment)
		if err != nil {
			return fmt.Errorf("marshal equipment %s: %w", s.UserID, err)
		}
		batch.Queue(`
			INSERT INTO space.player_state (
				user_id, currencies, cargo, bank, equipment, updated_at
			)
			VALUES ($1, $2, $3, $4, $5, NOW())
			ON CONFLICT (user_id) DO UPDATE SET
				currencies = EXCLUDED.currencies,
				cargo      = EXCLUDED.cargo,
				bank       = EXCLUDED.bank,
				equipment  = EXCLUDED.equipment,
				updated_at = NOW()
		`,
			s.UserID, currenciesJSON, cargoJSON, bankJSON, equipmentJSON,
		)
	}

	br := tx.SendBatch(ctx, batch)
	defer br.Close()

	for i := range len(snapshots) {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("playerStateRepo.SaveBatchTx upsert %s: %w", snapshots[i].UserID, err)
		}
	}
	return nil
}

// SaveBatch wraps SaveBatchTx in a short-lived transaction so the
// single-repo path stays a one-call API.
func (r *playerStateRepo) SaveBatch(ctx context.Context, snapshots []*gamepersist.PlayerStateSnapshot) error {
	if len(snapshots) == 0 {
		return nil
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("playerStateRepo.SaveBatch begin: %w", err)
	}
	if err := r.SaveBatchTx(ctx, tx, snapshots); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

// scanPlayerState unmarshals one player_state row into a snapshot.
// Works on both pgx.Row and pgx.Rows because both expose Scan with the
// same signature via the pgx.Row interface.
func scanPlayerState(scanner interface {
	Scan(dest ...any) error
}) (*gamepersist.PlayerStateSnapshot, error) {
	var snap gamepersist.PlayerStateSnapshot
	var currenciesBytes, cargoBytes, bankBytes, equipmentBytes []byte
	if err := scanner.Scan(
		&snap.UserID,
		&currenciesBytes,
		&cargoBytes,
		&bankBytes,
		&equipmentBytes,
	); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(currenciesBytes, &snap.Currencies); err != nil {
		return nil, fmt.Errorf("unmarshal currencies %s: %w", snap.UserID, err)
	}
	if err := json.Unmarshal(cargoBytes, &snap.Cargo); err != nil {
		return nil, fmt.Errorf("unmarshal cargo %s: %w", snap.UserID, err)
	}
	if err := json.Unmarshal(bankBytes, &snap.Bank); err != nil {
		return nil, fmt.Errorf("unmarshal bank %s: %w", snap.UserID, err)
	}
	if err := json.Unmarshal(equipmentBytes, &snap.Equipment); err != nil {
		return nil, fmt.Errorf("unmarshal equipment %s: %w", snap.UserID, err)
	}
	// Nil-map guard: JSONB '{}' decodes to a nil map under encoding/json
	// when the destination is a map type. Replace with empty maps so
	// downstream code can range/index without nil checks.
	if snap.Currencies == nil {
		snap.Currencies = map[uint32]int64{}
	}
	if snap.Cargo == nil {
		snap.Cargo = map[uint32]int32{}
	}
	if snap.Bank == nil {
		snap.Bank = map[uint32]int32{}
	}
	return &snap, nil
}

// marshalJSONOrEmptyObject returns "{}" for nil maps so the JSONB column
// always has a valid object (matching the schema's DEFAULT '{}'::jsonb).
func marshalJSONOrEmptyObject(v any) ([]byte, error) {
	if v == nil {
		return []byte(`{}`), nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if len(b) == 0 || string(b) == "null" {
		return []byte(`{}`), nil
	}
	return b, nil
}
