package main

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestPackagePlugin(t *testing.T) {
	dir := t.TempDir()
	manifest := []byte(`{"name":"Test","author":"Test","version":"0.2.0"}`)
	wasm := []byte{0, 'a', 's', 'm', 1, 0, 0, 0, 42}
	manifestPath := filepath.Join(dir, "manifest.json")
	wasmPath := filepath.Join(dir, "plugin.wasm")
	output := filepath.Join(dir, "dist", "test.ndp")
	if err := os.WriteFile(manifestPath, manifest, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wasmPath, wasm, 0644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := packagePlugin(manifestPath, wasmPath, output); err != nil {
			t.Fatal(err)
		}
	}
	archive, err := zip.OpenReader(output)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	if len(archive.File) != 2 {
		t.Fatal("unexpected entries", archive.File)
	}
	expected := map[string][]byte{"manifest.json": manifest, "plugin.wasm": wasm}
	for _, entry := range archive.File {
		reader, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(reader)
		reader.Close()
		if err != nil || !bytes.Equal(body, expected[entry.Name]) {
			t.Fatal(entry.Name, err)
		}
		delete(expected, entry.Name)
	}
	if len(expected) != 0 {
		t.Fatal("missing entries", expected)
	}
	info, _ := os.Stat(output)
	if info.Mode().Perm() != 0644 {
		t.Fatal("package must be readable by Navidrome", info.Mode())
	}
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(output), ".*.tmp"))
	if len(files) != 0 {
		t.Fatal("temporary files left behind", files)
	}
}

func TestInvalidInputPreservesInstalledPackage(t *testing.T) {
	for _, tc := range []struct{ name, manifest, wasm string }{
		{"bad-json", "{bad", "\x00asm\x01\x00\x00\x00"},
		{"incomplete-manifest", `{"name":"Test"}`, "\x00asm\x01\x00\x00\x00"},
		{"invalid-wasm", `{"name":"Test","author":"Test","version":"0.2.0"}`, "not wasm"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			manifest := filepath.Join(dir, "manifest.json")
			wasm := filepath.Join(dir, "plugin.wasm")
			output := filepath.Join(dir, "existing.ndp")
			for path, body := range map[string]string{manifest: tc.manifest, wasm: tc.wasm, output: "old"} {
				if err := os.WriteFile(path, []byte(body), 0644); err != nil {
					t.Fatal(err)
				}
			}
			if err := packagePlugin(manifest, wasm, output); err == nil {
				t.Fatal("invalid input accepted")
			}
			body, _ := os.ReadFile(output)
			if string(body) != "old" {
				t.Fatal("existing package overwritten")
			}
		})
	}
}

func TestMissingInput(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "manifest.json")
	wasm := filepath.Join(dir, "plugin.wasm")
	output := filepath.Join(dir, "plugin.ndp")
	if err := packagePlugin(manifest, wasm, output); err == nil {
		t.Fatal("missing manifest accepted")
	}
	if err := os.WriteFile(manifest, []byte(`{"name":"Test","author":"Test","version":"0.2.0"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := packagePlugin(manifest, wasm, output); err == nil {
		t.Fatal("missing WASM accepted")
	}
}
