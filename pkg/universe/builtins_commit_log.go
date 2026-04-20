// Package universe — builtins_commit_log.go
//
// commit.log — query the in-memory commit event ring populated by the
// commit plan executor, the coordinator's host-event wiring, and the
// invariant layer. Operators use this to diagnose asymmetric bugs in the
// distributed commit path: filter by commit ID to replay one commit's
// full step sequence, by cell to see every commit that touched a given
// cell, or by duration to catch the most recent activity. Read-only;
// RouteLocal.

package universe

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/zenion/mmoserver/pkg/cmdsys"
)

type commitLogArgs struct {
	N        int    `cmd:"optional,name=n,help=return the last N events (default 20)"`
	CommitID uint64 `cmd:"optional,name=commit,help=filter by commit ID"`
	CellID   string `cmd:"optional,name=cell,help=filter by cell ID,complete=cells"`
	Since    string `cmd:"optional,name=since,help=Go duration window (e.g. 5m, 30s)"`
}

// CommitLogRow is one rendered row in the commit log table. Fields are
// flattened strings so the cmd table renderer can lay them out with
// consistent column widths — the raw CommitEvent carries richer types
// that don't render cleanly as a uniform table.
type CommitLogRow struct {
	Seq      uint64
	Time     string
	CommitID uint64
	Kind     string
	Scenario string
	Step     string
	Status   string
	DurMs    int64
	Affected string
	Error    string
}

type commitLogResult struct {
	Events []CommitLogRow `cmd:"table"`
	Notice string
}

// registerCommitLogBuiltins wires `commit.log` into the dispatcher so
// operators can query recent topology events from any console.
func registerCommitLogBuiltins(reg *cmdsys.Registry, coord *Process) error {
	c := coord

	if err := reg.Register(cmdsys.Command{
		Verb:        "commit.log",
		Capability:  "commit.log",
		Description: "query the commit event log (steps, invariants, host events)",
		Route:       cmdsys.RouteLocal,
		Args:        commitLogArgs{},
		Result:      commitLogResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			if c.commitLog == nil {
				return commitLogResult{Notice: "commit log not enabled"}, nil
			}
			args := raw.(commitLogArgs)

			var events []CommitEvent
			switch {
			case args.CommitID != 0:
				events = c.commitLog.ByCommitID(args.CommitID)
			case strings.TrimSpace(args.CellID) != "":
				events = c.commitLog.ByCell(strings.TrimSpace(args.CellID))
			case strings.TrimSpace(args.Since) != "":
				d, err := time.ParseDuration(strings.TrimSpace(args.Since))
				if err != nil {
					return nil, fmt.Errorf("bad since duration: %w", err)
				}
				events = c.commitLog.Since(time.Now().Add(-d))
			default:
				n := args.N
				if n <= 0 {
					n = 20
				}
				events = c.commitLog.Recent(n)
			}

			if len(events) == 0 {
				return commitLogResult{Notice: "no matching events"}, nil
			}

			rows := make([]CommitLogRow, 0, len(events))
			for _, e := range events {
				status := "ok"
				if !e.Success {
					status = "FAIL"
				}
				// Scenario is zero-valued (= CommitKindSplit) for non-commit
				// events like invariant violations and host join/leave. Print
				// a dash in those cases rather than a misleading "Split".
				scenario := "-"
				switch e.Kind {
				case EventCommitSplit, EventCommitMerge, EventCommitMigrate:
					scenario = e.Scenario.String()
				}
				rows = append(rows, CommitLogRow{
					Seq:      e.SeqNo,
					Time:     e.Timestamp.Format("15:04:05.000"),
					CommitID: e.CommitID,
					Kind:     e.Kind.String(),
					Scenario: scenario,
					Step:     e.Step,
					Status:   status,
					DurMs:    e.DurationMs,
					Affected: strings.Join(e.Affected, ","),
					Error:    e.Error,
				})
			}
			return commitLogResult{Events: rows}, nil
		},
	}); err != nil {
		return fmt.Errorf("registerCommitLogBuiltins commit.log: %w", err)
	}
	return nil
}
