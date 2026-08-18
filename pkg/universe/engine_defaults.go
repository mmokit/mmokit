package universe

// EngineDefaultClientHandlers is the import-cycle indirection that lets
// universe.New register engine-default HandleClient handlers (Ping → Pong,
// and the explicit replication-frame ACK) without importing mmokit. mmokit's
// init() populates this callback; the closure calls mmokit.HandleClient
// against the given Process.
//
// When the hook is nil (a test that builds a Process without importing
// mmokit), engine-default client handlers are simply not installed — the
// stage works normally for everything except Ping.
//
// This is the one survivor of the five hook structs part B deleted, and it
// survives because it is not a registry read: it does not ask the facade a
// question, it asks the facade to REGISTER against a Process that has just
// been constructed. It goes away with the rest once mmokit.New bootstraps its
// own Process directly.
var EngineDefaultClientHandlers func(*Process)
