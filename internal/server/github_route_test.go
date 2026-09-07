package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// mirrorFixture is an httptest accelerator that can answer probes (OK) and
// API forwards (OK or a 403 rate-limit rejection) independently.
type mirrorFixture struct {
	server     *httptest.Server
	apiHits    int
	probeHits  int
	rejectAPI  bool
	delay      time.Duration
	lastAPIURI atomic.Value
}

func newMirrorFixture(t *testing.T, rejectAPI bool, delay time.Duration) *mirrorFixture {
	t.Helper()
	m := &mirrorFixture{rejectAPI: rejectAPI, delay: delay}
	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uri := r.RequestURI
		if strings.Contains(uri, "api.github.com") {
			m.apiHits++
			m.lastAPIURI.Store(uri)
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

func TestGitHubJSONNeverRoutesMetadataThroughMirror(t *testing.T) {
	// A healthy mirror must never receive api.github.com metadata requests:
	// the metadata carries both the asset URL and its digest, so a hostile
	// mirror could forge a matching pair and defeat download verification.
	// The mirrored URL would be mirror+"/https://api.github.com/..." — any
	// hit on the fixture's api path means the regression is back.
	mirror := newMirrorFixture(t, false, 0)
	withTestAcceleratorPool(t, mirror.server.URL)
	app := newTestApp(t)
	if snapshot := app.refreshAcceleratorSnapshot(context.Background()); snapshot.Best != mirror.server.URL {
		t.Fatalf("probe winner = %q (results: %#v)", snapshot.Best, snapshot.Results)
	}
	// A URL whose host is not GitHub routes verbatim even with a live mirror:
	// the received URI must be exactly what was requested — any accelerator
	// prefix would show up here.
	var release githubRelease
	verbatim := mirror.server.URL + "/api.github.com/repos/x/y/releases/latest"
	if err := app.fetchGitHubJSONContext(context.Background(), verbatim, &release); err != nil {
		t.Fatalf("verbatim route failed: %v (apiHits=%d probeHits=%d)", err, mirror.apiHits, mirror.probeHits)
	}
	if release.TagName != "v9.9.9" {
		t.Fatalf("payload not decoded: %#v", release)
	}
	// RequestURI 不含 scheme/host：等于请求路径即证明无镜像前缀拼接。
	if got, _ := mirror.lastAPIURI.Load().(string); got != "/api.github.com/repos/x/y/releases/latest" {
		t.Fatalf("metadata request URI = %q, want verbatim path (mirror prefixing is back)", got)
	}
}

func TestGitHubJSONRetriesDirectLineAfterFailure(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"API rate limit exceeded for 1.2.3.4.","documentation_url":"https://docs.github.com"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v9.9.9"}`))
	}))
	defer server.Close()

	app := newTestApp(t)
	var release githubRelease
	if err := app.fetchGitHubJSONContext(context.Background(), server.URL+"/releases/latest", &release); err != nil {
		t.Fatalf("direct retry should recover after a failed first line: %v", err)
	}
	if release.TagName != "v9.9.9" {
		t.Fatalf("retry payload not decoded: %#v", release)
	}
	if hits.Load() != 2 {
		t.Fatalf("total attempts = %d, want 2 (trusted line + direct retry)", hits.Load())
	}
}

func TestGitHubReleaseAssetURLAcceptsOnlyGitHubDownloads(t *testing.T) {
	ok := []string{
		"https://github.com/MetaCubeX/mihomo/releases/latest/download/mihomo-linux-amd64.gz",
		"https://github.com/vernesong/mihomo/releases/download/Prerelease-Alpha/mihomo-linux-arm64-v3.gz",
	}
	for _, raw := range ok {
		if got, err := githubReleaseAssetURL(raw); err != nil || got != raw {
			t.Fatalf("githubReleaseAssetURL(%q) = %q, %v; want verbatim acceptance", raw, got, err)
		}
	}
	bad := map[string]string{
		"":                               "empty",
		"https://evil.example/mihomo.gz": "foreign host",
		"http://github.com/owner/repo/releases/download/v1/a.gz": "plaintext http",
		"https://github.com/owner/repo/archive/v1.tar.gz":        "not a release download",
		"https://api.github.com/repos/o/r/releases/1":            "api endpoint, not asset",
	}
	for raw := range bad {
		if got, err := githubReleaseAssetURL(raw); err == nil {
			t.Fatalf("githubReleaseAssetURL(%q) accepted %q", raw, got)
		}
	}
	// Forged metadata must not leak an executable route into self-update or
	// component downloads: releaseAssetURL drops such assets entirely.
	forged := githubRelease{
		TagName: "v1.2.3",
		Assets: []githubAsset{
			{Name: "msf-linux-amd64.tar.gz", BrowserDownloadURL: "https://evil.example/msf-linux-amd64.tar.gz"},
			{Name: "msf-linux-arm64.tar.gz", BrowserDownloadURL: "https://github.com/scoltzero/msf/releases/download/v1.2.3/msf-linux-arm64.tar.gz"},
		},
	}
	if got := releaseAssetURL(forged, "linux-amd64", ".tar.gz"); got != "" {
		t.Fatalf("forged asset URL accepted: %q", got)
	}
	if got := releaseAssetURL(forged, "linux-arm64", ".tar.gz"); got != "https://github.com/scoltzero/msf/releases/download/v1.2.3/msf-linux-arm64.tar.gz" {
		t.Fatalf("legit asset not selected: %q", got)
	}
}

func TestGitHubJSONTokenNeverTransitsMirror(t *testing.T) {
	mirror := newMirrorFixture(t, false, 0)
	withTestAcceleratorPool(t, mirror.server.URL)
	app := newTestApp(t)
	app.setSetting(settingGitHubToken, "ghp_token1234567890abcdef")

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

func TestGitHubJSONTokenSendsBearer(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v1.0.0"}`))
	}))
	defer server.Close()

	app := newTestApp(t)
	app.setSetting(settingGitHubToken, "ghp_token1234567890abcdef")
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
