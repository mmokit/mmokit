package cmdsys

import (
	"strings"
	"testing"
)

// helpTestCommand returns a Command with a representative shape used by
// the per-command help tests.
func helpTestCommand() Command {
	type splitArgs struct {
		CellID string `cmd:"help=target cell ID (e.g. \"0_0\")"`
		Bypass bool   `cmd:"named-only,optional,default=false,help=skip cooldown check"`
	}
	cmd := Command{
		Verb:        "cell.split",
		Capability:  "cell.split",
		Description: "split a cell into 4 children at +1 depth",
		Route:       RouteLocal,
		Args:        splitArgs{},
		Usage:       "cell split <cellID> [--bypass]",
		Examples: []string{
			"cell split 0_0",
			"cell split 0_0 --bypass",
		},
	}
	if h, err := schemaHashOf(cmd.Args); err == nil {
		cmd.ArgsSchemaHash = h
	}
	return cmd
}

func TestRenderHelp_BasicCommand(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(helpTestCommand()); err != nil {
		t.Fatalf("Register: %v", err)
	}
	out := RenderHelp(reg, "cell.split")

	if !strings.Contains(out, "cell split — split a cell into 4 children at +1 depth") {
		t.Errorf("missing title line. output:\n%s", out)
	}
	if !strings.Contains(out, "USAGE\n    cell split <cellID> [--bypass]") {
		t.Errorf("missing USAGE block. output:\n%s", out)
	}
	if !strings.Contains(out, "ARGUMENTS") {
		t.Errorf("missing ARGUMENTS section. output:\n%s", out)
	}
	if !strings.Contains(out, "CellID") {
		t.Errorf("expected positional CellID listed. output:\n%s", out)
	}
	if !strings.Contains(out, "FLAGS") {
		t.Errorf("missing FLAGS section. output:\n%s", out)
	}
	if !strings.Contains(out, "--bypass") {
		t.Errorf("expected --bypass flag listed. output:\n%s", out)
	}
	if !strings.Contains(out, "EXAMPLES") {
		t.Errorf("missing EXAMPLES section. output:\n%s", out)
	}
	if !strings.Contains(out, "cell split 0_0 --bypass") {
		t.Errorf("expected example line. output:\n%s", out)
	}
}

func TestRenderHelp_AutoUsage(t *testing.T) {
	type fooArgs struct {
		Target string `cmd:"help=target name"`
		Force  bool   `cmd:"named-only,optional,default=false,help=force operation"`
	}
	cmd := Command{
		Verb:        "foo",
		Capability:  "foo",
		Description: "do foo",
		Route:       RouteLocal,
		Args:        fooArgs{},
	}
	if h, err := schemaHashOf(cmd.Args); err == nil {
		cmd.ArgsSchemaHash = h
	}
	reg := NewRegistry()
	if err := reg.Register(cmd); err != nil {
		t.Fatalf("Register: %v", err)
	}
	out := RenderHelp(reg, "foo")
	want := "foo <target> [--force]"
	if !strings.Contains(out, want) {
		t.Errorf("expected auto-usage %q in output:\n%s", want, out)
	}
}

func TestRenderHelp_NoFlags_NoExamples(t *testing.T) {
	type emptyArgs struct{}
	cmd := Command{
		Verb:        "ping",
		Capability:  "ping",
		Description: "ping the server",
		Route:       RouteLocal,
		Args:        emptyArgs{},
	}
	if h, err := schemaHashOf(cmd.Args); err == nil {
		cmd.ArgsSchemaHash = h
	}
	reg := NewRegistry()
	if err := reg.Register(cmd); err != nil {
		t.Fatalf("Register: %v", err)
	}
	out := RenderHelp(reg, "ping")
	if strings.Contains(out, "ARGUMENTS") {
		t.Errorf("ARGUMENTS section should be elided. output:\n%s", out)
	}
	if strings.Contains(out, "FLAGS") {
		t.Errorf("FLAGS section should be elided. output:\n%s", out)
	}
	if strings.Contains(out, "EXAMPLES") {
		t.Errorf("EXAMPLES section should be elided. output:\n%s", out)
	}
	if !strings.Contains(out, "ping — ping the server") {
		t.Errorf("missing title. output:\n%s", out)
	}
	if !strings.Contains(out, "USAGE") {
		t.Errorf("USAGE section should always render. output:\n%s", out)
	}
}

func TestRenderHelp_Aliases(t *testing.T) {
	type emptyArgs struct{}
	cmd := Command{
		Verb:        "help",
		Capability:  "help",
		Description: "show help",
		Route:       RouteLocal,
		Args:        emptyArgs{},
		Aliases:     []string{"h", "?"},
	}
	if h, err := schemaHashOf(cmd.Args); err == nil {
		cmd.ArgsSchemaHash = h
	}
	reg := NewRegistry()
	if err := reg.Register(cmd); err != nil {
		t.Fatalf("Register: %v", err)
	}
	out := RenderHelp(reg, "help")
	if !strings.Contains(out, "Aliases: h, ?") {
		t.Errorf("missing aliases line. output:\n%s", out)
	}
}

