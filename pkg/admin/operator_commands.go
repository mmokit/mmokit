package admin

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/zenion/mmoserver/pkg/cmdsys"
	"github.com/zenion/mmoserver/pkg/persist"
	"github.com/zenion/mmoserver/pkg/services/auth"
)

// ── admin.operator.create ────────────────────────────────────────────────────

type adminOperatorCreateArgs struct {
	Username string   `cmd:"help=operator username (lowercased)"`
	Password string   `cmd:"secret,help=initial password"`
	Grants   []string `cmd:"optional,help=grant patterns; default *.*"`
}

type adminOperatorCreateResult struct {
	Username string   `json:"username"`
	Grants   []string `json:"grants"`
}

// ── admin.operator.delete ────────────────────────────────────────────────────

type adminOperatorDeleteArgs struct {
	Username string `cmd:"help=operator username"`
}

type adminOperatorDeleteResult struct {
	Username string `json:"username"`
}

// ── admin.operator.password ──────────────────────────────────────────────────

type adminOperatorPasswordArgs struct {
	Username string `cmd:"help=operator username"`
	Password string `cmd:"secret,help=new password"`
}

type adminOperatorPasswordResult struct {
	Username string `json:"username"`
}

// ── admin.operator.list ──────────────────────────────────────────────────────

type adminOperatorListArgs struct{}

type adminOperatorListResult struct {
	Operators []adminOperatorListRow `json:"operators"`
}

type adminOperatorListRow struct {
	Username  string   `json:"username"`
	Grants    []string `json:"grants"`
	CreatedAt string   `json:"createdAt"`
	UpdatedAt string   `json:"updatedAt"`
}

// RegisterOperatorCommands installs the admin.operator.* verbs against reg.
// All four verbs share the admin.operator capability; the seeded default
// admin/admin operator's *.* grant subsumes it.
func RegisterOperatorCommands(reg *cmdsys.Registry, repo persist.AdminOperatorRepository) error {
	if reg == nil {
		return fmt.Errorf("admin: RegisterOperatorCommands: nil registry")
	}
	if repo == nil {
		return fmt.Errorf("admin: RegisterOperatorCommands: nil operator repo")
	}

	if err := reg.Register(cmdsys.Command{
		Verb:        "admin.operator.create",
		Capability:  "admin.operator",
		Description: "create a new admin dashboard operator",
		Route:       cmdsys.RouteLocal,
		Args:        adminOperatorCreateArgs{},
		Result:      &adminOperatorCreateResult{},
		Handler:     makeAdminOperatorCreate(repo),
	}); err != nil {
		return fmt.Errorf("admin.operator.create: %w", err)
	}

	if err := reg.Register(cmdsys.Command{
		Verb:        "admin.operator.delete",
		Capability:  "admin.operator",
		Description: "delete an admin dashboard operator",
		Route:       cmdsys.RouteLocal,
		Args:        adminOperatorDeleteArgs{},
		Result:      &adminOperatorDeleteResult{},
		Handler:     makeAdminOperatorDelete(repo),
	}); err != nil {
		return fmt.Errorf("admin.operator.delete: %w", err)
	}

	if err := reg.Register(cmdsys.Command{
		Verb:        "admin.operator.password",
		Capability:  "admin.operator",
		Description: "rotate an admin dashboard operator's password",
		Route:       cmdsys.RouteLocal,
		Args:        adminOperatorPasswordArgs{},
		Result:      &adminOperatorPasswordResult{},
		Handler:     makeAdminOperatorPassword(repo),
	}); err != nil {
		return fmt.Errorf("admin.operator.password: %w", err)
	}

	if err := reg.Register(cmdsys.Command{
		Verb:        "admin.operator.list",
		Capability:  "admin.operator",
		Description: "list every admin dashboard operator",
		Route:       cmdsys.RouteLocal,
		Args:        adminOperatorListArgs{},
		Result:      &adminOperatorListResult{},
		Handler:     makeAdminOperatorList(repo),
	}); err != nil {
		return fmt.Errorf("admin.operator.list: %w", err)
	}

	return nil
}

