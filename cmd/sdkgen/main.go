// cmd/sdkgen generates typed TypeScript client SDKs from protocol schema JSON.
//
// Usage:
//
//	go run ./examples/4node-basic --dump-schema | go run ./cmd/sdkgen --out examples/4node-basic/web/sdk
//	# or:
//	go run ./cmd/sdkgen --schema protocol-schema.json --out examples/4node-basic/web/sdk
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
	protoESRoot := flag.String("proto-es", "gen/es", "Root of proto-es generated files (for .d.ts parsing)")
	coreTS := flag.String("core", "pkg/quantize/ts/delta-decoder-core.ts", "Path to delta-decoder-core.ts to copy")
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

	// Parse proto .d.ts files to get message field info.
	msgDB := parseProtoES(*protoESRoot)

	// Generate SDK files.
	g := &Generator{
		schema: schema,
		msgs:   msgDB,
		outDir: *outDir,
	}

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(*outDir, "_core"), 0o755); err != nil {
		log.Fatalf("mkdir: %v", err)
	}

	// Copy core runtime.
	if err := copyFile(*coreTS, filepath.Join(*outDir, "_core", "delta-decoder-core.ts")); err != nil {
		log.Fatalf("copy core: %v", err)
	}

	// Generate each file.
	files := map[string]func() string{
		"transport.ts":     g.genTransport,
		"entities.ts":      g.genEntities,
		"delta-decoder.ts": g.genDeltaDecoder,
		"client.ts":        g.genClient,
		"index.ts":         g.genIndex,
	}
	for name, fn := range files {
		path := filepath.Join(*outDir, name)
		content := fn()
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
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

// snakeToTitle converts "move_target" to "MoveTarget".
func snakeToTitle(s string) string {
	parts := strings.Split(s, "_")
	for i := range parts {
		parts[i] = titleCase(parts[i])
	}
	return strings.Join(parts, "")
}
