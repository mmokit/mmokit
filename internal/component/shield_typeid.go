package component

// ShieldTypeID is the stable WASM-ABI id for the Shield column. Host and
// guest both reference this constant so they agree on which column maps.
// Phase 1 codegen will derive ids automatically.
const ShieldTypeID uint32 = 1
