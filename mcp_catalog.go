package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// The curated catalog ships embedded so a fresh install works offline, and can
// be refreshed from the repository so a new server needs no release. It mirrors
// recommended-models.json: an embedded fallback plus a short-lived remote copy.
//
//go:embed mcp-catalog.json
var embeddedMCPCatalog []byte

const (
	mcpCatalogEndpoint = "https://raw.githubusercontent.com/rosseca/kilo-proxy/main/mcp-catalog.json"
	mcpCatalogTimeout  = 3 * time.Second
	mcpCatalogLimit    = 128 << 10
	mcpCatalogMax      = 64
	mcpNameMax         = 60
	mcpDescriptionMax  = 240
	mcpScopeMax        = 60
)

var (
	mcpIDPattern     = regexp.MustCompile(`^[a-z][a-z0-9-]{1,47}$`)
	mcpEnvVarPattern = regexp.MustCompile(`^KILO_MCP_[A-Z0-9_]{1,40}$`)
	mcpHeaderPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	mcpModes         = map[string]bool{"A": true, "B": true}
	mcpAuths         = map[string]bool{"token": true, "none": true, "oauth": true}
)

type mcpText struct {
	EN string `json:"en"`
	ES string `json:"es"`
}

// One curated upstream MCP server. Mode A writes a literal secret; mode B
// writes the env var name and Kilo injects the token when it launches a
// harness. EnvVar is only set for token auth and must always be a
// KILO_MCP_<UPSTREAM>_TOKEN name: Claude Code reads credential-looking names
// (ANTHROPIC_*, MCP_CLIENT_SECRET) as empty toward a remote server.
type curatedMCP struct {
	ID          string   `json:"id"`
	Name        mcpText  `json:"name"`
	Description mcpText  `json:"description"`
	URL         string   `json:"url"`
	Transport   string   `json:"transport"`
	Auth        string   `json:"auth"`
	Header      string   `json:"header,omitempty"`
	ValuePrefix string   `json:"valuePrefix,omitempty"`
	EnvVar      string   `json:"envVar,omitempty"`
	Modes       []string `json:"modes,omitempty"`
	DocsURL     string   `json:"docsUrl,omitempty"`
	TokenURL    string   `json:"tokenUrl,omitempty"`
	Scopes      []string `json:"scopes,omitempty"`

	// OAuth overrides for servers that do not publish metadata, and the hosted
	// client-metadata document URL when the server prefers CIMD over DCR.
	AuthorizationURL  string `json:"authorizationUrl,omitempty"`
	RegistrationURL   string `json:"registrationUrl,omitempty"`
	ClientMetadataURL string `json:"clientMetadataUrl,omitempty"`
}

type curatedMCPCatalog struct {
	SchemaVersion int          `json:"schemaVersion"`
	Servers       []curatedMCP `json:"servers"`
}

func validMCPText(value string, limit int) bool {
	return value != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= limit && strings.IndexFunc(value, unicode.IsControl) < 0
}

// Remote endpoints must be HTTPS. A loopback HTTP endpoint is accepted for
// future local servers; userinfo, query and fragment are never allowed.
func validMCPUURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	if u.Scheme == "https" {
		return u.Host != ""
	}
	return u.Scheme == "http" && u.Hostname() == "127.0.0.1" && u.Port() != ""
}

func validMCPLink(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == ""
}

