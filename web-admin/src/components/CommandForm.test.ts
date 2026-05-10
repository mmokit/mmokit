import { describe, it, expect } from "vitest";
import { coerceArgs, defaultValueFor } from "./CommandForm.helpers";
import type { Schema } from "$lib/types";

const schema: Schema = {
  struct: "Args",
  fields: [
    { name: "Name", kind: "string", required: true, named_only: false, default: "", enum: [] },
    { name: "Count", kind: "int32", required: true, named_only: false, default: "", enum: [] },
    { name: "Ratio", kind: "float64", required: false, named_only: false, default: "0.5", enum: [] },
    { name: "Active", kind: "bool", required: false, named_only: false, default: "false", enum: [] },
  ],
};

describe("coerceArgs", () => {
  it("coerces numeric and bool fields from string inputs", () => {
    const { args, errors } = coerceArgs(schema, { Name: "alice", Count: "42", Ratio: "0.75", Active: "true" });
    expect(errors).toEqual({});
    expect(args).toEqual({ Name: "alice", Count: 42, Ratio: 0.75, Active: true });
  });

  it("flags missing required fields", () => {
    const { args, errors } = coerceArgs(schema, { Count: "1" });
    expect(args).toBeNull();
    expect(errors.Name).toMatch(/required/i);
  });

  it("flags non-numeric input on int field", () => {
    const { args, errors } = coerceArgs(schema, { Name: "alice", Count: "abc" });
    expect(args).toBeNull();
    expect(errors.Count).toMatch(/integer|number/i);
  });

  it("uses default when optional field is empty", () => {
    const { args, errors } = coerceArgs(schema, { Name: "alice", Count: "1" });
    expect(errors).toEqual({});
    expect(args!.Ratio).toBeCloseTo(0.5);
    expect(args!.Active).toBe(false);
  });
});

describe("defaultValueFor", () => {
  it("returns default literal for non-empty defaults", () => {
    expect(defaultValueFor({ name: "x", kind: "int32", required: false, named_only: false, default: "7", enum: [] })).toBe("7");
  });
  it("returns empty string when no default and not required", () => {
    expect(defaultValueFor({ name: "x", kind: "string", required: false, named_only: false, default: "", enum: [] })).toBe("");
  });
});
