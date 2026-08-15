package wasmctl

import "testing"

func TestDeriveWasmName(t *testing.T) {
	cases := map[string]string{
		"dist/wasmmods/tint.wasm": "tint",
		"shieldregen.wasm":        "shieldregen",
		"/a/b/c.wasm":             "c",
	}
	for in, want := range cases {
		if got := deriveWasmName(in); got != want {
			t.Fatalf("deriveWasmName(%q)=%q want %q", in, got, want)
		}
	}
}