func parseMCPCatalog(data []byte) ([]curatedMCP, error) {
	var catalog curatedMCPCatalog
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&catalog); err != nil {
		return nil, err
	}
	if decoder.More() {
		return nil, errors.New("trailing data after MCP catalog")
	}
	if catalog.SchemaVersion != 1 {
		return nil, fmt.Errorf("unsupported MCP catalog schema %d", catalog.SchemaVersion)
	}
	if len(catalog.Servers) == 0 || len(catalog.Servers) > mcpCatalogMax {
		return nil, errors.New("MCP catalog is empty or too long")
	}
	ids, envs := map[string]bool{}, map[string]bool{}
	for _, s := range catalog.Servers {
		if !mcpIDPattern.MatchString(s.ID) {
			return nil, fmt.Errorf("invalid MCP id %q", s.ID)
		}
		if ids[s.ID] {
			return nil, fmt.Errorf("duplicate MCP %q", s.ID)
		}
		ids[s.ID] = true
		if !validMCPText(s.Name.EN, mcpNameMax) || !validMCPText(s.Name.ES, mcpNameMax) {
			return nil, fmt.Errorf("invalid MCP name for %q", s.ID)
		}
		if !validMCPText(s.Description.EN, mcpDescriptionMax) || !validMCPText(s.Description.ES, mcpDescriptionMax) {
			return nil, fmt.Errorf("invalid MCP description for %q", s.ID)
		}
		if s.Transport != "http" || !validMCPUURL(s.URL) {
			return nil, fmt.Errorf("invalid MCP transport or URL for %q", s.ID)
		}
		if !mcpAuths[s.Auth] {
			return nil, fmt.Errorf("invalid MCP auth for %q", s.ID)
		}
		if s.DocsURL != "" && !validMCPLink(s.DocsURL) {
			return nil, fmt.Errorf("invalid docs URL for %q", s.ID)
		}
		if s.TokenURL != "" && !validMCPLink(s.TokenURL) {
			return nil, fmt.Errorf("invalid token URL for %q", s.ID)
		}
		for _, scope := range s.Scopes {
			if !validMCPText(scope, mcpScopeMax) {
				return nil, fmt.Errorf("invalid scope for %q", s.ID)
			}
		}
		for _, link := range []string{s.AuthorizationURL, s.RegistrationURL, s.ClientMetadataURL} {
			if link != "" && !validMCPLink(link) {
				return nil, fmt.Errorf("invalid OAuth override for %q", s.ID)
			}
		}
		if s.Auth == "oauth" {
			// An OAuth access token expires, so it is only ever injected at launch.
			if s.Header == "" || s.ValuePrefix == "" || s.EnvVar == "" {
				return nil, fmt.Errorf("MCP %q needs a header, value prefix and launch variable for OAuth", s.ID)
			}
			if len(s.Modes) != 1 || s.Modes[0] != "B" {
				return nil, fmt.Errorf("MCP %q must declare mode B only for OAuth", s.ID)
			}
			if !mcpEnvVarPattern.MatchString(s.EnvVar) {
				return nil, fmt.Errorf("invalid env var for %q", s.ID)
			}
			if envs[s.EnvVar] {
				return nil, fmt.Errorf("duplicate env var for %q", s.ID)
			}
			envs[s.EnvVar] = true
			continue
		}
		if s.Auth != "token" {
			if s.Header != "" || s.ValuePrefix != "" || s.EnvVar != "" || len(s.Modes) > 0 {
				return nil, fmt.Errorf("MCP %q must not declare token fields for %q auth", s.ID, s.Auth)
			}
			continue
		}
		if !mcpHeaderPattern.MatchString(s.Header) {
			return nil, fmt.Errorf("MCP %q needs a plain header name for token auth", s.ID)
		}
		if strings.ContainsAny(s.ValuePrefix, "\x00\r\n") {
			return nil, fmt.Errorf("invalid value prefix for %q", s.ID)
		}
		if len(s.Modes) == 0 {
			return nil, fmt.Errorf("MCP %q declares no delivery mode", s.ID)
		}
		modeB := false
		for _, mode := range s.Modes {
			if !mcpModes[mode] {
				return nil, fmt.Errorf("invalid delivery mode %q for %q", mode, s.ID)
			}
			modeB = modeB || mode == "B"
		}
		if modeB && s.EnvVar == "" {
			return nil, fmt.Errorf("MCP %q declares mode B but no env var", s.ID)
		}
		if s.EnvVar != "" {
			if !mcpEnvVarPattern.MatchString(s.EnvVar) {
				return nil, fmt.Errorf("invalid env var for %q", s.ID)
			}
			if envs[s.EnvVar] {
				return nil, fmt.Errorf("duplicate env var for %q", s.ID)
			}
			envs[s.EnvVar] = true
		}
	}
	return catalog.Servers, nil
}

func builtinMCPCatalog() []curatedMCP {
	servers, err := parseMCPCatalog(embeddedMCPCatalog)
	if err != nil {
		// The embedded file is validated by tests; a broken build shows no catalog.
		return nil
	}
	return servers
}

type mcpCatalogCache struct {
	endpoint  string
	expiresAt time.Time
	data      []curatedMCP
}

// mcpCatalog returns the freshest catalog available: a short-lived remote copy
// when the real gateway is in use, the compiled list otherwise or on failure.
func (a *app) mcpCatalog(ctx context.Context) []curatedMCP {
	a.mu.Lock()
	endpoint := a.mcpCatalogURL
	if endpoint == "" && a.upstream.Scheme == "https" && a.upstream.Host == "api.kilo.ai" {
		endpoint = mcpCatalogEndpoint
	}
	// Synthetic/custom gateways use the compiled list and make no GitHub request.
	if endpoint == "" {
		a.mu.Unlock()
		return builtinMCPCatalog()
	}
	cache := &a.mcpCatalogCache
	if cache.endpoint == endpoint && time.Now().Before(cache.expiresAt) {
		data := cache.data
		a.mu.Unlock()
		return data
	}
	var previous []curatedMCP
	if cache.endpoint == endpoint {
		previous = cache.data
	}
	transport := a.transport
	a.mu.Unlock()

	data, err := fetchMCPCatalog(ctx, endpoint, transport)
	ttl := time.Hour
	if err != nil {
		data, ttl = previous, 5*time.Minute
		if data == nil {
			data = builtinMCPCatalog()
		}
	}
	a.mu.Lock()
	a.mcpCatalogCache = mcpCatalogCache{endpoint: endpoint, data: data, expiresAt: time.Now().Add(ttl)}
	a.mu.Unlock()
	return data
}

func fetchMCPCatalog(ctx context.Context, endpoint string, transport http.RoundTripper) ([]curatedMCP, error) {
	ctx, cancel := context.WithTimeout(ctx, mcpCatalogTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if req.URL.User != nil {
		return nil, errors.New("catalog URL must not contain credentials")
	}
	// Unauthenticated and independent of the gateway request: no Kilo key,
	// organization header, local key or cookies reach GitHub.
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Kilo-Proxy/"+version)
	client := &http.Client{Transport: transport, Timeout: mcpCatalogTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("MCP catalog unavailable")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, mcpCatalogLimit+1))
	if err != nil || len(body) > mcpCatalogLimit {
		return nil, errors.New("invalid MCP catalog response")
	}
	return parseMCPCatalog(body)
}
