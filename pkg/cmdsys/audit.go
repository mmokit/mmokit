package cmdsys

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// AuditRecord describes a single command lifecycle event.
type AuditRecord struct {
	Time       time.Time
	TraceID    string
	CallerID   string
	Source     CallerSource
	Verb       string
	ArgsJSON   []byte
	Phase      string // "start" or "done"
	Targets    []string // populated on phase=done
	OK         bool
	Error      string // short error code: "parse_error", "rbac_denied", "unknown_verb", "not_yet_wired", "handler_error"
	Detail     string // human-readable detail for the error code; empty when OK or no detail
	DurationMS int64  // populated on phase=done
}

// AuditSink receives audit records. Implementations must be goroutine-safe.
type AuditSink interface {
	Emit(rec AuditRecord)
}

// StderrAuditSink writes audit records to stderr as single-line JSON.
type StderrAuditSink struct{}

func (StderrAuditSink) Emit(r AuditRecord) {
	b, _ := json.Marshal(r)
	fmt.Fprintf(os.Stderr, "cmdsys audit: %s\n", b)
}

// NoopAuditSink discards all records.
type NoopAuditSink struct{}

func (NoopAuditSink) Emit(AuditRecord) {}
