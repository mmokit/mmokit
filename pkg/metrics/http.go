package metrics

import (
	"fmt"
	"net/http"
	"sort"
)

// Handler returns an HTTP handler that serves Prometheus-compatible text
// exposition format metrics. snapshotFn is called on each scrape to get
// current node load snapshots.
//
// No Prometheus client library dependency — hand-written text format.
// Games can replace this with a real Prometheus registry if desired.
func Handler(snapshotFn func() map[string]LoadSnapshot) http.HandlerFunc {
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
	}
}
