package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// oauthStandIn is a minimal MCP authorization server: Protected Resource
// Metadata, Authorization Server Metadata, Dynamic Client Registration and a
// token endpoint that honours PKCE S256 and refreshes.
type oauthStandIn struct {
	*httptest.Server
	mu           sync.Mutex
	challenge    string
	clientID     string
	accessIssued int
	lastForm     url.Values
	denyToken    bool
}

func newOAuthStandIn(t *testing.T) *oauthStandIn {
	t.Helper()
	o := &oauthStandIn{clientID: "stand-in-client"}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"resource": o.URL + "/mcp", "authorization_servers": []string{o.URL}})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issuer": o.URL, "authorization_endpoint": o.URL + "/authorize", "token_endpoint": o.URL + "/token",
			"registration_endpoint": o.URL + "/register", "code_challenge_methods_supported": []string{"S256"},
		})
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			w.WriteHeader(400)
			return
		}
		if uris, _ := body["redirect_uris"].([]any); len(uris) != 1 {
			w.WriteHeader(400)
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"client_id": o.clientID})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		o.mu.Lock()
		o.lastForm = r.PostForm
		deny := o.denyToken
		challenge := o.challenge
		o.mu.Unlock()
		if deny {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch r.PostForm.Get("grant_type") {
		case "authorization_code":
			// Prove PKCE: the verifier must hash to the challenge Kilo sent.
			sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
			if challenge == "" || base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			o.mu.Lock()
			o.accessIssued++
			n := o.accessIssued
			o.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"access_token": fmt.Sprintf("access-%d", n), "refresh_token": "refresh-1", "token_type": "Bearer", "expires_in": 3600})
		case "refresh_token":
			if r.PostForm.Get("refresh_token") != "refresh-1" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			o.mu.Lock()
			o.accessIssued++
			n := o.accessIssued
			o.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"access_token": fmt.Sprintf("access-%d", n), "token_type": "Bearer", "expires_in": 3600})
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	o.Server = httptest.NewServer(mux)
	t.Cleanup(o.Close)
	return o
}

func (o *oauthStandIn) noteChallenge(challenge string) {
	o.mu.Lock()
	o.challenge = challenge
	o.mu.Unlock()
}

func oauthTestServer(o *oauthStandIn) curatedMCP {
	return curatedMCP{
		ID: "amplitude", Name: mcpText{EN: "Amplitude", ES: "Amplitude"}, Description: mcpText{EN: "d", ES: "d"},
		URL: o.URL + "/mcp", Transport: "http", Auth: "oauth",
		Header: "Authorization", ValuePrefix: "Bearer ", EnvVar: "KILO_MCP_AMPLITUDE_TOKEN", Modes: []string{"B"},
	}
}

