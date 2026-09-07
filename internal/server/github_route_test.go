package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// mirrorFixture is an httptest accelerator that can answer probes (OK) and
// API forwards (OK or a 403 rate-limit rejection) independently.
type mirrorFixture struct {
	server    *httptest.Server
	apiHits   int
	probeHits int
	rejectAPI bool
	delay     time.Duration
}

func newMirrorFixture(t *testing.T, rejectAPI bool, delay time.Duration) *mirrorFixture {
	t.Helper()
	m := &mirrorFixture{rejectAPI: rejectAPI, delay: delay}
	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uri := r.RequestURI
		if strings.Contains(uri, "api.github.com") {
			m.apiHits++
			if m.rejectAPI {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"message":"API rate limit exceeded for 1.2.3.4.","documentation_url":"https://docs.github.com"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tag_name":"v9.9.9"}`))
			return
		}
		m.probeHits++
		if m.delay > 0 {
			time.Sleep(m.delay)
		}
		_, _ = w.Write([]byte(strings.Repeat("probe-body-", 40)))
	}))
	t.Cleanup(m.server.Close)
	return m
}

func withTestAcceleratorPool(t *testing.T, prefixes ...string) {
	t.Helper()
	original := builtinGitHubAcceleratorPrefixes
	resetAcceleratorManagerForTest(prefixes)
	t.Cleanup(func() { resetAcceleratorManagerForTest(original) })
}

func TestGitHubJSONMetadataNeverTransitsMirror(t *testing.T) {
	mirror := newMirrorFixture(t, false, 0)
	withTestAcceleratorPool(t, mirror.server.URL)
	app := newTestApp(t)
	if snapshot := app.refreshAcceleratorSnapshot(context.Background()); snapshot.Best != mirror.server.URL {
		t.Fatalf("probe winner = %q (results: %#v)", snapshot.Best, snapshot.Results)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var release githubRelease
	_ = app.fetchGitHubJSONContext(ctx, "https://api.github.com/repos/scoltzero/msf/releases/latest", &release)
	if mirror.apiHits != 0 {
		t.Fatalf("release metadata transited the public mirror %d times", mirror.apiHits)
	}
}

func TestGitHubJSONTokenNeverTransitsMirror(t *testing.T) {
	mirror := newMirrorFixture(t, false, 0)
	withTestAcceleratorPool(t, mirror.server.URL)
	app := newTestApp(t)
	if err := app.saveGitHubToken("ghp_token1234567890abcdef"); err != nil {
		t.Fatal(err)
	}

	// Cancelled context: the request itself fails immediately, but the
	// routing decision is still observable — the mirror must never be hit.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var release githubRelease
	_ = app.fetchGitHubJSONContext(ctx, "https://api.github.com/repos/scoltzero/msf/releases/latest", &release)
	if mirror.apiHits != 0 {
		t.Fatalf("tokened request transited the public mirror %d times", mirror.apiHits)
	}
}

func TestGitHubAcceleratorOffModeDoesNotProbe(t *testing.T) {
	mirror := newMirrorFixture(t, false, 0)
	withTestAcceleratorPool(t, mirror.server.URL)
	app := newTestApp(t)
	app.setSetting(settingAcceleratorMode, "off")
	snapshot := app.refreshAcceleratorSnapshot(context.Background())
	if snapshot.Mode != "off" || snapshot.Best != "" || len(snapshot.Results) != 0 {
		t.Fatalf("off-mode snapshot = %#v", snapshot)
	}
	if mirror.probeHits != 0 {
		t.Fatalf("off mode probed public accelerators %d times", mirror.probeHits)
	}
}

func TestGitHubJSONTokenSendsBearer(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v1.0.0"}`))
	}))
	defer server.Close()

	app := newTestApp(t)
	if err := app.saveGitHubToken("ghp_token1234567890abcdef"); err != nil {
		t.Fatal(err)
	}
	var release githubRelease
	// Non-GitHub host: routed verbatim, so the httptest URL is reachable.
	if err := app.fetchGitHubJSONOnce(context.Background(), server.URL, false, "ghp_token1234567890abcdef", &release, "token"); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer ghp_token1234567890abcdef" {
		t.Fatalf("Authorization header = %q, want Bearer token", gotAuth)
	}
}

