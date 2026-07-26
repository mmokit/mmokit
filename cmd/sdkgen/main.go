// cmd/sdkgen generates typed TypeScript client SDKs from protocol schema JSON.
//
// Usage:
//
//	go run ./examples/4node-basic --dump-schema | go run ./cmd/sdkgen --out examples/4node-basic/web/sdk
//	# or:
//	go run ./cmd/sdkgen --schema protocol-schema.json --out examples/4node-basic/web/sdk
//	# select the target language (default ts):
//	go run ./cmd/sdkgen --lang=ts --schema protocol-schema.json --out sdk
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	schemaFile := flag.String("schema", "", "Path to protocol-schema.json (reads stdin if empty)")
	outDir := flag.String("out", "sdk", "Output directory for generated SDK")
	lang := flag.String("lang", "ts", "Target language for the generated SDK (ts, csharp)")
	coreTS := flag.String("core", "pkg/quantize/ts/delta-decoder-core.ts", "Path to delta-decoder-core.ts to copy")
	interpTS := flag.String("interp", "pkg/quantize/ts/interpolation-core.ts", "Path to interpolation-core.ts to copy")
	clockSyncTS := flag.String("clock-sync", "pkg/quantize/ts/clock-sync.ts", "Path to clock-sync.ts to copy")
	interpBufferTS := flag.String("interp-buffer", "pkg/quantize/ts/interpolation-buffer.ts", "Path to interpolation-buffer.ts to copy")
	playbackTS := flag.String("playback", "pkg/quantize/ts/playback-controller.ts", "Path to playback-controller.ts to copy")
	predictionTS := flag.String("prediction", "pkg/quantize/ts/prediction-buffer.ts", "Path to prediction-buffer.ts to copy")
	reconGateTS := flag.String("recon-gate", "pkg/quantize/ts/reconciliation-gate.ts", "Path to reconciliation-gate.ts to copy")
	namespace := flag.String("namespace", "Mmokit.Sdk", "C#: root namespace for generated files")
	csharpCore := flag.String("csharp-core", "csharp/Mmokit.Sdk.Core", "C#: dir holding the hand-written _core .cs sources")
	flag.Parse()

	// Read schema.
	var r io.Reader = os.Stdin
	if *schemaFile != "" {
		f, err := os.Open(*schemaFile)
		if err != nil {
			log.Fatalf("open schema: %v", err)
		}
		defer f.Close()
		r = f
	}

	var schema ProtocolSchema
	if err := json.NewDecoder(r).Decode(&schema); err != nil {
		log.Fatalf("decode schema: %v", err)
	}

	// Select the language backend.
	backend, err := backendFor(*lang, backendOpts{
		CoreTS:         *coreTS,
		InterpTS:       *interpTS,
		ClockSyncTS:    *clockSyncTS,
		InterpBufferTS: *interpBufferTS,
		PlaybackTS:     *playbackTS,
		PredictionTS:   *predictionTS,
		ReconGateTS:    *reconGateTS,
		CSharpCoreDir:  *csharpCore,
		Namespace:      *namespace,
	})
	if err != nil {
		log.Fatalf("sdkgen: %v", err)
	}

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(*outDir, "_core"), 0o755); err != nil {
		log.Fatalf("mkdir: %v", err)
	}

	// Copy core runtime files.
	for _, cf := range backend.CoreFiles() {
		if err := copyFile(cf.Src, filepath.Join(*outDir, "_core", cf.Dst)); err != nil {
			log.Fatalf("copy core %s: %v", cf.Dst, err)
		}
	}

	// Generate each output file.
	for name, fn := range backend.OutputFiles(schema) {
		path := filepath.Join(*outDir, name)
		if err := os.WriteFile(path, []byte(fn()), 0o644); err != nil {
			log.Fatalf("write %s: %v", name, err)
		}
		fmt.Printf("  %s\n", path)
	}
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

// titleCase converts "playerSpawned" to "PlayerSpawned".
func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
