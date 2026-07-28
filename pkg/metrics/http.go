package metrics

import (
	"fmt"
	"net/http"
	"sort"
)

// Handler returns an HTTP handler that serves Prometheus-compatible text
// exposition format metrics.
//
// snapshotFn is called on each scrape for the per-cell load snapshots.
// processFn is called on each scrape for the counters that belong to the
// process rather than to a cell; it may be nil, which omits those families.
// Both halves are needed because a process's roles decide which it has: a
// host-role process owns cells and no client ingress, a gateway-role-only
// process owns client ingress and no cells. Feeding the handler only
// snapshotFn is what made a gateway's /metrics a body of header comments with
// no samples in it at all.
//
// No Prometheus client library dependency — hand-written text format.
// Games can replace this with a real Prometheus registry if desired.
func Handler(snapshotFn func() map[string]LoadSnapshot, processFn func() ProcessSnapshot) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		snapshots := snapshotFn()

		// Sort node IDs for deterministic output.
		nodeIDs := make([]string, 0, len(snapshots))
		for id := range snapshots {
			nodeIDs = append(nodeIDs, id)
		}
		sort.Strings(nodeIDs)

		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

		// Tick duration percentiles
		fmt.Fprintln(w, "# HELP mmokit_tick_duration_seconds Tick duration in seconds")
		fmt.Fprintln(w, "# TYPE mmokit_tick_duration_seconds gauge")
		for _, id := range nodeIDs {
			s := snapshots[id]
			fmt.Fprintf(w, "mmokit_tick_duration_seconds{node=%q,quantile=\"0.5\"} %g\n", id, s.Tick.AvgDuration.Seconds())
			fmt.Fprintf(w, "mmokit_tick_duration_seconds{node=%q,quantile=\"0.95\"} %g\n", id, s.Tick.P95Duration.Seconds())
			fmt.Fprintf(w, "mmokit_tick_duration_seconds{node=%q,quantile=\"0.99\"} %g\n", id, s.Tick.P99Duration.Seconds())
		}

		// Effective tick rate
		fmt.Fprintln(w, "# HELP mmokit_tick_rate_hz Effective tick rate")
		fmt.Fprintln(w, "# TYPE mmokit_tick_rate_hz gauge")
		for _, id := range nodeIDs {
			fmt.Fprintf(w, "mmokit_tick_rate_hz{node=%q} %g\n", id, snapshots[id].Tick.EffectiveHz)
		}

		// Overbudget ratio
		fmt.Fprintln(w, "# HELP mmokit_tick_overbudget_ratio Fraction of ticks exceeding budget")
		fmt.Fprintln(w, "# TYPE mmokit_tick_overbudget_ratio gauge")
		for _, id := range nodeIDs {
			fmt.Fprintf(w, "mmokit_tick_overbudget_ratio{node=%q} %g\n", id, snapshots[id].Tick.OverbudgetPct)
		}

		// Entity counts
		fmt.Fprintln(w, "# HELP mmokit_entities Entity count by type")
		fmt.Fprintln(w, "# TYPE mmokit_entities gauge")
		for _, id := range nodeIDs {
			s := snapshots[id]
			fmt.Fprintf(w, "mmokit_entities{node=%q,type=\"real\"} %d\n", id, s.Entities.Real)
			fmt.Fprintf(w, "mmokit_entities{node=%q,type=\"replica\"} %d\n", id, s.Entities.Replica)
			fmt.Fprintf(w, "mmokit_entities{node=%q,type=\"ghost\"} %d\n", id, s.Entities.Ghost)
		}

		// Connected entities
		fmt.Fprintln(w, "# HELP mmokit_connected Connected entity count")
		fmt.Fprintln(w, "# TYPE mmokit_connected gauge")
		for _, id := range nodeIDs {
			fmt.Fprintf(w, "mmokit_connected{node=%q} %d\n", id, snapshots[id].Entities.Connected)
		}

		// Connections
		fmt.Fprintln(w, "# HELP mmokit_connections Active connection count")
		fmt.Fprintln(w, "# TYPE mmokit_connections gauge")
		for _, id := range nodeIDs {
			fmt.Fprintf(w, "mmokit_connections{node=%q} %d\n", id, snapshots[id].Network.Connections)
		}

		// Bytes sent/recv (counters)
		fmt.Fprintln(w, "# HELP mmokit_bytes_sent_total Total bytes sent")
		fmt.Fprintln(w, "# TYPE mmokit_bytes_sent_total counter")
		for _, id := range nodeIDs {
			fmt.Fprintf(w, "mmokit_bytes_sent_total{node=%q} %d\n", id, snapshots[id].Network.BytesSent)
		}
		fmt.Fprintln(w, "# HELP mmokit_bytes_recv_total Total bytes received")
		fmt.Fprintln(w, "# TYPE mmokit_bytes_recv_total counter")
		for _, id := range nodeIDs {
			fmt.Fprintf(w, "mmokit_bytes_recv_total{node=%q} %d\n", id, snapshots[id].Network.BytesRecv)
		}

		// Composite load score
		fmt.Fprintln(w, "# HELP mmokit_node_load Composite load score (0=idle, 1=at budget, >1=overloaded)")
		fmt.Fprintln(w, "# TYPE mmokit_node_load gauge")
		for _, id := range nodeIDs {
			fmt.Fprintf(w, "mmokit_node_load{node=%q} %g\n", id, snapshots[id].CompositeLoad)
		}

		if processFn == nil {
			return
		}
		writeProcessMetrics(w, processFn())
	}
}

