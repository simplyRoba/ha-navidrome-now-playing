// Package the compiler output without requiring zip or Python on the build host.
package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func packagePlugin(manifestPath, wasmPath, output string) error {
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	var metadata struct{ Name, Author, Version string }
	if err := json.Unmarshal(manifest, &metadata); err != nil {
		return err
	}
	if metadata.Name == "" || metadata.Author == "" || metadata.Version == "" {
		return errors.New("manifest requires name, author and version")
	}
	wasm, err := os.ReadFile(wasmPath)
	if err != nil {
		return err
	}
	if !bytes.HasPrefix(wasm, []byte{0, 'a', 's', 'm', 1, 0, 0, 0}) {
		return errors.New("not a WebAssembly v1 module")
	}
	if err := os.MkdirAll(filepath.Dir(output), 0755); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(output), ".navidrome-nowplaying-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	archive := zip.NewWriter(file)
	for _, entry := range []struct {
		name string
		body []byte
	}{{"manifest.json", manifest}, {"plugin.wasm", wasm}} {
		writer, err := archive.Create(entry.name)
		if err != nil {
			return err
		}
		if _, err := writer.Write(entry.body); err != nil {
			return err
		}
	}
	if err := archive.Close(); err != nil {
		return err
	}
	if err := file.Chmod(0644); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), output)
}

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: package manifest.json plugin.wasm output.ndp")
		os.Exit(2)
	}
	if err := packagePlugin(os.Args[1], os.Args[2], os.Args[3]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
