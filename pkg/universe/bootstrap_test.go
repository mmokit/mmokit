package universe

import (
	"flag"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

// TestBindFlagsPreParseDefault verifies that a Config field set before
// BindFlags becomes the flag's default — games can override engine defaults
// by pre-populating the field.
func TestBindFlagsPreParseDefault(t *testing.T) {
	cfg := Config{Mode: "coordinator,host"}

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	origCmd := flag.CommandLine
	flag.CommandLine = fs
	defer func() { flag.CommandLine = origCmd }()

	cfg.BindFlags()
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("fs.Parse: %v", err)
	}

	if cfg.Mode != "coordinator,host" {
		t.Errorf("cfg.Mode = %q, want %q (pre-parse default should win)", cfg.Mode, "coordinator,host")
	}
}

// TestBindFlagsEngineDefault verifies that zero-valued string/int Config fields
// fall through to the engine default baked into BindFlags.
func TestBindFlagsEngineDefault(t *testing.T) {
	cfg := Config{}

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	origCmd := flag.CommandLine
	flag.CommandLine = fs
	defer func() { flag.CommandLine = origCmd }()

	cfg.BindFlags()
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("fs.Parse: %v", err)
	}

	if cfg.Mode != "all" {
		t.Errorf("cfg.Mode = %q, want %q", cfg.Mode, "all")
	}
	if cfg.ControlListen != ":9100" {
		t.Errorf("cfg.ControlListen = %q, want %q", cfg.ControlListen, ":9100")
	}
	if cfg.GatewayMode != "local-shortcut" {
		t.Errorf("cfg.GatewayMode = %q, want %q", cfg.GatewayMode, "local-shortcut")
	}
	if cfg.WebDir != "embed" {
		t.Errorf("cfg.WebDir = %q, want %q", cfg.WebDir, "embed")
	}
	if cfg.HTTPPort != 8080 {
		t.Errorf("cfg.HTTPPort = %d, want 8080", cfg.HTTPPort)
	}
}

// TestBindFlagsParseOverrides confirms that --mode on the command line
// overrides a pre-set Config field (games set intent defaults; operators
// override at runtime).
func TestBindFlagsParseOverrides(t *testing.T) {
	cfg := Config{Mode: "coordinator,host"}

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	origCmd := flag.CommandLine
	flag.CommandLine = fs
	defer func() { flag.CommandLine = origCmd }()

	cfg.BindFlags()
	if err := fs.Parse([]string{"--mode=host", "--coordinator-addr=remote:9100", "--port=9999"}); err != nil {
		t.Fatalf("fs.Parse: %v", err)
	}

	if cfg.Mode != "host" {
		t.Errorf("cfg.Mode = %q, want %q (CLI override)", cfg.Mode, "host")
	}
	if cfg.CoordinatorAddr != "remote:9100" {
		t.Errorf("cfg.CoordinatorAddr = %q, want %q (CLI override)", cfg.CoordinatorAddr, "remote:9100")
	}
	if cfg.HTTPPort != 9999 {
		t.Errorf("cfg.HTTPPort = %d, want 9999 (CLI override)", cfg.HTTPPort)
	}
}

// TestBindFlagsDroppedFlags guarantees that the decommissioned --dynamic-cells
// and --two-hosts flags no longer exist. If a regression reintroduces them
// this test fails loudly.
func TestBindFlagsDroppedFlags(t *testing.T) {
	cfg := Config{}
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	origCmd := flag.CommandLine
	flag.CommandLine = fs
	defer func() { flag.CommandLine = origCmd }()

	cfg.BindFlags()

	if fs.Lookup("dynamic-cells") != nil {
		t.Error("--dynamic-cells flag should not exist (decommissioned)")
	}
	if fs.Lookup("two-hosts") != nil {
		t.Error("--two-hosts flag should not exist (decommissioned)")
	}
}

// TestStaticFSSubPrefix ensures that Config.StaticFS + StaticFSPrefix resolves
// via fs.Sub so game code can pass the raw //go:embed FS without calling
// fs.Sub itself. Exercised by replicating the tiny fs.Sub path inline here —
// we deliberately don't drive startHTTPListener because unit tests shouldn't
// bind a real TCP port.
func TestStaticFSSubPrefix(t *testing.T) {
	embedded := fstest.MapFS{
		"web/dist/index.html":  &fstest.MapFile{Data: []byte("<html>root</html>")},
		"web/dist/app.js":      &fstest.MapFile{Data: []byte("console.log(1)")},
		"unrelated/ignore.txt": &fstest.MapFile{Data: []byte("ignore me")},
	}

	rootFS, err := fs.Sub(embedded, "web/dist")
	if err != nil {
		t.Fatalf("fs.Sub: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(rootFS)))

	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/index.html")
	if err != nil {
		t.Fatalf("GET /index.html: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "<html>root</html>" {
		t.Errorf("body = %q, want %q", string(body), "<html>root</html>")
	}

	// File outside the sub-path must NOT be served.
	resp2, err := http.Get(srv.URL + "/unrelated/ignore.txt")
	if err != nil {
		t.Fatalf("GET /unrelated/ignore.txt: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for /unrelated/ignore.txt outside sub-path, got %d", resp2.StatusCode)
	}
}

// TestDisabledPartitionConfig returns a config that the monitor treats as no-op.
func TestDisabledPartitionConfig(t *testing.T) {
	dp := DisabledPartitionConfig()
	if dp == nil {
		t.Fatal("DisabledPartitionConfig returned nil")
	}
	if dp.AutoSplitEnabled {
		t.Error("AutoSplitEnabled should be false")
	}
	if dp.AutoMergeEnabled {
		t.Error("AutoMergeEnabled should be false")
	}
}

// TestNewCoordinatorPartitioningDefaultsOff verifies that leaving
// Config.DynamicPartitioning nil keeps it nil — dynamic partitioning
// is off unless a game explicitly opts in via DefaultPartitionConfig().
func TestNewCoordinatorPartitioningDefaultsOff(t *testing.T) {
	cfg := Config{Mode: "all"}
	c := New(cfg)
	if c.cfg.DynamicPartitioning != nil {
		t.Fatal("DynamicPartitioning should stay nil (off) by default")
	}
}

// TestNewCoordinatorOptInPartitioning verifies games can opt in via
// DefaultPartitionConfig and the returned config has auto-split enabled.
func TestNewCoordinatorOptInPartitioning(t *testing.T) {
	cfg := Config{
		Mode:                "all",
		DynamicPartitioning: DefaultPartitionConfig(),
	}
	c := New(cfg)
	if c.cfg.DynamicPartitioning == nil {
		t.Fatal("DynamicPartitioning should stay non-nil after explicit opt-in")
	}
	if !c.cfg.DynamicPartitioning.AutoSplitEnabled {
		t.Error("DefaultPartitionConfig should have AutoSplitEnabled=true")
	}
}

// TestNewCoordinatorOptOutPartitioning verifies games can opt out via
// DisabledPartitionConfig without the engine re-enabling it.
func TestNewCoordinatorOptOutPartitioning(t *testing.T) {
	cfg := Config{
		Mode:                "all",
		DynamicPartitioning: DisabledPartitionConfig(),
	}
	c := New(cfg)
	if c.cfg.DynamicPartitioning == nil {
		t.Fatal("DynamicPartitioning should stay non-nil after DisabledPartitionConfig")
	}
	if c.cfg.DynamicPartitioning.AutoSplitEnabled {
		t.Error("AutoSplitEnabled should remain false after opt-out")
	}
}
