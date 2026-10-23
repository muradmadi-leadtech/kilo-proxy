package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func mcpTestApp(dir string, vault credentialVault) *app {
	return &app{dir: dir, vault: vault}
}

func mcpTestCatalog() []curatedMCP {
	return []curatedMCP{
		{ID: "supermetrics", URL: supermetricsURL, Transport: "http", Auth: "token", Header: "Authorization", ValuePrefix: "Bearer ", EnvVar: "KILO_MCP_SUPERMETRICS_TOKEN", Modes: []string{"A", "B"}},
		{ID: "amplitude", URL: "https://mcp.eu.amplitude.com/mcp", Transport: "http", Auth: "oauth", Header: "Authorization", ValuePrefix: "Bearer ", EnvVar: "KILO_MCP_AMPLITUDE_TOKEN", Modes: []string{"B"}},
	}
}

func TestMCPServerConfigsResolvesModes(t *testing.T) {
	vault := mcpTestVault{mcpVaultSlot("supermetrics"): "api_key_1"}
	a := mcpTestApp(t.TempDir(), vault)
	catalog := mcpTestCatalog()

	state := emptyMCPStateFile()
	state.Servers["supermetrics"] = mcpServerState{Enabled: true, Mode: "A"}
	configs, problems := a.mcpServerConfigs(catalog, state, "codex")
	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
	if configs[0].ID != "supermetrics" || !configs[0].Enabled || configs[0].Token != "api_key_1" || configs[0].EnvVar != "" {
		t.Fatalf("mode A not resolved: %+v", configs[0])
	}

	state.Servers["supermetrics"] = mcpServerState{Enabled: true, Mode: "B"}
	configs, _ = a.mcpServerConfigs(catalog, state, "codex")
	if configs[0].EnvVar != "KILO_MCP_SUPERMETRICS_TOKEN" || configs[0].Token != "" {
		t.Fatalf("mode B not resolved: %+v", configs[0])
	}

	// OAuth is delivered as mode B: the config names the launch variable and no
	// secret is written, so nothing needs a token lookup here.
	state.Servers["amplitude"] = mcpServerState{Enabled: true, Mode: "B"}
	configs, problems = a.mcpServerConfigs(catalog, state, "codex")
	if len(problems) != 0 {
		t.Fatalf("oauth must not report a problem: %v", problems)
	}
	if configs[1].ID != "amplitude" || !configs[1].Enabled || configs[1].EnvVar != "KILO_MCP_AMPLITUDE_TOKEN" || configs[1].Token != "" {
		t.Fatalf("oauth must resolve to an env reference only: %+v", configs[1])
	}

	// Missing token: omitted and reported, never a removal.
	state = emptyMCPStateFile()
	state.Servers["supermetrics"] = mcpServerState{Enabled: true, Mode: "A"}
	a = mcpTestApp(t.TempDir(), mcpTestVault{})
	configs, problems = a.mcpServerConfigs(catalog, state, "codex")
	if len(problems) != 1 || problems[0].Server != "supermetrics" || problems[0].Code != "missing-token" {
		t.Fatalf("a missing token must be reported as a code: %+v", problems)
	}
	for _, cfg := range configs {
		if cfg.ID == "supermetrics" {
			t.Fatalf("an unresolved server must be omitted, not written: %+v", cfg)
		}
	}
}

func TestWriteMCPHarnessBacksUpAndPreserves(t *testing.T) {
	dir := t.TempDir()
	original := []byte("# keep\nmodel = 'vendor/code'\n")
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	a := mcpTestApp(dir, mcpTestVault{})
	configs := []mcpServerConfig{{ID: "supermetrics", Enabled: true, URL: supermetricsURL, Header: "Authorization", ValuePrefix: "Bearer ", Token: "api_1"}}
	report := a.writeMCPHarness(mcpTarget{harness: "codex", dir: dir, file: "config.toml"}, configs)
	if report.Status != "changed" || report.Backup == "" {
		t.Fatalf("expected a change with a backup: %+v", report)
	}
	backup, err := os.ReadFile(path + ".bak")
	if err != nil || !bytes.Equal(backup, original) {
		t.Fatalf("backup must hold the original: %v", err)
	}
	written, _ := os.ReadFile(path)
	if !bytes.Contains(written, []byte("# keep")) || !bytes.Contains(written, []byte("supermetrics")) {
		t.Fatalf("unrelated content lost: %s", written)
	}
	// Second run is a no-op.
	if again := a.writeMCPHarness(mcpTarget{harness: "codex", dir: dir, file: "config.toml"}, configs); again.Status != "unchanged" {
		t.Fatalf("expected unchanged: %+v", again)
	}
	// A missing profile directory is skipped, not failed.
	if skipped := a.writeMCPHarness(mcpTarget{harness: "codex", dir: filepath.Join(dir, "nope"), file: "config.toml"}, configs); skipped.Status != "skipped" {
		t.Fatalf("expected skipped: %+v", skipped)
	}
}

