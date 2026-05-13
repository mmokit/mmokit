package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zenion/mmoserver/pkg/persist"
)

type playerRepo struct {
	pool *pgxpool.Pool
}

var _ persist.PlayerRepository = (*playerRepo)(nil)

const playerSelectColumns = `user_id, username, cell_id, pos_x, pos_y, created_at, last_login, debug_flags`

func (r *playerRepo) Load(ctx context.Context, userID uuid.UUID) (*persist.PlayerSnapshot, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+playerSelectColumns+` FROM engine.players WHERE user_id = $1`,
		userID,
	)
	snap, err := scanPlayerRow(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, persist.ErrNotFound
		}
		return nil, fmt.Errorf("playerRepo.Load %s: %w", userID, err)
	}
	return snap, nil
}

func (r *playerRepo) LoadByUsername(ctx context.Context, username string) (*persist.PlayerSnapshot, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+playerSelectColumns+` FROM engine.players WHERE username = $1`,
		username,
	)
	snap, err := scanPlayerRow(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, persist.ErrNotFound
		}
		return nil, fmt.Errorf("playerRepo.LoadByUsername %q: %w", username, err)
	}
	return snap, nil
}

func (r *playerRepo) LoadAll(ctx context.Context, fn func(*persist.PlayerSnapshot) error) error {
	rows, err := r.pool.Query(ctx,
		`SELECT `+playerSelectColumns+` FROM engine.players`)
	if err != nil {
		return fmt.Errorf("playerRepo.LoadAll: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		snap, err := scanPlayerRow(rows)
		if err != nil {
			return fmt.Errorf("playerRepo.LoadAll scan: %w", err)
		}
		if err := fn(snap); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (r *playerRepo) SaveBatchTx(ctx context.Context, tx pgx.Tx, snapshots []*persist.PlayerSnapshot) error {
	if len(snapshots) == 0 {
		return nil
	}
	for i := 1; i < len(snapshots); i++ {
		if snapshots[i-1].UserID.String() > snapshots[i].UserID.String() {
			return errors.New("playerRepo.SaveBatchTx: snapshots not sorted by user_id (deadlock prevention contract)")
		}
	}

	batch := &pgx.Batch{}
	for _, snap := range snapshots {
		flagsJSON, err := marshalDebugFlags(snap.DebugFlags)
		if err != nil {
			return fmt.Errorf("playerRepo.SaveBatchTx marshal flags %s: %w", snap.UserID, err)
		}
		batch.Queue(`
			INSERT INTO engine.players (
				user_id, username, cell_id, pos_x, pos_y, created_at, last_login, debug_flags, updated_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, NOW())
			ON CONFLICT (user_id) DO UPDATE SET
			    username = EXCLUDED.username,
			    cell_id = EXCLUDED.cell_id,
			    pos_x = EXCLUDED.pos_x,
			    pos_y = EXCLUDED.pos_y,
			    last_login = EXCLUDED.last_login,
			    debug_flags = EXCLUDED.debug_flags,
			    updated_at = NOW()`,
			snap.UserID, snap.Username, snap.CellID, snap.PosX, snap.PosY,
			snap.CreatedAt, snap.LastLogin, flagsJSON,
		)
	}

	br := tx.SendBatch(ctx, batch)
	defer br.Close()
	for i := range snapshots {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("playerRepo.SaveBatchTx exec %d: %w", i, err)
		}
	}
	return nil
}

// SaveBatch wraps SaveBatchTx in a short-lived transaction so the
// single-repo path stays a one-call API. The PlayerFlusher uses
// SaveBatchTx directly so engine identity and game state commit atomically.
func (r *playerRepo) SaveBatch(ctx context.Context, snapshots []*persist.PlayerSnapshot) error {
	if len(snapshots) == 0 {
		return nil
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("playerRepo.SaveBatch begin: %w", err)
	}
	if err := r.SaveBatchTx(ctx, tx, snapshots); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

// LoadDebugFlags / SaveDebugFlags / LoadAllDebugFlags remain keyed on
// username — they back console admin commands that take a display name.
// username is UNIQUE on engine.players so the filter is still a single-row
// lookup.

func (r *playerRepo) LoadDebugFlags(ctx context.Context, username string) ([]string, error) {
	var flagsBytes []byte
	err := r.pool.QueryRow(ctx,
		`SELECT debug_flags FROM engine.players WHERE username = $1`,
		username,
	).Scan(&flagsBytes)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, persist.ErrNotFound
		}
		return nil, fmt.Errorf("playerRepo.LoadDebugFlags %q: %w", username, err)
	}
	var flags []string
	if err := json.Unmarshal(flagsBytes, &flags); err != nil {
		return nil, fmt.Errorf("playerRepo.LoadDebugFlags decode %q: %w", username, err)
	}
	if flags == nil {
		flags = []string{}
	}
	return flags, nil
}

func (r *playerRepo) SaveDebugFlags(ctx context.Context, username string, flags []string) error {
	flagsJSON, err := marshalDebugFlags(flags)
	if err != nil {
		return fmt.Errorf("playerRepo.SaveDebugFlags marshal %q: %w", username, err)
	}
	// SaveDebugFlags updates an existing player's flag list — the row
	// must already exist (engine.players requires user_id, which we
	// don't have here). Treat a no-row update as ErrNotFound so callers
	// can surface "no such user" rather than silently no-op.
	tag, err := r.pool.Exec(ctx,
		`UPDATE engine.players
		 SET debug_flags = $2::jsonb, updated_at = NOW()
		 WHERE username = $1`,
		username, flagsJSON,
	)
	if err != nil {
		return fmt.Errorf("playerRepo.SaveDebugFlags %q: %w", username, err)
	}
	if tag.RowsAffected() == 0 {
		return persist.ErrNotFound
	}
	return nil
}

func (r *playerRepo) LoadAllDebugFlags(ctx context.Context) (map[string][]string, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT username, debug_flags FROM engine.players WHERE debug_flags <> '[]'::jsonb`)
	if err != nil {
		return nil, fmt.Errorf("playerRepo.LoadAllDebugFlags: %w", err)
	}
	defer rows.Close()

	out := make(map[string][]string)
	for rows.Next() {
		var username string
		var flagsBytes []byte
		if err := rows.Scan(&username, &flagsBytes); err != nil {
			return nil, fmt.Errorf("playerRepo.LoadAllDebugFlags scan: %w", err)
		}
		var flags []string
		if err := json.Unmarshal(flagsBytes, &flags); err != nil {
			return nil, fmt.Errorf("playerRepo.LoadAllDebugFlags decode %q: %w", username, err)
		}
		if len(flags) > 0 {
			out[username] = flags
		}
	}
	return out, rows.Err()
}

type pgxScanner interface {
	Scan(dest ...any) error
}

func scanPlayerRow(row pgxScanner) (*persist.PlayerSnapshot, error) {
	var snap persist.PlayerSnapshot
	var flagsBytes []byte
	if err := row.Scan(
		&snap.UserID,
		&snap.Username,
		&snap.CellID,
		&snap.PosX,
		&snap.PosY,
		&snap.CreatedAt,
		&snap.LastLogin,
		&flagsBytes,
	); err != nil {
		return nil, err
	}
	var flags []string
	if err := json.Unmarshal(flagsBytes, &flags); err != nil {
		return nil, fmt.Errorf("decode debug_flags: %w", err)
	}
	if flags != nil {
		snap.DebugFlags = flags
	}
	return &snap, nil
}

func marshalDebugFlags(flags []string) ([]byte, error) {
	if flags == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(flags)
}
