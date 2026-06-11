package universe

import (
	"reflect"
	"testing"
)

func TestWSAllowedOrigins(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want []string
	}{
		{
			name: "explicit wins",
			cfg:  Config{AllowedWSOrigins: []string{"app.example.com"}, CORSOrigins: "http://other.example.com"},
			want: []string{"app.example.com"},
		},
		{
			name: "falls back to CORS when unset",
			cfg:  Config{CORSOrigins: "http://localhost:5174"},
			want: []string{"http://localhost:5174"},
		},
		{
			name: "CORS fallback splits + trims multiple",
			cfg:  Config{CORSOrigins: "http://localhost:5174, https://app.example.com "},
			want: []string{"http://localhost:5174", "https://app.example.com"},
		},
		{
			name: "both empty => nil (same-origin only)",
			cfg:  Config{},
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := wsAllowedOrigins(tc.cfg)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("wsAllowedOrigins(%+v) = %v, want %v", tc.cfg, got, tc.want)
			}
		})
	}
}
