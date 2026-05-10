import type { Schema, FieldSchema } from "$lib/types";

// CoerceResult carries either the typed args object or per-field error
// messages. errors is keyed by field name; absent keys mean valid.
export type CoerceResult = {
  args: Record<string, unknown> | null;
  errors: Record<string, string>;
};

// defaultValueFor returns the string the form should pre-fill into the
// input for this field — either the schema's `default` literal or empty.
export function defaultValueFor(f: FieldSchema): string {
  return f.default || "";
}

// coerceArgs validates `values` against the schema and returns typed
// args ready for JSON.stringify. On any error returns args=null with
// per-field error messages.
export function coerceArgs(schema: Schema, values: Record<string, string>): CoerceResult {
  const errors: Record<string, string> = {};
  const out: Record<string, unknown> = {};
  for (const f of schema.fields) {
    const raw = values[f.name];
    const isEmpty = raw === undefined || raw === "";
    if (isEmpty) {
      if (f.required && !f.default) {
        errors[f.name] = `${f.name} is required`;
        continue;
      }
      const def = f.default;
      if (def === "" && !f.required) {
        // Skip optional empty-default fields entirely; backend uses Go zero value.
        continue;
      }
      out[f.name] = coerceOne(f, def, errors);
      continue;
    }
    out[f.name] = coerceOne(f, raw, errors);
  }
  if (Object.keys(errors).length > 0) {
    return { args: null, errors };
  }
  return { args: out, errors };
}

function coerceOne(f: FieldSchema, raw: string, errors: Record<string, string>): unknown {
  switch (f.kind) {
    case "string":
      return raw;
    case "bool":
      if (raw === "true" || raw === "1") return true;
      if (raw === "false" || raw === "0" || raw === "") return false;
      errors[f.name] = `${f.name} must be true or false`;
      return null;
    case "int":
    case "int32":
    case "int64":
    case "uint32":
    case "uint64": {
      if (!/^-?\d+$/.test(raw)) {
        errors[f.name] = `${f.name} must be an integer`;
        return null;
      }
      // We send as JSON numbers; backend uses json.Decoder on the typed Args
      // struct so the int kind is honored on receive. JS Number is fine for
      // values within Number.MAX_SAFE_INTEGER (~ 2^53).
      return Number.parseInt(raw, 10);
    }
    case "float32":
    case "float64": {
      const n = Number.parseFloat(raw);
      if (Number.isNaN(n)) {
        errors[f.name] = `${f.name} must be a number`;
        return null;
      }
      return n;
    }
    default:
      // Unknown / nested struct kinds — pass through as string for v1.
      return raw;
  }
}
