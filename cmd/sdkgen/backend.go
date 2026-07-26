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

// backendOpts carries the language-specific knobs main.go derives from flags.
type backendOpts struct {
	CoreTS         string // TS: delta-decoder-core.ts source path
	InterpTS       string // TS: interpolation-core.ts source path
	ClockSyncTS    string // TS: clock-sync.ts source path
	InterpBufferTS string // TS: interpolation-buffer.ts source path
	PlaybackTS     string // TS: playback-controller.ts source path
	PredictionTS   string // TS: prediction-buffer.ts source path
	CSharpCoreDir  string // C#: dir holding the hand-written _core .cs files
	Namespace      string // C#: root namespace for generated files
}

// backendFor selects a backend by --lang token.
func backendFor(lang string, o backendOpts) (Backend, error) {
	switch lang {
	case "ts":
		return tsBackend{
			coreTS: o.CoreTS, interpTS: o.InterpTS, clockSyncTS: o.ClockSyncTS,
			interpBufferTS: o.InterpBufferTS, playbackTS: o.PlaybackTS, predictionTS: o.PredictionTS,
		}, nil
	case "csharp":
		ns := o.Namespace
		if ns == "" {
			ns = "Mmokit.Sdk"
		}
		return csharpBackend{namespace: ns, coreDir: o.CSharpCoreDir}, nil
	default:
		return nil, fmt.Errorf("unknown --lang %q (want: ts, csharp)", lang)
	}
}
