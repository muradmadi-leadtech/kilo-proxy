package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestMCPStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := newMCPStateStore(dir)
	if len(store.value.Servers) != 0 {
		t.Fatal("new store must be empty")
	}
	file := emptyMCPStateFile()
	file.Servers["supermetrics"] = mcpServerState{Enabled: true, Mode: "B", Harnesses: []string{"codex", "claude"}}
	file.Servers["appsflyer"] = mcpServerState{Enabled: false}
	if err := store.save(file, false); err != nil {
		t.Fatal(err)
	}
	reloaded := newMCPStateStore(dir)
	if !reloaded.value.Servers["supermetrics"].Enabled || reloaded.value.Servers["supermetrics"].Mode != "B" {
		t.Fatalf("state did not round-trip: %+v", reloaded.value.Servers["supermetrics"])
	}
	if len(reloaded.value.Servers["supermetrics"].Harnesses) != 2 {
		t.Fatal("harness selection did not round-trip")
	}
	// A second write must back up the version it replaces.
	file.Servers["supermetrics"] = mcpServerState{Enabled: true, Mode: "A"}
	if err := reloaded.save(file, false); err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(filepath.Join(dir, "mcp-state.json.bak"))
	if err != nil {
		t.Fatalf("expected a backup of the replaced state: %v", err)
	}
	if !bytes.Contains(backup, []byte("\"B\"")) {
		t.Fatal("backup must hold the previous value")
	}
}

func TestMCPStateRejectsInvalid(t *testing.T) {
	base := emptyMCPStateFile()
	bad := map[string]mcpStateFile{
		"bad mode": func() mcpStateFile {
			f := emptyMCPStateFile()
			f.Servers["x"] = mcpServerState{Mode: "C"}
			return f
		}(),
		"bad harness": func() mcpStateFile {
			f := emptyMCPStateFile()
			f.Servers["x"] = mcpServerState{Harnesses: []string{"emacs"}}
			return f
		}(),
		"bad id": func() mcpStateFile {
			f := emptyMCPStateFile()
			f.Servers["Bad_ID"] = mcpServerState{Enabled: true}
			return f
		}(),
	}
	if err := validateMCPStateFile(base); err != nil {
		t.Fatalf("empty state must be valid: %v", err)
	}
	for name, file := range bad {
		if err := validateMCPStateFile(file); err == nil {
			t.Fatalf("%s: expected rejection", name)
		}
	}
}

// A fresh profile has no settings directory yet; the store must create it
// rather than depend on another component having done so.
func TestMCPStateCreatesAMissingDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "app")
	store := newMCPStateStore(dir)
	file := emptyMCPStateFile()
	file.Servers["supermetrics"] = mcpServerState{Enabled: true, Mode: "B"}
	if err := store.save(file, false); err != nil {
		t.Fatalf("a save into a missing directory must create it: %v", err)
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		t.Fatalf("the settings directory was not created: %v", err)
	}
	if reloaded := newMCPStateStore(dir); !reloaded.value.Servers["supermetrics"].Enabled {
		t.Fatal("the saved state did not round-trip")
	}
}