// writeProcessMetrics emits the process-scoped families. Split out of Handler
// so the cell-scoped and process-scoped halves stay independently readable.
//
// Every ingress series is emitted on every scrape, zeros included: the label
// sets are a fixed 2x12 table, so the output size is constant regardless of how
// much hostile traffic the process has taken, and an alert can tell "no
// rejections" from "this build has no such counter".
func writeProcessMetrics(w http.ResponseWriter, p ProcessSnapshot) {
	fmt.Fprintln(w, "# HELP mmokit_ingress_rejected_total Inbound payloads refused, by reason and ingress surface")
	fmt.Fprintln(w, "# TYPE mmokit_ingress_rejected_total counter")
	for _, rej := range p.Ingress.Rejections {
		fmt.Fprintf(w, "mmokit_ingress_rejected_total{reason=%q,surface=%q} %d\n",
			rej.Reason.String(), rej.Surface.String(), rej.Count)
	}

	// The UDP listener's packet-level refusals are only meaningful when a
	// listener is bound; emitting zeros from every non-gateway process would
	// claim a clean UDP surface on a process that has none.
	if !p.UDP.Bound {
		return
	}
	fmt.Fprintln(w, "# HELP mmokit_udp_packets_dropped_total Client UDP packets refused before any frame was decoded")
	fmt.Fprintln(w, "# TYPE mmokit_udp_packets_dropped_total counter")
	fmt.Fprintf(w, "mmokit_udp_packets_dropped_total{reason=\"source_mismatch\"} %d\n", p.UDP.SourceMismatchDrops)
	fmt.Fprintf(w, "mmokit_udp_packets_dropped_total{reason=\"capacity\"} %d\n", p.UDP.CapacityDrops)
	fmt.Fprintf(w, "mmokit_udp_packets_dropped_total{reason=\"pending_full\"} %d\n", p.UDP.PendingFullDrops)

	fmt.Fprintln(w, "# HELP mmokit_udp_pending_handshakes Unproven UDP handshakes awaiting a return-routability packet")
	fmt.Fprintln(w, "# TYPE mmokit_udp_pending_handshakes gauge")
	fmt.Fprintf(w, "mmokit_udp_pending_handshakes %d\n", p.UDP.PendingCount)
}
