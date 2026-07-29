package universe

import (
	"github.com/zenion/mmoserver/pkg/cmdsys"
	"github.com/zenion/mmoserver/pkg/logger"
)

// logAuditSink routes cmdsys audit records through the process logger under
// CatCmdsysAudit.
//
// It replaces the NoopAuditSink the dispatcher shipped with, which meant a
// mesh-delivered command left no trace anywhere: InvokeLocal emitted nothing,
// and pkg/admin's audit ring is fed only by its HTTP handlers. "Caller.ID is
// preserved for audit" was satisfied by a field nobody read.
//
// Records are logged rather than pushed into the admin ring because
// InvokeAsPeer runs on hosts and gateways, while the ring lives on the
// coordinator — surfacing them there needs a transport of its own.
//
// Residual: the category is registered but not in StartupCategories, so the
// trail is one --log=cmdsys:audit away rather than on by default. That is a
// deliberate noise trade for a per-command line, not an oversight.
type logAuditSink struct {
	log *logger.Logger
}

func (s logAuditSink) Emit(r cmdsys.AuditRecord) {
	if s.log == nil {
		return
	}
	// Only the terminal record is worth a line; the "start" phase doubles
	// every entry and carries nothing the "done" phase lacks except args.
	if r.Phase != "done" {
		return
	}
	via := "local"
	if r.PeerID != "" {
		via = "peer=" + r.PeerID
	}
	if r.OK {
		s.log.Log(CatCmdsysAudit, "cmdsys: %s caller=%q src=%d %s ok in %dms",
			r.Verb, r.CallerID, r.Source, via, r.DurationMS)
		return
	}
	s.log.Log(CatCmdsysAudit, "cmdsys: %s caller=%q src=%d %s FAILED %s: %s",
		r.Verb, r.CallerID, r.Source, via, r.Error, r.Detail)
}
