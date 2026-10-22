package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
)

// One harness config the fan-out can edit.
type mcpTarget struct {
	harness string
	dir     string
	file    string
}

type mcpSyncReport struct {
	Harness  string   `json:"harness"`
	Status   string   `json:"status"` // changed | unchanged | skipped | failed
	Reason   string   `json:"reason,omitempty"`
	Skipped  []string `json:"skipped,omitempty"`
	Problems []string `json:"problems,omitempty"`
	Backup   string   `json:"backup,omitempty"`
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// Caller holds a.mu. Resolves every harness profile exactly as its launcher does.
func (a *app) mcpTargets() ([]mcpTarget, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, errors.New("No se puede localizar el directorio personal.")
	}
	dirs := map[string]string{
		"codex":     firstNonEmpty(a.codexProfileDir, filepath.Join(home, ".codex-kilo-desktop")),
		"codex-cli": firstNonEmpty(a.codexCLIProfileDir, filepath.Join(home, ".codex-kilo-cli")),
		"claude":    firstNonEmpty(a.claudeProfileDir, filepath.Join(home, ".claude-kilo")),
	}
	files := map[string]string{"codex": "config.toml", "codex-cli": "config.toml", "claude": ".claude.json"}
	targets := make([]mcpTarget, 0, len(mcpHarnesses))
	for _, h := range mcpHarnesses {
		targets = append(targets, mcpTarget{harness: h.ID, dir: dirs[h.ID], file: files[h.ID]})
	}
	return targets, nil
}

// mcpServerConfigs turns the catalog and saved state into the exact list a
// writer needs for one harness. A disabled server is included so an entry Kilo
// wrote earlier is removed; a server that cannot be resolved (no token yet,
// OAuth in P0) is omitted and reported, so a working entry is never destroyed.
func (a *app) mcpServerConfigs(catalog []curatedMCP, state mcpStateFile, harness string) ([]mcpServerConfig, []string) {
	var configs []mcpServerConfig
	var problems []string
	for _, s := range catalog {
		st := state.Servers[s.ID]
		selected := len(st.Harnesses) == 0 || slices.Contains(st.Harnesses, harness)
		cfg := mcpServerConfig{ID: s.ID, URL: s.URL, Header: s.Header, ValuePrefix: s.ValuePrefix}
		if !st.Enabled || !selected {
			configs = append(configs, cfg) // Enabled=false: remove only if Kilo owns it
			continue
		}
		switch s.Auth {
		case "none":
			cfg.Enabled = true
		case "token":
			mode := st.Mode
			if mode == "" {
				mode = "A"
			}
			if mode == "B" && s.EnvVar != "" {
				cfg.Enabled, cfg.EnvVar = true, s.EnvVar
			} else if mode == "B" {
				problems = append(problems, s.ID+": no launch variable configured")
				continue
			} else if token, err := a.vault.Get(mcpVaultSlot(s.ID)); err == nil && token != "" {
				cfg.Enabled, cfg.Token = true, token
			} else {
				problems = append(problems, s.ID+": no saved token")
				continue
			}
		default:
			problems = append(problems, s.ID+": OAuth is not available yet")
			continue
		}
		configs = append(configs, cfg)
	}
	return configs, problems
}

func (a *app) writeMCPHarness(target mcpTarget, configs []mcpServerConfig) mcpSyncReport {
	report := mcpSyncReport{Harness: target.harness}
	info, err := os.Lstat(target.dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		report.Status, report.Reason = "skipped", "perfil no preparado"
		return report
	}
	path := filepath.Join(target.dir, target.file)
	old, err := readCatalogFile(path)
	if errors.Is(err, os.ErrNotExist) {
		old = nil
	} else if err != nil {
		report.Status, report.Reason = "failed", "no se puede leer "+target.file
		return report
	}
	var merged []byte
	var skipped []string
	if target.harness == "claude" {
		if len(old) == 0 {
			old = []byte("{}\n")
		}
		merged, skipped, err = mergeClaudeCodeMCP(old, configs)
	} else {
		merged, skipped, err = mergeCodexMCP(old, configs)
	}
	report.Skipped = skipped
	if err != nil {
		report.Status, report.Reason = "failed", err.Error()
		return report
	}
	file, err := prepareProfileFile(path, merged)
	if err != nil {
		report.Status, report.Reason = "failed", err.Error()
		return report
	}
	// Preserve the file's existing permissions instead of forcing 0600.
	if file.exists {
		if current, statErr := os.Lstat(path); statErr == nil {
			file.mode = current.Mode().Perm()
		}
	}
	if !file.changed {
		report.Status = "unchanged"
		return report
	}
	changed, err := saveEditorFiles([]profileFile{file})
	if err != nil {
		report.Status, report.Reason = "failed", err.Error()
		return report
	}
	report.Status = "unchanged"
	if changed {
		report.Status, report.Backup = "changed", path+".bak"
	}
	return report
}

// syncMCP runs the whole loop: catalog + state + vault → every harness config.
// Each harness is independent; one failure never stops the others.
func (a *app) syncMCP(ctx context.Context) ([]mcpSyncReport, error) {
	catalog := a.mcpCatalog(ctx) // takes a.mu internally
	if len(catalog) == 0 {
		return nil, errors.New("No hay ningún catálogo MCP disponible.")
	}
	a.mu.Lock()
	state := newMCPStateStore(a.dir).value
	targets, err := a.mcpTargets()
	a.mu.Unlock()
	if err != nil {
		return nil, err
	}
	reports := make([]mcpSyncReport, 0, len(targets))
	for _, target := range targets {
		configs, problems := a.mcpServerConfigs(catalog, state, target.harness)
		report := a.writeMCPHarness(target, configs)
		report.Problems = problems
		reports = append(reports, report)
	}
	return reports, nil
}

// mcpLaunchEnv returns the variables Kilo sets when it launches a harness, one
// per enabled mode-B server. Mode B keeps the secret out of every config file,
// so the value must match the reference the writers emit: the bare token.
// Caller holds a.mu.
func (a *app) mcpLaunchEnv() map[string]string {
	state := newMCPStateStore(a.dir).value
	env := map[string]string{}
	for _, s := range builtinMCPCatalog() {
		st := state.Servers[s.ID]
		if !st.Enabled || s.Auth != "token" || st.Mode != "B" || s.EnvVar == "" {
			continue
		}
		if token, err := a.vault.Get(mcpVaultSlot(s.ID)); err == nil && token != "" {
			env[s.EnvVar] = token
		}
	}
	return env
}