func TestGitHubAcceleratorsPUTAndMaskedToken(t *testing.T) {
	withTestAcceleratorPool(t) // empty pool: no outbound probes during GET
	app := newTestApp(t)
	token := tokenForRole(t, app, "admin")

	res := requestJSON(t, app, http.MethodPut, "/api/v1/github/accelerators", token, map[string]any{
		"mode":           "manual",
		"extra_prefixes": []string{"https://mirror.a.example", "https://mirror.b.example"},
		"github_token":   "ghp_token1234567890abcdef",
	})
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"success":true`) {
		t.Fatalf("PUT accelerators failed: status=%d body=%s", res.Code, res.Body.String())
	}

	res = requestJSON(t, app, http.MethodGet, "/api/v1/github/accelerators", token, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("GET accelerators failed: status=%d", res.Code)
	}
	var payload struct {
		Success bool `json:"success"`
		Data    struct {
			Mode              string   `json:"mode"`
			ExtraPrefixes     []string `json:"extra_prefixes"`
			GitHubTokenMasked string   `json:"github_token_masked"`
		} `json:"data"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Mode != "manual" {
		t.Fatalf("mode = %q, want manual", payload.Data.Mode)
	}
	if len(payload.Data.ExtraPrefixes) != 2 || payload.Data.ExtraPrefixes[0] != "https://mirror.a.example" {
		t.Fatalf("extra_prefixes = %#v", payload.Data.ExtraPrefixes)
	}
	if payload.Data.GitHubTokenMasked != "ghp_******cdef" {
		t.Fatalf("masked token = %q", payload.Data.GitHubTokenMasked)
	}
	if full := res.Body.String(); strings.Contains(full, "ghp_token1234567890abcdef") {
		t.Fatal("raw token leaked through the accelerators endpoint")
	}
	var legacyCount int
	if err := app.DB.QueryRow(`select count(*) from settings where key=?`, settingGitHubToken).Scan(&legacyCount); err != nil || legacyCount != 0 {
		t.Fatalf("plaintext GitHub token key remains: count=%d err=%v", legacyCount, err)
	}
	var encrypted string
	if err := app.DB.QueryRow(`select value from settings where key=?`, settingGitHubTokenCiphertext).Scan(&encrypted); err != nil || encrypted == "" || strings.Contains(encrypted, "ghp_token") {
		t.Fatalf("encrypted GitHub token storage invalid: value=%q err=%v", encrypted, err)
	}
	settings := requestJSON(t, app, http.MethodGet, "/api/v1/settings", token, nil)
	if body := settings.Body.String(); strings.Contains(body, "ghp_token1234567890abcdef") || strings.Contains(body, settingGitHubTokenCiphertext) || strings.Contains(body, settingGitHubTokenNonce) {
		t.Fatalf("generic settings response exposed GitHub token material: %s", body)
	}

	res = requestJSON(t, app, http.MethodPut, "/api/v1/github/accelerators", token, map[string]any{"reset_token": true})
	if res.Code != http.StatusOK {
		t.Fatalf("token reset failed: status=%d body=%s", res.Code, res.Body.String())
	}
	if masked := maskGitHubToken(app.githubToken()); masked != "" {
		t.Fatalf("token not cleared: %q", masked)
	}

	res = requestJSON(t, app, http.MethodPut, "/api/v1/github/accelerators", token, map[string]any{"mode": "bogus"})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("bogus mode must 400, got %d", res.Code)
	}
}

func TestFriendlyGitHubAPIError(t *testing.T) {
	err := friendlyGitHubAPIError(stringsToError("github api 403 Forbidden: {\"message\":\"API rate limit exceeded for 1.2.3.4.\"}"))
	if err == nil || !strings.Contains(err.Error(), "匿名限流") || !strings.Contains(err.Error(), "5000") {
		t.Fatalf("rate-limit hint missing: %v", err)
	}
	err = friendlyGitHubAPIError(stringsToError("github api 401 Unauthorized: {\"message\":\"Bad credentials\"}"))
	if err == nil || !strings.Contains(err.Error(), "Token 无效") {
		t.Fatalf("bad-credentials hint missing: %v", err)
	}
	if err := friendlyGitHubAPIError(stringsToError("github api 404 Not Found: {}")); err == nil || strings.Contains(err.Error(), "限流") {
		t.Fatalf("404 must stay untouched: %v", err)
	}
}

func stringsToError(msg string) error { return &testError{msg} }

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }

func TestMaskGitHubToken(t *testing.T) {
	cases := map[string]string{
		"":                        "",
		"short":                   "****",
		"ghp_token1234567890abcd": "ghp_******abcd",
	}
	for input, want := range cases {
		if got := maskGitHubToken(input); got != want {
			t.Fatalf("maskGitHubToken(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestGitHubTokenLegacyStorageMigratesWithoutChangingValue(t *testing.T) {
	app := newTestApp(t)
	const token = "ghp_legacytoken1234567890"
	app.setSetting(settingGitHubToken, token)
	if err := app.migrateGitHubTokenStorage(); err != nil {
		t.Fatal(err)
	}
	if got := app.githubToken(); got != token {
		t.Fatalf("migrated token = %q", got)
	}
	var count int
	if err := app.DB.QueryRow(`select count(*) from settings where key=?`, settingGitHubToken).Scan(&count); err != nil || count != 0 {
		t.Fatalf("legacy plaintext key remains: count=%d err=%v", count, err)
	}
	const replacement = "ghp_replacement1234567890"
	admin := tokenForRole(t, app, "admin")
	res := requestJSON(t, app, http.MethodPut, "/api/v1/settings", admin, map[string]any{settingGitHubToken: replacement})
	if res.Code != http.StatusOK || app.githubToken() != replacement {
		t.Fatalf("legacy settings endpoint did not encrypt replacement token: status=%d body=%s", res.Code, res.Body.String())
	}
	if err := app.DB.QueryRow(`select count(*) from settings where key=?`, settingGitHubToken).Scan(&count); err != nil || count != 0 {
		t.Fatalf("legacy settings endpoint stored plaintext token: count=%d err=%v", count, err)
	}
}