func makeAdminOperatorCreate(repo persist.AdminOperatorRepository) cmdsys.HandlerFunc {
	return func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
		args, ok := raw.(adminOperatorCreateArgs)
		if !ok {
			return nil, fmt.Errorf("admin.operator.create: bad args type %T", raw)
		}
		username := strings.ToLower(strings.TrimSpace(args.Username))
		if username == "" {
			return nil, fmt.Errorf("admin.operator.create: username required")
		}
		if args.Password == "" {
			return nil, fmt.Errorf("admin.operator.create: password required")
		}
		grants := args.Grants
		if len(grants) == 0 {
			grants = []string{"*.*"}
		} else {
			grants = slices.Clone(grants)
		}
		hash, err := auth.HashPassword(args.Password, auth.DefaultArgonParams())
		if err != nil {
			return nil, fmt.Errorf("admin.operator.create: hash password: %w", err)
		}
		now := time.Now().UTC()
		op := &persist.AdminOperator{
			Username:     username,
			PasswordHash: hash,
			Grants:       grants,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		if err := repo.Create(ctx, op); err != nil {
			return nil, fmt.Errorf("admin.operator.create: %w", err)
		}
		return &adminOperatorCreateResult{Username: username, Grants: grants}, nil
	}
}

func makeAdminOperatorDelete(repo persist.AdminOperatorRepository) cmdsys.HandlerFunc {
	return func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
		args, ok := raw.(adminOperatorDeleteArgs)
		if !ok {
			return nil, fmt.Errorf("admin.operator.delete: bad args type %T", raw)
		}
		username := strings.ToLower(strings.TrimSpace(args.Username))
		if username == "" {
			return nil, fmt.Errorf("admin.operator.delete: username required")
		}
		// Self-delete guard: prevents an operator from locking themselves
		// out by name. The console caller-ID is "console", which collides
		// only if an operator named "console" exists (acceptable degenerate
		// case).
		if env != nil && strings.ToLower(env.Caller.ID) == username {
			return nil, fmt.Errorf("admin.operator.delete: cannot delete yourself")
		}
		// Confirm the target exists first so the unknown-user case doesn't
		// get misclassified as "last operator" by the count guard below.
		if _, err := repo.GetByUsername(ctx, username); err != nil {
			if errors.Is(err, persist.ErrNotFound) {
				return nil, err
			}
			return nil, fmt.Errorf("admin.operator.delete: lookup: %w", err)
		}
		n, err := repo.Count(ctx)
		if err != nil {
			return nil, fmt.Errorf("admin.operator.delete: count: %w", err)
		}
		if n <= 1 {
			return nil, fmt.Errorf("admin.operator.delete: cannot delete the last admin operator")
		}
		if err := repo.Delete(ctx, username); err != nil {
			return nil, fmt.Errorf("admin.operator.delete: %w", err)
		}
		return &adminOperatorDeleteResult{Username: username}, nil
	}
}

func makeAdminOperatorPassword(repo persist.AdminOperatorRepository) cmdsys.HandlerFunc {
	return func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
		args, ok := raw.(adminOperatorPasswordArgs)
		if !ok {
			return nil, fmt.Errorf("admin.operator.password: bad args type %T", raw)
		}
		username := strings.ToLower(strings.TrimSpace(args.Username))
		if username == "" {
			return nil, fmt.Errorf("admin.operator.password: username required")
		}
		if args.Password == "" {
			return nil, fmt.Errorf("admin.operator.password: password required")
		}
		hash, err := auth.HashPassword(args.Password, auth.DefaultArgonParams())
		if err != nil {
			return nil, fmt.Errorf("admin.operator.password: hash password: %w", err)
		}
		// UpdatePasswordHash returns persist.ErrNotFound for unknown users —
		// surface it directly so callers can errors.Is-match.
		if err := repo.UpdatePasswordHash(ctx, username, hash); err != nil {
			if errors.Is(err, persist.ErrNotFound) {
				return nil, err
			}
			return nil, fmt.Errorf("admin.operator.password: %w", err)
		}
		return &adminOperatorPasswordResult{Username: username}, nil
	}
}

func makeAdminOperatorList(repo persist.AdminOperatorRepository) cmdsys.HandlerFunc {
	return func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
		if _, ok := raw.(adminOperatorListArgs); !ok && raw != nil {
			return nil, fmt.Errorf("admin.operator.list: bad args type %T", raw)
		}
		ops, err := repo.List(ctx)
		if err != nil {
			return nil, fmt.Errorf("admin.operator.list: %w", err)
		}
		rows := make([]adminOperatorListRow, 0, len(ops))
		for _, op := range ops {
			rows = append(rows, adminOperatorListRow{
				Username:  op.Username,
				Grants:    slices.Clone(op.Grants),
				CreatedAt: op.CreatedAt.UTC().Format(time.RFC3339),
				UpdatedAt: op.UpdatedAt.UTC().Format(time.RFC3339),
			})
		}
		return &adminOperatorListResult{Operators: rows}, nil
	}
}
