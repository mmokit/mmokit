package cmdsys

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// AuditRecord describes a single command lifecycle event.
type AuditRecord struct {
	Time    time.Time
	TraceID string
	Phase   string // "start" or "done"
	Verb    string
	Caller  string
	OK      bool
	Error   string
	Args    json.RawMessage
}

// AuditSink receives audit records. Implementations must be goroutine-safe.
type AuditSink interface {
	Record(r AuditRecord)
}

// StderrAuditSink writes audit records to stderr as single-line JSON.
type StderrAuditSink struct{}

func (StderrAuditSink) Record(r AuditRecord) {
	b, _ := json.Marshal(r)
	fmt.Fprintf(os.Stderr, "cmdsys audit: %s\n", b)
}

// NoopAuditSink discards all records.
type NoopAuditSink struct{}

func (NoopAuditSink) Record(AuditRecord) {}
