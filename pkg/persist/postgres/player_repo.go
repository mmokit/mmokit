package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zenion/mmoserver/pkg/persist"
)

type playerRepo struct {
	pool *pgxpool.Pool
}

var _ persist.PlayerRepository = (*playerRepo)(nil)

func (r *playerRepo) Load(ctx context.Context, username string) (*persist.PlayerSnapshot, error) {
	return nil, errors.New("playerRepo.Load: not implemented (T4)")
}

func (r *playerRepo) LoadAll(ctx context.Context, fn func(*persist.PlayerSnapshot) error) error {
	return errors.New("playerRepo.LoadAll: not implemented (T4)")
}

func (r *playerRepo) SaveBatch(ctx context.Context, snapshots []*persist.PlayerSnapshot) error {
	return errors.New("playerRepo.SaveBatch: not implemented (T4)")
}
