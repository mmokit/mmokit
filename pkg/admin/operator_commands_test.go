package admin

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zenion/mmoserver/pkg/cmdsys"
	"github.com/zenion/mmoserver/pkg/persist"
	"github.com/zenion/mmoserver/pkg/persist/persisttest"
	"github.com/zenion/mmoserver/pkg/services/auth"
)

// adminOpEnv builds a synthetic cmdsys.Env for direct handler invocation in
// tests. callerID is the operator (or "console") issuing the command.
func adminOpEnv(callerID string) (*cmdsys.Env, context.Context) {
	return &cmdsys.Env{Caller: cmdsys.Caller{ID: callerID, Source: cmdsys.SourceAdminHTTP}}, context.Background()
}

func seedOperator(t *testing.T, repo *persisttest.AdminOperatorRepoMock, username, password string, grants []string) {
	t.Helper()
	hash, err := auth.HashPassword(password, auth.DefaultArgonParams())
	if err != nil {
		t.Fatalf("seed: hash: %v", err)
	}
	if err := repo.Create(context.Background(), &persist.AdminOperator{
		Username:     username,
		PasswordHash: hash,
		Grants:       grants,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed: create %q: %v", username, err)
	}
}

func TestAdminOperatorCreate_Success(t *testing.T) {
	repo := persisttest.NewAdminOperatorRepoMock()
	env, ctx := adminOpEnv("admin")
	h := makeAdminOperatorCreate(repo)

	out, err := h(ctx, env, adminOperatorCreateArgs{
		Username: "Alice",
		Password: "swordfish",
		Grants:   []string{"cell.*"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	res, ok := out.(*adminOperatorCreateResult)
	if !ok {
		t.Fatalf("create: result type %T", out)
	}
	if res.Username != "alice" {
		t.Fatalf("create: username = %q, want alice", res.Username)
	}
	if len(res.Grants) != 1 || res.Grants[0] != "cell.*" {
		t.Fatalf("create: grants = %v, want [cell.*]", res.Grants)
	}

	op, err := repo.GetByUsername(ctx, "alice")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if op.Username != "alice" {
		t.Fatalf("stored username = %q, want alice", op.Username)
	}
	if len(op.Grants) != 1 || op.Grants[0] != "cell.*" {
		t.Fatalf("stored grants = %v, want [cell.*]", op.Grants)
	}
	ok, err = auth.VerifyPassword("swordfish", op.PasswordHash)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !ok {
		t.Fatalf("stored hash does not verify against original password")
	}
}

func TestAdminOperatorCreate_DefaultsGrantsToWildcard(t *testing.T) {
	repo := persisttest.NewAdminOperatorRepoMock()
	env, ctx := adminOpEnv("admin")
	h := makeAdminOperatorCreate(repo)

	out, err := h(ctx, env, adminOperatorCreateArgs{
		Username: "noob",
		Password: "pw",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	res := out.(*adminOperatorCreateResult)
	if len(res.Grants) != 1 || res.Grants[0] != "*.*" {
		t.Fatalf("default grants = %v, want [*.*]", res.Grants)
	}
}

func TestAdminOperatorCreate_DuplicateRejected(t *testing.T) {
	repo := persisttest.NewAdminOperatorRepoMock()
	seedOperator(t, repo, "alice", "pw", []string{"*.*"})
	env, ctx := adminOpEnv("admin")
	h := makeAdminOperatorCreate(repo)

	_, err := h(ctx, env, adminOperatorCreateArgs{
		Username: "alice",
		Password: "newpw",
	})
	if err == nil {
		t.Fatalf("create: expected duplicate error, got nil")
	}
}

func TestAdminOperatorCreate_RejectsEmptyPassword(t *testing.T) {
	repo := persisttest.NewAdminOperatorRepoMock()
	env, ctx := adminOpEnv("admin")
	h := makeAdminOperatorCreate(repo)

	_, err := h(ctx, env, adminOperatorCreateArgs{
		Username: "alice",
		Password: "",
	})
	if err == nil {
		t.Fatalf("create: expected empty-password error, got nil")
	}
	if !strings.Contains(err.Error(), "password") {
		t.Fatalf("create: error %q should mention password", err)
	}
}

func TestAdminOperatorList_ReturnsSorted(t *testing.T) {
	repo := persisttest.NewAdminOperatorRepoMock()
	seedOperator(t, repo, "zoe", "pw", []string{"*.*"})
	seedOperator(t, repo, "alice", "pw", []string{"*.*"})
	seedOperator(t, repo, "bob", "pw", []string{"*.*"})
	env, ctx := adminOpEnv("admin")
	h := makeAdminOperatorList(repo)

	out, err := h(ctx, env, adminOperatorListArgs{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	res := out.(*adminOperatorListResult)
	if len(res.Operators) != 3 {
		t.Fatalf("list: got %d rows, want 3", len(res.Operators))
	}
	want := []string{"alice", "bob", "zoe"}
	for i, row := range res.Operators {
		if row.Username != want[i] {
			t.Fatalf("list: row[%d].Username = %q, want %q", i, row.Username, want[i])
		}
		if row.CreatedAt == "" || row.UpdatedAt == "" {
			t.Fatalf("list: row[%d] missing timestamp; got %+v", i, row)
		}
		// CreatedAt should parse as RFC3339.
		if _, err := time.Parse(time.RFC3339, row.CreatedAt); err != nil {
			t.Fatalf("list: row[%d].CreatedAt %q not RFC3339: %v", i, row.CreatedAt, err)
		}
	}
}

func TestAdminOperatorPassword_Rotates(t *testing.T) {
	repo := persisttest.NewAdminOperatorRepoMock()
	seedOperator(t, repo, "alice", "old", []string{"*.*"})
	env, ctx := adminOpEnv("admin")
	h := makeAdminOperatorPassword(repo)

	_, err := h(ctx, env, adminOperatorPasswordArgs{
		Username: "alice",
		Password: "newpass",
	})
	if err != nil {
		t.Fatalf("password: %v", err)
	}

	op, err := repo.GetByUsername(ctx, "alice")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	ok, err := auth.VerifyPassword("newpass", op.PasswordHash)
	if err != nil {
		t.Fatalf("verify new: %v", err)
	}
	if !ok {
		t.Fatalf("new password does not verify against stored hash")
	}
	// Old password should no longer verify.
	ok, _ = auth.VerifyPassword("old", op.PasswordHash)
	if ok {
		t.Fatalf("old password still verifies after rotation")
	}
}

func TestAdminOperatorPassword_UnknownUser(t *testing.T) {
	repo := persisttest.NewAdminOperatorRepoMock()
	env, ctx := adminOpEnv("admin")
	h := makeAdminOperatorPassword(repo)

	_, err := h(ctx, env, adminOperatorPasswordArgs{
		Username: "ghost",
		Password: "pw",
	})
	if err == nil {
		t.Fatalf("password: expected error, got nil")
	}
	if !errors.Is(err, persist.ErrNotFound) {
		t.Fatalf("password: error = %v, want ErrNotFound", err)
	}
}

func TestAdminOperatorDelete_Success(t *testing.T) {
	repo := persisttest.NewAdminOperatorRepoMock()
	seedOperator(t, repo, "alice", "pw", []string{"*.*"})
	seedOperator(t, repo, "bob", "pw", []string{"*.*"})
	env, ctx := adminOpEnv("admin")
	h := makeAdminOperatorDelete(repo)

	out, err := h(ctx, env, adminOperatorDeleteArgs{Username: "bob"})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	res := out.(*adminOperatorDeleteResult)
	if res.Username != "bob" {
		t.Fatalf("delete: result username %q, want bob", res.Username)
	}

	if _, err := repo.GetByUsername(ctx, "bob"); !errors.Is(err, persist.ErrNotFound) {
		t.Fatalf("delete: post-delete lookup err = %v, want ErrNotFound", err)
	}
	if _, err := repo.GetByUsername(ctx, "alice"); err != nil {
		t.Fatalf("delete: alice still exists check: %v", err)
	}
}

func TestAdminOperatorDelete_RejectsLastOperator(t *testing.T) {
	repo := persisttest.NewAdminOperatorRepoMock()
	seedOperator(t, repo, "alice", "pw", []string{"*.*"})
	// Use a different caller id so the self-delete guard doesn't fire first.
	env, ctx := adminOpEnv("ops")
	h := makeAdminOperatorDelete(repo)

	_, err := h(ctx, env, adminOperatorDeleteArgs{Username: "alice"})
	if err == nil {
		t.Fatalf("delete: expected last-operator error, got nil")
	}
	if !strings.Contains(err.Error(), "last admin operator") {
		t.Fatalf("delete: error %q should mention last admin operator", err)
	}
	if _, err := repo.GetByUsername(ctx, "alice"); err != nil {
		t.Fatalf("delete: alice should still be present: %v", err)
	}
}

func TestAdminOperatorDelete_RejectsSelf(t *testing.T) {
	repo := persisttest.NewAdminOperatorRepoMock()
	seedOperator(t, repo, "alice", "pw", []string{"*.*"})
	seedOperator(t, repo, "bob", "pw", []string{"*.*"})
	env, ctx := adminOpEnv("alice")
	h := makeAdminOperatorDelete(repo)

	_, err := h(ctx, env, adminOperatorDeleteArgs{Username: "alice"})
	if err == nil {
		t.Fatalf("delete: expected self-delete error, got nil")
	}
	if !strings.Contains(err.Error(), "yourself") {
		t.Fatalf("delete: error %q should mention 'yourself'", err)
	}
	if _, err := repo.GetByUsername(ctx, "alice"); err != nil {
		t.Fatalf("delete: alice should still be present: %v", err)
	}
}

func TestAdminOperatorDelete_ConsoleCanDeleteAnyone(t *testing.T) {
	repo := persisttest.NewAdminOperatorRepoMock()
	seedOperator(t, repo, "alice", "pw", []string{"*.*"})
	seedOperator(t, repo, "bob", "pw", []string{"*.*"})
	env := &cmdsys.Env{Caller: cmdsys.Caller{ID: "console", Source: cmdsys.SourceConsole}}
	ctx := context.Background()
	h := makeAdminOperatorDelete(repo)

	if _, err := h(ctx, env, adminOperatorDeleteArgs{Username: "alice"}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := repo.GetByUsername(ctx, "alice"); !errors.Is(err, persist.ErrNotFound) {
		t.Fatalf("delete: alice should be gone; err = %v", err)
	}
}

func TestAdminOperatorDelete_UnknownUser(t *testing.T) {
	repo := persisttest.NewAdminOperatorRepoMock()
	seedOperator(t, repo, "alice", "pw", []string{"*.*"})
	seedOperator(t, repo, "bob", "pw", []string{"*.*"})
	env, ctx := adminOpEnv("admin")
	h := makeAdminOperatorDelete(repo)

	_, err := h(ctx, env, adminOperatorDeleteArgs{Username: "ghost"})
	if err == nil {
		t.Fatalf("delete: expected error, got nil")
	}
	if !errors.Is(err, persist.ErrNotFound) {
		t.Fatalf("delete: error = %v, want ErrNotFound", err)
	}
}
