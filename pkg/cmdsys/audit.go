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
	CallerID   string       // renamed from Caller
	Source     CallerSource // source of the caller
	Verb       string
	ArgsJSON   []byte // renamed from Args json.RawMessage
	Phase      string // "start" or "done"
	Targets    []string // populated on phase=done
	OK         bool
	Error      string
	DurationMS int64 // populated on phase=done
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
