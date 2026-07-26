package main

// tsBackend emits the TypeScript SDK. It delegates content generation to
// the existing *Generator; the file-selection logic here mirrors exactly
// what main.go did before the backend seam was introduced.
type tsBackend struct {
	coreTS         string // path to delta-decoder-core.ts
	interpTS       string // path to interpolation-core.ts
	clockSyncTS    string // path to clock-sync.ts
	interpBufferTS string // path to interpolation-buffer.ts
	playbackTS     string // path to playback-controller.ts
	predictionTS   string // path to prediction-buffer.ts
}

func (b tsBackend) Lang() string { return "ts" }

func (b tsBackend) CoreFiles() []CoreFile {
	return []CoreFile{
		{Src: b.coreTS, Dst: "delta-decoder-core.ts"},
		{Src: b.interpTS, Dst: "interpolation-core.ts"},
		{Src: b.clockSyncTS, Dst: "clock-sync.ts"},
		{Src: b.interpBufferTS, Dst: "interpolation-buffer.ts"},
		{Src: b.playbackTS, Dst: "playback-controller.ts"},
		{Src: b.predictionTS, Dst: "prediction-buffer.ts"},
	}
}

func (b tsBackend) OutputFiles(schema ProtocolSchema) map[string]func() string {
	g := &Generator{schema: schema}
	files := map[string]func() string{
		"transport.ts":     g.genTransport,
		"entities.ts":      g.genEntities,
		"delta-decoder.ts": g.genDeltaDecoder,
		"client.ts":        g.genClient,
		"index.ts":         g.genIndex,
	}
	// broadcasts.ts holds both HandleAll[T] broadcast classes AND
	// RegisterEvent[T] typed-server-event classes — same wire layout, same
	// dispatcher path. Emit when either registry is non-empty.
	if len(schema.BroadcastTypes) > 0 || len(schema.ServerEventTypes) > 0 {
		files["broadcasts.ts"] = g.genBroadcasts
	}
	// inputs.ts mirrors broadcasts.ts but for client-input messages.
	if len(schema.ClientInputTypes) > 0 {
		files["inputs.ts"] = g.genInputs
	}
	// operations.ts holds RegisterOp[Req, Res] Request + Response classes.
	if len(schema.Operations) > 0 {
		files["operations.ts"] = g.genOperations
	}
	// entityType.ts holds the EntityType const block (one entry per kind).
	if len(schema.Entities) > 0 {
		files["entityType.ts"] = g.genEntityType
	}
	return files
}
