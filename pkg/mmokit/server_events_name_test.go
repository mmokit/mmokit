package mmokit

import "testing"

func TestDeriveEventName(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"SE_PLAYER_SPAWNED", "playerSpawned"},
		{"GSE_BANK_CONTENTS", "bankContents"},
		{"SE_CELL_TOPOLOGY", "cellTopology"},
		{"GSE_CURRENCY_UPDATE", "currencyUpdate"},
		{"SE_PONG", "pong"},
		{"SSE_LEADERBOARD", "leaderboard"},
		{"BCE_LOGIN", "login"},
		{"CE_PING", "ping"},
		{"PLAYER_SPAWNED", "playerSpawned"}, // no prefix
		{"SE_X", "x"},                       // single segment
		{"_FOO_BAR", "fooBar"},              // empty first segment after split — first non-empty must lowercase
		{"SE__DOUBLE", "double"},            // double underscore — empty middle segment must skip cleanly
	}
	for _, tc := range cases {
		if got := deriveEventName(tc.in); got != tc.want {
			t.Errorf("deriveEventName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