func TestRenderHelp_EnumAndDefaults(t *testing.T) {
	type colorArgs struct {
		Color string `cmd:"enum=red|green|blue,help=output color"`
		Loud  bool   `cmd:"named-only,optional,default=true,help=verbose mode"`
	}
	cmd := Command{
		Verb:        "tone",
		Capability:  "tone",
		Description: "set tone",
		Route:       RouteLocal,
		Args:        colorArgs{},
	}
	if h, err := schemaHashOf(cmd.Args); err == nil {
		cmd.ArgsSchemaHash = h
	}
	reg := NewRegistry()
	if err := reg.Register(cmd); err != nil {
		t.Fatalf("Register: %v", err)
	}
	out := RenderHelp(reg, "tone")
	if !strings.Contains(out, "enum: red|green|blue") {
		t.Errorf("missing enum annotation. output:\n%s", out)
	}
	if !strings.Contains(out, "default: true") {
		t.Errorf("missing default annotation. output:\n%s", out)
	}
}

func TestRenderHelp_UnknownVerb(t *testing.T) {
	reg := NewRegistry()
	out := RenderHelp(reg, "doesnotexist")
	if !strings.Contains(out, "unknown command") {
		t.Errorf("expected unknown command message. output:\n%s", out)
	}
}

func TestRenderHelp_Group(t *testing.T) {
	type spawnArgs struct {
		Count  int    `cmd:"help=number of bots"`
		CellID string `cmd:"help=target cell ID"`
	}
	type clearArgs struct{}
	reg := NewRegistry()
	cmd1 := Command{Verb: "bot.spawn", Capability: "bot.spawn",
		Description: "spawn N bots in a cell", Route: RouteLocal, Args: spawnArgs{}}
	if h, err := schemaHashOf(cmd1.Args); err == nil {
		cmd1.ArgsSchemaHash = h
	}
	cmd2 := Command{Verb: "bot.clear", Capability: "bot.clear",
		Description: "remove all bots", Route: RouteLocal, Args: clearArgs{}}
	if h, err := schemaHashOf(cmd2.Args); err == nil {
		cmd2.ArgsSchemaHash = h
	}
	if err := reg.Register(cmd1); err != nil {
		t.Fatalf("Register bot.spawn: %v", err)
	}
	if err := reg.Register(cmd2); err != nil {
		t.Fatalf("Register bot.clear: %v", err)
	}
	out := RenderHelp(reg, "bot")
	if !strings.Contains(out, "bot commands:") {
		t.Errorf("missing group header. output:\n%s", out)
	}
	if !strings.Contains(out, "bot spawn") || !strings.Contains(out, "bot clear") {
		t.Errorf("missing sub-verb listing (interactive renderers display verbs with spaces, not dots). output:\n%s", out)
	}
	if strings.Contains(out, "bot.spawn") || strings.Contains(out, "bot.clear") {
		t.Errorf("interactive renderer leaked dot-notation; expected spaces. output:\n%s", out)
	}
	if !strings.Contains(out, "spawn N bots in a cell") {
		t.Errorf("missing sub-verb description. output:\n%s", out)
	}
}

func TestRenderHelp_GroupShimWithSubcommands(t *testing.T) {
	type subArgs struct {
		Sub string `cmd:"optional,rest"`
	}
	type catArgs struct {
		Category string
	}
	reg := NewRegistry()
	shim := Command{Verb: "log", Capability: "log",
		Description: "manage log categories", Route: RouteLocal, Args: subArgs{}}
	if h, err := schemaHashOf(shim.Args); err == nil {
		shim.ArgsSchemaHash = h
	}
	statusCmd := Command{Verb: "log.status", Capability: "log.status",
		Description: "show log categories on/off", Route: RouteLocal, Args: subArgs{}}
	if h, err := schemaHashOf(statusCmd.Args); err == nil {
		statusCmd.ArgsSchemaHash = h
	}
	onCmd := Command{Verb: "log.on", Capability: "log.on",
		Description: "enable a log category", Route: RouteLocal, Args: catArgs{}}
	if h, err := schemaHashOf(onCmd.Args); err == nil {
		onCmd.ArgsSchemaHash = h
	}
	if err := reg.Register(shim); err != nil {
		t.Fatalf("Register log: %v", err)
	}
	if err := reg.Register(statusCmd); err != nil {
		t.Fatalf("Register log.status: %v", err)
	}
	if err := reg.Register(onCmd); err != nil {
		t.Fatalf("Register log.on: %v", err)
	}
	out := RenderHelp(reg, "log")
	if !strings.Contains(out, "log — manage log categories") {
		t.Errorf("missing per-command title. output:\n%s", out)
	}
	if !strings.Contains(out, "SUBCOMMANDS") {
		t.Errorf("missing SUBCOMMANDS section. output:\n%s", out)
	}
	if !strings.Contains(out, "log status") || !strings.Contains(out, "log on") {
		t.Errorf("missing sub-verb entries (display form is space-separated). output:\n%s", out)
	}
	if strings.Contains(out, "log.status") || strings.Contains(out, "log.on") {
		t.Errorf("interactive renderer leaked dot-notation. output:\n%s", out)
	}
}
