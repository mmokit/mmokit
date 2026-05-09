package admin

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDTOs_CamelCaseJSON(t *testing.T) {
	t.Parallel()
	c := CellInfo{ID: "0_0", Depth: 1, HostID: "host-a", Load: 0.5, TickP99Us: 1234}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{`"id":"0_0"`, `"hostId":"host-a"`, `"tickP99Us":1234`} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s in %s", want, got)
		}
	}
}
