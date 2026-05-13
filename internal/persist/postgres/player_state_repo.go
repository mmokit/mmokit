package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	gamepersist "github.com/zenion/mmoserver/internal/persist"
)

type playerStateRepo struct {
	pool *pgxpool.Pool
}

var _ gamepersist.PlayerStateRepository = (*playerStateRepo)(nil)

const playerStateSelectColumns = `username, currencies, cargo, bank, equipment`

// Load fetches one player's space-game state by username.
func (r *playerStateRepo) Load(ctx context.Context, username string) (*gamepersist.PlayerStateSnapshot, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+playerStateSelectColumns+` FROM space.player_state WHERE username = $1`,
		username,
	)
	snap, err := scanPlayerState(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, gamepersist.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("playerStateRepo.Load %q: %w", username, err)
	}
	return snap, nil
}

// LoadAll streams every player_state row. Iteration order is
// unspecified (no ORDER BY) — the caller is the in-memory cache, which
// just builds a map keyed by username.
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
// transaction. Same sort-by-username contract as SaveBatch — callers
// must pre-sort. Used by the game-side PlayerFlusher to write engine
// identity and game state atomically under one pgx.Tx.
func (r *playerStateRepo) SaveBatchTx(ctx context.Context, tx pgx.Tx, snapshots []*gamepersist.PlayerStateSnapshot) error {
	if len(snapshots) == 0 {
		return nil
	}
	for i := 1; i < len(snapshots); i++ {
		if snapshots[i-1].Username > snapshots[i].Username {
			return errors.New("playerStateRepo.SaveBatchTx: snapshots not sorted by username (deadlock prevention contract)")
		}
	}

	batch := &pgx.Batch{}
	for _, s := range snapshots {
		currenciesJSON, err := marshalJSONOrEmptyObject(s.Currencies)
		if err != nil {
			return fmt.Errorf("marshal currencies %q: %w", s.Username, err)
		}
		cargoJSON, err := marshalJSONOrEmptyObject(s.Cargo)
		if err != nil {
			return fmt.Errorf("marshal cargo %q: %w", s.Username, err)
		}
		bankJSON, err := marshalJSONOrEmptyObject(s.Bank)
		if err != nil {
			return fmt.Errorf("marshal bank %q: %w", s.Username, err)
		}
		equipmentJSON, err := json.Marshal(s.Equipment)
		if err != nil {
			return fmt.Errorf("marshal equipment %q: %w", s.Username, err)
		}
		batch.Queue(`
			INSERT INTO space.player_state (
				username, currencies, cargo, bank, equipment, updated_at
			)
			VALUES ($1, $2, $3, $4, $5, NOW())
			ON CONFLICT (username) DO UPDATE SET
				currencies = EXCLUDED.currencies,
				cargo      = EXCLUDED.cargo,
				bank       = EXCLUDED.bank,
				equipment  = EXCLUDED.equipment,
				updated_at = NOW()
		`,
			s.Username, currenciesJSON, cargoJSON, bankJSON, equipmentJSON,
		)
	}

	br := tx.SendBatch(ctx, batch)
	defer br.Close()

	for i := range len(snapshots) {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("playerStateRepo.SaveBatchTx upsert %q: %w", snapshots[i].Username, err)
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
		&snap.Username,
		&currenciesBytes,
		&cargoBytes,
		&bankBytes,
		&equipmentBytes,
	); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(currenciesBytes, &snap.Currencies); err != nil {
		return nil, fmt.Errorf("unmarshal currencies %q: %w", snap.Username, err)
	}
	if err := json.Unmarshal(cargoBytes, &snap.Cargo); err != nil {
		return nil, fmt.Errorf("unmarshal cargo %q: %w", snap.Username, err)
	}
	if err := json.Unmarshal(bankBytes, &snap.Bank); err != nil {
		return nil, fmt.Errorf("unmarshal bank %q: %w", snap.Username, err)
	}
	if err := json.Unmarshal(equipmentBytes, &snap.Equipment); err != nil {
		return nil, fmt.Errorf("unmarshal equipment %q: %w", snap.Username, err)
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
