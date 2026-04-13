package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zenion/mmoserver/pkg/persist"
)

type configRepo struct {
	pool *pgxpool.Pool
}

var _ persist.ConfigRepository = (*configRepo)(nil)

func (r *configRepo) Load(ctx context.Context) (*persist.ConfigSnapshot, error) {
	return nil, errors.New("configRepo.Load: not implemented (T4)")
}

func (r *configRepo) Save(ctx context.Context, snapshot *persist.ConfigSnapshot) error {
	return errors.New("configRepo.Save: not implemented (T4)")
}
