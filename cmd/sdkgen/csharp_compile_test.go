//go:build csharptest

// Compile gate: emit the C# SDK for a sample schema, copy the _core sources,
// and run `dotnet build` on the result. Tagged so it only runs where the .NET
// SDK is available: `go test -tags=csharptest ./cmd/sdkgen/`.
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCsharpSdk_Compiles(t *testing.T) {
	if _, err := exec.LookPath("dotnet"); err != nil {
		t.Skip("dotnet not installed; skipping C# compile gate")
	}

	out := t.TempDir()
	if err := os.MkdirAll(filepath.Join(out, "_core"), 0o755); err != nil {
		t.Fatal(err)
	}

	b := csharpBackend{namespace: "Mmokit.Sdk", coreDir: "../../csharp/Mmokit.Sdk.Core"}

	// Copy the five _core files.
	for _, cf := range b.CoreFiles() {
		data, err := os.ReadFile(cf.Src)
		if err != nil {
			t.Fatalf("read core %s: %v", cf.Src, err)
		}
		if err := os.WriteFile(filepath.Join(out, "_core", cf.Dst), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Emit the generated files for the sample schema.
	for name, gen := range b.OutputFiles(sampleEntitySchema()) {
		if err := os.WriteFile(filepath.Join(out, name), []byte(gen()), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Minimal netstandard2.1 project that compiles every .cs in the dir.
	csproj := `<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup>
    <TargetFramework>netstandard2.1</TargetFramework>
    <LangVersion>9.0</LangVersion>
    <Nullable>enable</Nullable>
  </PropertyGroup>
</Project>
`
	if err := os.WriteFile(filepath.Join(out, "Sdk.csproj"), []byte(csproj), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("dotnet", "build", "-nologo", "-v", "quiet")
	cmd.Dir = out
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dotnet build failed: %v\n%s", err, output)
	}
}
