package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

const mcpStateLimit = 64 << 10

// One harness config Kilo can write. These are the profiles Kilo launches, so a
// mode-B variable is injected at launch and never stored in a file.
type mcpHarness struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

var mcpHarnesses = []mcpHarness{
	{ID: "codex", Name: "Codex Desktop"},
	{ID: "codex-cli", Name: "Codex CLI"},
	{ID: "claude", Name: "Claude Code"},
}

func mcpHarnessIDs() map[string]bool {
	ids := make(map[string]bool, len(mcpHarnesses))
	for _, h := range mcpHarnesses {
		ids[h.ID] = true
	}
	return ids
}

// Per-user, per-server selection. Harnesses empty means every available writer.
// The credential itself lives in the vault, never here.
type mcpServerState struct {
	Enabled   bool     `json:"enabled"`
	Mode      string   `json:"mode,omitempty"`
	Harnesses []string `json:"harnesses,omitempty"`
}

type mcpStateFile struct {
	SchemaVersion int                       `json:"schemaVersion"`
	Servers       map[string]mcpServerState `json:"servers"`
}

func emptyMCPStateFile() mcpStateFile {
	return mcpStateFile{SchemaVersion: 1, Servers: map[string]mcpServerState{}}
}

func cloneMCPStateFile(file mcpStateFile) mcpStateFile {
	out := mcpStateFile{SchemaVersion: file.SchemaVersion, Servers: make(map[string]mcpServerState, len(file.Servers))}
	for id, state := range file.Servers {
		state.Harnesses = append([]string(nil), state.Harnesses...)
		out.Servers[id] = state
	}
	return out
}

func validateMCPStateFile(file mcpStateFile) error {
	if file.SchemaVersion != 1 {
		return errors.New("Unsupported MCP state schema")
	}
	harnesses := mcpHarnessIDs()
	for id, state := range file.Servers {
		if !mcpIDPattern.MatchString(id) {
			return errors.New("Invalid MCP state entry")
		}
		if state.Mode != "" && !mcpModes[state.Mode] {
			return errors.New("Invalid MCP delivery mode")
		}
		seen := map[string]bool{}
		for _, h := range state.Harnesses {
			if !harnesses[h] || seen[h] {
				return errors.New("Invalid MCP harness selection")
			}
			seen[h] = true
		}
	}
	return nil
}

func decodeMCPStateFile(data []byte) (mcpStateFile, error) {
	var file mcpStateFile
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return file, err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return file, errors.New("Trailing content in MCP state")
	}
	if err := validateMCPStateFile(file); err != nil {
		return file, err
	}
	return cloneMCPStateFile(file), nil
}

func readMCPStateFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > mcpStateLimit {
		return nil, errors.New("MCP state must be a regular file smaller than 64 KiB")
	}
	data, err := os.ReadFile(path)
	if len(data) > mcpStateLimit {
		return nil, errors.New("MCP state exceeds 64 KiB")
	}
	return data, err
}

type mcpStateStore struct {
	dir         string
	value       mcpStateFile
	disk        []byte
	diskErr     error
	recovery    bool
	recoverable bool
	warning     string
}

func newMCPStateStore(dir string) *mcpStateStore {
	s := &mcpStateStore{dir: dir, value: emptyMCPStateFile()}
	path := filepath.Join(dir, "mcp-state.json")
	data, err := readMCPStateFile(path)
	s.disk, s.diskErr = data, err
	if err == nil {
		if v, parse := decodeMCPStateFile(data); parse == nil {
			s.value = v
			return s
		}
	}
	backup, backErr := readMCPStateFile(path + ".bak")
	if errors.Is(err, os.ErrNotExist) && errors.Is(backErr, os.ErrNotExist) {
		return s
	}
	s.recovery = true
	s.warning = "Saved MCP settings need review. Existing files have not been changed."
	if backErr == nil {
		if v, parse := decodeMCPStateFile(backup); parse == nil {
			s.value = v
			s.recoverable = true
			s.warning = "The last good MCP settings backup is shown. Review and recover before editing."
		}
	}
	return s
}

func (s *mcpStateStore) save(file mcpStateFile, recover bool) error {
	if err := validateMCPStateFile(file); err != nil {
		return err
	}
	if s.recovery && !recover {
		return errors.New("MCP settings changed outside this window. Reload the saved settings before saving again.")
	}
	if s.recovery && !s.recoverable {
		return errors.New("No valid MCP settings backup is available. The damaged files have not been replaced.")
	}
	path := filepath.Join(s.dir, "mcp-state.json")
	encoded, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if len(encoded) > mcpStateLimit {
		return errors.New("MCP state exceeds 64 KiB")
	}
	if s.recovery {
		if err := atomicCatalogFile(path+".bak", s.disk); err != nil {
			return errors.New("Cannot back up the damaged MCP settings")
		}
	} else if len(s.disk) > 0 {
		if err := atomicCatalogFile(path+".bak", s.disk); err != nil {
			return errors.New("Cannot back up MCP settings")
		}
	}
	if err := atomicCatalogFile(path, encoded); err != nil {
		return errors.New("Cannot save MCP settings")
	}
	s.value, s.disk, s.diskErr = cloneMCPStateFile(file), encoded, nil
	s.recovery, s.recoverable, s.warning = false, false, ""
	return nil
}
