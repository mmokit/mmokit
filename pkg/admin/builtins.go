package admin

// RegisterBuiltinPanels registers the v1 MVP panels declared in the spec §8.
// Returns the first registration error (e.g. duplicate ID); returns nil on
// success. Idempotent on first call only — calling twice on the same registry
// returns the duplicate-ID error from the second invocation.
func RegisterBuiltinPanels(r *PanelRegistry) error {
	defs := []PanelDef{
		{ID: "cluster", Label: "Cluster", Icon: "globe", Group: "Cluster",
			Topics:       []string{"cells", "topology"},
			Commands:     []string{"cell.split", "cell.merge", "cell.migrate"},
			InitialFetch: "/admin/api/cluster"},
		{ID: "hosts", Label: "Hosts", Icon: "server", Group: "Cluster",
			Topics:       []string{"hosts"},
			InitialFetch: "/admin/api/hosts"},
		{ID: "gateways", Label: "Gateways", Icon: "git-branch", Group: "Cluster",
			Topics:       []string{"sessions"},
			InitialFetch: "/admin/api/gateways"},
		{ID: "players", Label: "Players", Icon: "users", Group: "People",
			Topics:       []string{"players"},
			Commands:     []string{"player.tp", "player.tpto", "player.kick", "player.info"},
			InitialFetch: "/admin/api/players"},
		{ID: "performance", Label: "Performance", Icon: "activity", Group: "Diagnose",
			Topics: []string{"cells", "hosts"}},
		{ID: "events", Label: "Events", Icon: "list", Group: "Diagnose",
			Topics:       []string{"events", "alerts"},
			InitialFetch: "/admin/api/events?n=100"},
		{ID: "logs", Label: "Logs", Icon: "scroll", Group: "Diagnose"},
		{ID: "settings", Label: "Settings", Icon: "settings", Group: "Config"},
	}
	for _, d := range defs {
		if err := r.Register(d); err != nil {
			return err
		}
	}
	return nil
}