func waitForMCPOAuth(t *testing.T, a *app, want string) *mcpOAuthSession {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		var snapshot *mcpOAuthSession
		if a.mcpOAuthFlow != nil {
			copied := *a.mcpOAuthFlow
			copied.cancel = nil
			snapshot = &copied
		}
		a.mu.Unlock()
		if snapshot != nil && snapshot.Status == want {
			return snapshot
		}
		if snapshot != nil && (snapshot.Status == "error" || snapshot.Status == "cancelled") && want != snapshot.Status {
			t.Fatalf("flow ended as %q: %s", snapshot.Status, snapshot.Message)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for status %q", want)
	return nil
}

func TestMCPOAuthFullFlowStoresCredentials(t *testing.T) {
	stand := newOAuthStandIn(t)
	dir := t.TempDir()
	vault := mcpTestVault{}
	a := mcpTestApp(dir, vault)
	server := oauthTestServer(stand)

	if connected := a.mcpOAuthConnected(server.ID); connected {
		t.Fatal("a fresh install must not report a sign-in")
	}
	if _, err := a.beginMCPOAuth(server); err != nil {
		t.Fatal(err)
	}
	pending := waitForMCPOAuth(t, a, "pending")
	if pending.AuthorizationURL == "" {
		t.Fatal("the user must receive an authorization URL")
	}
	authorize, err := url.Parse(pending.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	query := authorize.Query()
	if query.Get("code_challenge_method") != "S256" || query.Get("code_challenge") == "" || query.Get("state") == "" {
		t.Fatalf("authorization request is not PKCE/state protected: %s", authorize.RawQuery)
	}
	if query.Get("resource") != server.URL {
		t.Fatalf("the resource parameter is missing: %s", authorize.RawQuery)
	}
	if query.Get("client_id") != stand.clientID {
		t.Fatalf("registered client id was not used: %s", query.Get("client_id"))
	}
	stand.noteChallenge(query.Get("code_challenge"))

	// Simulate the browser hitting the loopback redirect.
	redirect, err := url.Parse(query.Get("redirect_uri"))
	if err != nil || redirect.Hostname() != "127.0.0.1" {
		t.Fatalf("redirect must be loopback: %v", query.Get("redirect_uri"))
	}
	callback := *redirect
	callbackQuery := callback.Query()
	callbackQuery.Set("code", "auth-code-1")
	callbackQuery.Set("state", query.Get("state"))
	callback.RawQuery = callbackQuery.Encode()
	resp, err := http.Get(callback.String())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	waitForMCPOAuth(t, a, "connected")
	if !a.mcpOAuthConnected(server.ID) {
		t.Fatal("credentials must be stored after the exchange")
	}
	token, err := a.mcpOAuthAccessToken(context.Background(), server.ID)
	if err != nil || token != "access-1" {
		t.Fatalf("unexpected access token %q (%v)", token, err)
	}
	// The credential is stored as JSON in the vault, not in any config file.
	raw, ok := vault[mcpOAuthSlot(server.ID)]
	if !ok || !strings.Contains(raw, "refresh-1") || !strings.Contains(raw, "\"clientId\"") {
		t.Fatalf("vault contents are wrong: %q", raw)
	}
}

func TestMCPOAuthRefreshReplacesAnExpiredToken(t *testing.T) {
	stand := newOAuthStandIn(t)
	a := mcpTestApp(t.TempDir(), mcpTestVault{})
	server := oauthTestServer(stand)

	expired, _ := mcpOAuthJSON(mcpOAuthCredentials{
		ClientID: "stand-in-client", AccessToken: "stale", RefreshToken: "refresh-1",
		TokenURL: stand.URL + "/token", ExpiresAt: time.Now().Add(-time.Hour).Unix(), Resource: server.URL,
	})
	if err := a.vault.Set(mcpOAuthSlot(server.ID), expired); err != nil {
		t.Fatal(err)
	}
	token, err := a.mcpOAuthAccessToken(context.Background(), server.ID)
	if err != nil || token == "stale" || token == "" {
		t.Fatalf("an expired token must be refreshed, got %q (%v)", token, err)
	}
	// The refreshed credential is persisted, and the refresh token is retained
	// even though the response omitted it.
	saved, _ := a.mcpOAuthCredentials(server.ID)
	if saved.RefreshToken != "refresh-1" || saved.AccessToken != token {
		t.Fatalf("refreshed credentials were not persisted: %+v", saved)
	}
}

func TestMCPOAuthReportsFailureWithoutStoringAnything(t *testing.T) {
	stand := newOAuthStandIn(t)
	stand.mu.Lock()
	stand.denyToken = true
	stand.mu.Unlock()
	vault := mcpTestVault{}
	a := mcpTestApp(t.TempDir(), vault)
	server := oauthTestServer(stand)

	if _, err := a.beginMCPOAuth(server); err != nil {
		t.Fatal(err)
	}
	pending := waitForMCPOAuth(t, a, "pending")
	authorize, _ := url.Parse(pending.AuthorizationURL)
	query := authorize.Query()
	stand.noteChallenge(query.Get("code_challenge"))
	callback, _ := url.Parse(query.Get("redirect_uri"))
	q := callback.Query()
	q.Set("code", "auth-code-1")
	q.Set("state", query.Get("state"))
	callback.RawQuery = q.Encode()
	resp, err := http.Get(callback.String())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	failed := waitForMCPOAuth(t, a, "error")
	if failed.Message == "" {
		t.Fatal("a failed exchange must explain itself")
	}
	if a.mcpOAuthConnected(server.ID) {
		t.Fatal("a failed exchange must not store credentials")
	}
	if len(vault) != 0 {
		t.Fatalf("nothing should be written: %v", vault)
	}
}

func TestMCPOAuthStateMismatchIsRejected(t *testing.T) {
	stand := newOAuthStandIn(t)
	vault := mcpTestVault{}
	a := mcpTestApp(t.TempDir(), vault)
	server := oauthTestServer(stand)
	if _, err := a.beginMCPOAuth(server); err != nil {
		t.Fatal(err)
	}
	pending := waitForMCPOAuth(t, a, "pending")
	authorize, _ := url.Parse(pending.AuthorizationURL)
	query := authorize.Query()
	callback, _ := url.Parse(query.Get("redirect_uri"))
	q := callback.Query()
	q.Set("code", "auth-code-1")
	q.Set("state", "not-the-issued-state")
	callback.RawQuery = q.Encode()
	resp, err := http.Get(callback.String())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if failed := waitForMCPOAuth(t, a, "error"); failed.Message == "" {
		t.Fatal("a mismatched state must fail with a message")
	}
	if a.mcpOAuthConnected(server.ID) || len(vault) != 0 {
		t.Fatal("a mismatched state must never store credentials")
	}
}

func TestMCPOAuthSecondFlowIsRefused(t *testing.T) {
	stand := newOAuthStandIn(t)
	a := mcpTestApp(t.TempDir(), mcpTestVault{})
	server := oauthTestServer(stand)
	if _, err := a.beginMCPOAuth(server); err != nil {
		t.Fatal(err)
	}
	defer func() {
		a.mu.Lock()
		a.cancelMCPOAuthLocked()
		a.mu.Unlock()
	}()
	if _, err := a.beginMCPOAuth(server); err == nil {
		t.Fatal("a second concurrent flow must be refused")
	}
}

func TestMCPOAuthAccessTokenRequiresASignIn(t *testing.T) {
	a := mcpTestApp(t.TempDir(), mcpTestVault{})
	if _, err := a.mcpOAuthAccessToken(context.Background(), "amplitude"); err == nil {
		t.Fatal("a missing sign-in must be reported, not silently returned")
	}
}

func TestMCPOAuthCredentialsValidation(t *testing.T) {
	good := mcpOAuthCredentials{ClientID: "c", AccessToken: "a", TokenURL: "https://as.example/token", RefreshToken: "r"}
	if !validMCPOAuthCredentials(good) {
		t.Fatal("a complete credential must validate")
	}
	for name, mutate := range map[string]func(*mcpOAuthCredentials){
		"no client":    func(c *mcpOAuthCredentials) { c.ClientID = "" },
		"no token":     func(c *mcpOAuthCredentials) { c.AccessToken = "" },
		"insecure url": func(c *mcpOAuthCredentials) { c.TokenURL = "http://as.example/token" },
		"newline":      func(c *mcpOAuthCredentials) { c.AccessToken = "a\nb" },
		"expired without refresh": func(c *mcpOAuthCredentials) {
			c.RefreshToken, c.ExpiresAt = "", time.Now().Add(-time.Hour).Unix()
		},
	} {
		c := good
		mutate(&c)
		if validMCPOAuthCredentials(c) {
			t.Fatalf("%s: expected rejection", name)
		}
	}
}

func TestMCPLaunchEnvRefreshesOAuthServers(t *testing.T) {
	stand := newOAuthStandIn(t)
	dir := t.TempDir()
	a := mcpTestApp(dir, mcpTestVault{})
	server := oauthTestServer(stand)
	expired, _ := mcpOAuthJSON(mcpOAuthCredentials{
		ClientID: "stand-in-client", AccessToken: "stale", RefreshToken: "refresh-1",
		TokenURL: stand.URL + "/token", ExpiresAt: time.Now().Add(-time.Hour).Unix(), Resource: server.URL,
	})
	if err := a.vault.Set(mcpOAuthSlot(server.ID), expired); err != nil {
		t.Fatal(err)
	}
	a.mcpCatalogURL = "local" // keep the embedded catalog for this test
	file := emptyMCPStateFile()
	file.Servers[server.ID] = mcpServerState{Enabled: true, Mode: "B"}
	if err := newMCPStateStore(dir).save(file, false); err != nil {
		t.Fatal(err)
	}
	// Inject using the stand-in catalog entry so the test does not depend on the
	// shipped list.
	if env := a.mcpLaunchEnvFor(context.Background(), []curatedMCP{server}); env[server.EnvVar] == "" || env[server.EnvVar] == "stale" {
		t.Fatalf("a launch must inject a fresh OAuth token: %v", env)
	}
}

// TestMCPOAuthCancelledFlowIgnoresTheCallback covers the race where the user
// cancels while the browser is still open.
func TestMCPOAuthCancelledFlowIgnoresTheCallback(t *testing.T) {
	stand := newOAuthStandIn(t)
	vault := mcpTestVault{}
	a := mcpTestApp(t.TempDir(), vault)
	server := oauthTestServer(stand)
	if _, err := a.beginMCPOAuth(server); err != nil {
		t.Fatal(err)
	}
	pending := waitForMCPOAuth(t, a, "pending")
	authorize, _ := url.Parse(pending.AuthorizationURL)
	query := authorize.Query()
	stand.noteChallenge(query.Get("code_challenge"))

	a.mu.Lock()
	a.cancelMCPOAuthLocked()
	a.mu.Unlock()
	waitForMCPOAuth(t, a, "cancelled")

	callback, _ := url.Parse(query.Get("redirect_uri"))
	q := callback.Query()
	q.Set("code", "auth-code-1")
	q.Set("state", query.Get("state"))
	callback.RawQuery = q.Encode()
	if resp, err := http.Get(callback.String()); err == nil {
		resp.Body.Close()
	}
	time.Sleep(200 * time.Millisecond)
	if a.mcpOAuthConnected(server.ID) || len(vault) != 0 {
		t.Fatal("a cancelled flow must not store credentials")
	}
}
