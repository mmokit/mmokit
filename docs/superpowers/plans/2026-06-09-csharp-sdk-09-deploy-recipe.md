# C# SDK — Plan 9: `just csharp-sdk` deploy recipe

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A `just csharp-sdk` recipe that generates the 4node-basic C# SDK and writes it straight to the Unity project's `Assets/` tree (a `/mnt/c/...` path from WSL), overridable via `UNITY_SDK_DIR`.

**Architecture:** Mirror the existing `client-sdk` (TS) recipe but with `--lang=csharp` and the C# `--out`/`--csharp-core` flags. Dump the 4node schema with the control + admin listeners disabled so the generate works even while the user's dev server is running (avoids the `:9100` bind conflict). The generator writes `EntityType/Entities/DeltaDecoder/Events/Inputs/Operations/Client.cs` + the `_core/*.cs` straight into `UNITY_SDK_DIR`; Unity compiles the `.cs` source.

**Spec:** [docs/superpowers/specs/2026-06-06-csharp-sdk-unity-design.md](../specs/2026-06-06-csharp-sdk-unity-design.md) §E. Default target `<WINDOWS-HOME>/unitygames/spacemmo-client/Assets/Mmokit/Sdk` (the WSL view of `C:\Users\<YOU>\unitygames\spacemmo-client\Assets`).

**Prerequisites:** Plans 1–8 merged (the full csharp backend). Postgres up (`just db-up`) for the schema dump.

---

## File Structure

- **Modify:** `justfile` — add the `csharp-sdk` recipe near the other `csharp-*` recipes.

---

### Task 1: Add the `csharp-sdk` recipe + verify

- [ ] **Step 1: Add the recipe** — append to `justfile` (next to `csharp-compile-test`):

```just
# generate + deploy the C# client SDK for 4node-basic into the Unity Assets
# tree. Override the target with UNITY_SDK_DIR (defaults to a /mnt/c path).
# Control/admin listeners are disabled so the schema dump works even while a
# dev server is running. Requires Postgres (just db-up).
csharp-sdk:
    go run ./examples/4node-basic --dump-schema --control-listen= --admin-listen= \
        "--postgres-url={{ env('POSTGRES_URL', 'postgres://mmo:mmo@localhost:5432/mmo_4node?sslmode=disable') }}" \
      | go run ./cmd/sdkgen --lang=csharp \
          --csharp-core csharp/Mmokit.Sdk.Core \
          --out "{{ env('UNITY_SDK_DIR', '<WINDOWS-HOME>/unitygames/spacemmo-client/Assets/Mmokit/Sdk') }}"
```

- [ ] **Step 2: Verify the recipe generates a compiling SDK to a temp dir**

Do NOT write into the real Unity `Assets/` during verification (avoid clobbering the user's project). Override `UNITY_SDK_DIR` to a temp dir:

Run:
```bash
just db-up   # if not already up
rm -rf /tmp/csharp-sdk-verify && UNITY_SDK_DIR=/tmp/csharp-sdk-verify just csharp-sdk
ls /tmp/csharp-sdk-verify /tmp/csharp-sdk-verify/_core
```
Expected: the recipe prints the written file paths; the dir contains `EntityType.cs`, `Entities.cs`, `DeltaDecoder.cs`, `Client.cs` (+ `Events.cs`/`Inputs.cs`/`Operations.cs` if 4node registers those), and `_core/` has the 6 runtime files (`DeltaDecoderCore.cs`, `InterpolationCore.cs`, `ReflectCodec.cs`, `UdpProto.cs`, `UdpTransport.cs`, `UdpTransport.Socket.cs`).

- [ ] **Step 3: Confirm the deployed SDK compiles**

Run:
```bash
cat > /tmp/csharp-sdk-verify/Sdk.csproj << 'EOF'
<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup>
    <TargetFramework>netstandard2.1</TargetFramework>
    <LangVersion>9.0</LangVersion>
    <Nullable>enable</Nullable>
  </PropertyGroup>
</Project>
EOF
cd /tmp/csharp-sdk-verify && dotnet build -nologo -v quiet
```
Expected: build succeeds (0 errors) — proves the recipe produces a real, compiling C# SDK from the actual 4node schema (not just the in-test sample). Clean up: `rm -rf /tmp/csharp-sdk-verify`.

**If Postgres/Docker is unavailable:** `just db-up` + the dump can't run; report `DONE_WITH_CONCERNS`, note the deploy verification was deferred, and rely on the recipe text being correct (the generator + compile gate are already proven hermetically in Plans 5–8). The user can run `just csharp-sdk` with the DB up.

- [ ] **Step 4: Commit**

```bash
git add justfile
git commit -m "feat(build): just csharp-sdk — deploy the C# SDK to UNITY_SDK_DIR

Generates the 4node-basic C# SDK (--lang=csharp) straight into the Unity
Assets tree (default a /mnt/c path; override via UNITY_SDK_DIR). Control/
admin listeners disabled so the schema dump works alongside a running dev
server. Verified: generates a compiling SDK from the real 4node schema.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review

- **Spec coverage (§E):** `just csharp-sdk` generates the 4node C# SDK to `UNITY_SDK_DIR` (default the documented `/mnt/c` Unity path); control/admin disabled for conflict-free dumps; generator writes the full SDK + `_core` straight to the target. Verified end-to-end by generating from the real schema + `dotnet build`. ✅
- **Placeholder scan:** Complete recipe; verification uses a temp dir to avoid touching the user's Unity project. ✅
- **Consistency:** mirrors the `client-sdk` TS recipe's structure (`--dump-schema | sdkgen`); `--lang=csharp` + `--csharp-core` + `--out` match the Plan 5 flags; the default path matches the spec. ✅

## Post-completion: end-to-end smoke (the final behavioral proof — out of scope here)

The compile gate proves the SDK builds; the remaining behavioral proof is a live smoke (the user's Unity work or a standalone `dotnet` console bot): connect to a running 4node server over UDP (`UdpTransport.Connect`), `await client.AuthLogin(new AuthLoginRequest{...})`, then `client.OnWorldDelta(m => client.Decoder.Decode(m.body))` and observe entity updates. This requires a running server + the UDP op-channel auth (Plan 1) + a real account, so it's delivered as inline instructions, not an automated test (per the no-SMOKE.md convention).