func TestWriteMCPHarnessClaudeCode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude.json")
	if err := os.WriteFile(path, []byte("{\n  \"numStartups\": 3\n}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	a := mcpTestApp(dir, mcpTestVault{})
	configs := []mcpServerConfig{{ID: "supermetrics", Enabled: true, URL: supermetricsURL, Header: "Authorization", ValuePrefix: "Bearer ", EnvVar: "KILO_MCP_SUPERMETRICS_TOKEN"}}
	report := a.writeMCPHarness(mcpTarget{harness: "claude", dir: dir, file: ".claude.json"}, configs)
	if report.Status != "changed" {
		t.Fatalf("expected a change: %+v", report)
	}
	written, _ := os.ReadFile(path)
	if !bytes.Contains(written, []byte("Bearer ${KILO_MCP_SUPERMETRICS_TOKEN}")) {
		t.Fatalf("mode B reference missing: %s", written)
	}
	if !bytes.Contains(written, []byte("\"numStartups\": 3")) {
		t.Fatalf("unrelated state or formatting lost: %s", written)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0644 {
		t.Fatalf("original permissions must be preserved: %v", err)
	}
}

func TestMCPLaunchEnvOnlyModeB(t *testing.T) {
	dir := t.TempDir()
	a := mcpTestApp(dir, mcpTestVault{mcpVaultSlot("supermetrics"): "tok_1"})
	file := emptyMCPStateFile()
	file.Servers["supermetrics"] = mcpServerState{Enabled: true, Mode: "B"}
	file.Servers["appsflyer"] = mcpServerState{Enabled: true, Mode: "A"}
	if err := newMCPStateStore(dir).save(file, false); err != nil {
		t.Fatal(err)
	}
	env := a.mcpLaunchEnv()
	if env["KILO_MCP_SUPERMETRICS_TOKEN"] != "tok_1" {
		t.Fatalf("mode B must inject the bare token: %v", env)
	}
	if _, ok := env["KILO_MCP_APPSFLYER_TOKEN"]; ok {
		t.Fatal("mode A must not inject a launch variable")
	}
}

func TestMCPReportUsesStableReasonCodes(t *testing.T) {
	dir := t.TempDir()
	a := mcpTestApp(dir, mcpTestVault{})
	// A profile directory that does not exist yet.
	report := a.writeMCPHarness(mcpTarget{harness: "codex", dir: filepath.Join(dir, "absent"), file: "config.toml"}, nil)
	if report.Status != "skipped" || report.ReasonCode != "profile-not-prepared" {
		t.Fatalf("expected a stable reason code, got %+v", report)
	}
	if report.Reason != "" {
		t.Fatal("localized prose must not be sent to the UI")
	}
	// A directory that exists but holds invalid TOML.
	bad := filepath.Join(dir, "bad")
	if err := os.MkdirAll(bad, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, "config.toml"), []byte("this is not = = toml"), 0600); err != nil {
		t.Fatal(err)
	}
	report = a.writeMCPHarness(mcpTarget{harness: "codex", dir: bad, file: "config.toml"}, []mcpServerConfig{{ID: "x", Enabled: true, URL: "https://x.example/mcp", Header: "Authorization", ValuePrefix: "Bearer ", Token: "t"}})
	if report.Status != "failed" || report.ReasonCode != "" || report.Reason == "" {
		t.Fatalf("an unexpected failure must carry its detail: %+v", report)
	}
}
