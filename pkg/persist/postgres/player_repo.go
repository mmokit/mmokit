package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zenion/mmoserver/pkg/persist"
)

type playerRepo struct {
	pool *pgxpool.Pool
}

var _ persist.PlayerRepository = (*playerRepo)(nil)

const playerSelectColumns = `username, cell_id, pos_x, pos_y, currencies, cargo, bank, equipment, created_at, last_login`

// Load fetches one player by username.
func (r *playerRepo) Load(ctx context.Context, username string) (*persist.PlayerSnapshot, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+playerSelectColumns+` FROM players WHERE username = $1`,
		username,
	)
	snap, err := scanPlayer(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, persist.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("playerRepo.Load %q: %w", username, err)
	}
	return snap, nil
}

// LoadAll streams every player record. Iteration order is unspecified
// (no ORDER BY) — the caller is the in-memory PlayerRepo cache, which
// just builds a map keyed by username.
func (r *playerRepo) LoadAll(ctx context.Context, fn func(*persist.PlayerSnapshot) error) error {
	rows, err := r.pool.Query(ctx, `SELECT `+playerSelectColumns+` FROM players`)
	if err != nil {
		return fmt.Errorf("playerRepo.LoadAll query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		snap, err := scanPlayer(rows)
		if err != nil {
			return fmt.Errorf("playerRepo.LoadAll scan: %w", err)
		}
		if err := fn(snap); err != nil {
			return err
		}
	}
	return rows.Err()
}

// SaveBatch upserts every snapshot via a single pgx.Batch. Caller is
// expected to sort snapshots by username (PlayerRepository contract)
// to prevent deadlocks under concurrent flushes — we do NOT re-sort
// here, because the contract is on the caller.
//
// Sanity: if the slice is unsorted we return an error rather than
// silently risking a deadlock. Cheap O(n) check; pays for itself the
// first time someone forgets to sort.
func (r *playerRepo) SaveBatch(ctx context.Context, snapshots []*persist.PlayerSnapshot) error {
	if len(snapshots) == 0 {
		return nil
	}
	if !sort.SliceIsSorted(snapshots, func(i, j int) bool {
		return snapshots[i].Username < snapshots[j].Username
	}) {
		return errors.New("playerRepo.SaveBatch: snapshots not sorted by username (deadlock prevention contract)")
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
			INSERT INTO players (
				username, cell_id, pos_x, pos_y, currencies, cargo, bank, equipment,
				created_at, last_login, updated_at
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NOW())
			ON CONFLICT (username) DO UPDATE SET
				cell_id    = EXCLUDED.cell_id,
				pos_x      = EXCLUDED.pos_x,
				pos_y      = EXCLUDED.pos_y,
				currencies = EXCLUDED.currencies,
				cargo      = EXCLUDED.cargo,
				bank       = EXCLUDED.bank,
				equipment  = EXCLUDED.equipment,
				last_login = EXCLUDED.last_login,
				updated_at = NOW()
		`,
			s.Username, s.CellID, s.PosX, s.PosY,
			currenciesJSON, cargoJSON, bankJSON, equipmentJSON,
			s.CreatedAt, s.LastLogin,
		)
	}

	br := r.pool.SendBatch(ctx, batch)
	defer br.Close()

	for i := range len(snapshots) {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("playerRepo.SaveBatch upsert %q: %w", snapshots[i].Username, err)
		}
	}
	return nil
}

// scanPlayer unmarshals one player row into a PlayerSnapshot. Works on
// both pgx.Row and pgx.Rows because both expose Scan with the same
// signature via the pgx.Row interface.
func scanPlayer(scanner interface {
	Scan(dest ...any) error
}) (*persist.PlayerSnapshot, error) {
	var snap persist.PlayerSnapshot
	var currenciesBytes, cargoBytes, bankBytes, equipmentBytes []byte
	if err := scanner.Scan(
		&snap.Username,
		&snap.CellID,
		&snap.PosX,
		&snap.PosY,
		&currenciesBytes,
		&cargoBytes,
		&bankBytes,
		&equipmentBytes,
		&snap.CreatedAt,
		&snap.LastLogin,
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
