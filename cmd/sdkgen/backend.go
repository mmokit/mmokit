package main

import "fmt"

// CoreFile is a runtime file copied verbatim into <out>/_core/.
type CoreFile struct {
	Src string // path on disk to copy from
	Dst string // filename under _core/
}

// Backend is a language-specific SDK emitter. main.go is language-agnostic:
// it decodes the schema, asks the backend for its core files and its
// schema-filtered output file set, and writes them.
type Backend interface {
	// Lang is the --lang token this backend handles ("ts", "csharp").
	Lang() string
	// CoreFiles lists runtime files to copy verbatim into <out>/_core/.
	CoreFiles() []CoreFile
	// OutputFiles returns filename -> content-generator for the given
	// schema, already filtered to what the schema needs (e.g. no
	// broadcasts file when the schema declares none).
	OutputFiles(schema ProtocolSchema) map[string]func() string
}

// backendFor selects a backend by --lang token. The C# backend is added in
// a later plan; until then it returns a clear not-implemented error so the
// flag is wired and the message is specific.
func backendFor(lang, coreTS, interpTS string) (Backend, error) {
	switch lang {
	case "ts":
		return tsBackend{coreTS: coreTS, interpTS: interpTS}, nil
	case "csharp":
		return nil, fmt.Errorf("--lang=csharp: C# backend not yet implemented")
	default:
		return nil, fmt.Errorf("unknown --lang %q (want: ts, csharp)", lang)
	}
}
