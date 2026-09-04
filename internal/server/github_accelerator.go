package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Public GitHub accelerator prefixes, ordered by historical reliability.
// These services come and go (ghp.ci / ghgo.xyz are already dead, and even
// the long-lived gh-proxy.com rotated domains) — which is exactly why the
// winner is chosen by probing, never by trusting this list.  Downloads are
// still protected end-to-end by the SHA-256 digests from the GitHub release
// API, so a hijacked accelerator cannot serve tampered binaries.
var builtinGitHubAcceleratorPrefixes = []string{
	"https://gh-proxy.com/",
	"https://ghfast.top/",
	"https://gh.zwy.one/",
	"https://ghproxy.cxkpro.top/",
	"https://ghproxy.net/",
}

// A small, stable raw.githubusercontent.com file used purely for probing.
// Ranged to the first 2KB; the body is never used beyond "did it arrive".
const acceleratorProbeTarget = "https://raw.githubusercontent.com/hunshcn/gh-proxy/master/README.md"

const (
	acceleratorProbeTimeout = 6 * time.Second
	acceleratorCacheTTL     = 10 * time.Minute

	settingAcceleratorMode          = "github_accelerator_mode"           // auto|manual|off ("" = auto)
	settingAcceleratorExtraPrefixes = "github_accelerator_extra_prefixes" // user-supplied, comma separated
	settingAcceleratorBest          = "github_accelerator_last_best"      // observability: last winner
	settingGitHubToken              = "github_token"                      // optional PAT, lifts api.github.com quota 60/h -> 5000/h
)

type acceleratorProbeResult struct {
	Prefix    string `json:"prefix"`
	Source    string `json:"source"` // builtin|extra|manual
	OK        bool   `json:"ok"`
	LatencyMS int64  `json:"latency_ms"`
	Status    int    `json:"status,omitempty"`
	Error     string `json:"error,omitempty"`
}

type acceleratorSnapshot struct {
	Best     string                    `json:"best_prefix"`
	Mode     string                    `json:"mode"`
	ProbedAt time.Time                 `json:"probed_at"`
	Results  []acceleratorProbeResult `json:"results"`
}

type acceleratorCandidate struct {
	Prefix string
	Source string
}

type acceleratorManager struct {
	mu       sync.Mutex
	snapshot acceleratorSnapshot
	probedAt time.Time
	probing  bool
}

var accelerators = &acceleratorManager{}

// resetAcceleratorManagerForTest gives tests an empty cache and a injected
// built-in pool; call with the original slice to restore.
func resetAcceleratorManagerForTest(original []string) {
	accelerators = &acceleratorManager{}
	builtinGitHubAcceleratorPrefixes = original
}


func (a *App) acceleratorMode() string {
	switch strings.ToLower(strings.TrimSpace(a.setting(settingAcceleratorMode, ""))) {
	case "off":
		return "off"
	case "manual":
		return "manual"
	case "auto":
		return "auto"
	}
	// Legacy mapping (mode key unset): an enabled accelerator URL is an
	// explicit operator pin — keep the pre-auto behaviour and use it
	// verbatim; otherwise default to the auto-probing pool.
	if a.manualAcceleratorPrefix() != "" {
		return "manual"
	}
	return "auto"
}

// acceleratorCandidates returns the probe pool: the operator's extras first
// (most intentional), then the legacy manual URL, then the built-ins.
func (a *App) acceleratorCandidates() []acceleratorCandidate {
	seen := map[string]bool{}
	out := make([]acceleratorCandidate, 0, len(builtinGitHubAcceleratorPrefixes)+3)
	add := func(prefix, source string) {
		prefix = strings.TrimRight(strings.TrimSpace(prefix), "/")
		if prefix == "" || !(strings.HasPrefix(prefix, "https://") || strings.HasPrefix(prefix, "http://")) || seen[prefix] {
			return
		}
		seen[prefix] = true
		out = append(out, acceleratorCandidate{Prefix: prefix, Source: source})
	}
	for _, field := range strings.Split(a.setting(settingAcceleratorExtraPrefixes, ""), ",") {
		add(field, "extra")
	}
	var enabled bool
	var manual sql.NullString
	_ = a.DB.QueryRow(`select github_accelerator_enabled,github_accelerator_url from system_setups order by id desc limit 1`).Scan(&enabled, &manual)
	if enabled {
		add(manual.String, "manual")
	}
	for _, prefix := range builtinGitHubAcceleratorPrefixes {
		add(prefix, "builtin")
	}
	return out
}

// probeGitHubAccelerators measures every candidate concurrently against the
// probe target and returns the ranked results (fastest OK first).
func (a *App) probeGitHubAccelerators(ctx context.Context) []acceleratorProbeResult {
	candidates := a.acceleratorCandidates()
	if len(candidates) == 0 {
		return nil
	}
	client := &http.Client{Timeout: acceleratorProbeTimeout}
	results := make([]acceleratorProbeResult, len(candidates))
	var wg sync.WaitGroup
	for i, candidate := range candidates {
		wg.Add(1)
		go func(index int, candidate acceleratorCandidate) {
			defer wg.Done()
			start := time.Now()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, candidate.Prefix+"/"+acceleratorProbeTarget, nil)
			if err != nil {
				results[index] = acceleratorProbeResult{Prefix: candidate.Prefix, Source: candidate.Source, Error: err.Error()}
				return
			}
			req.Header.Set("Range", "bytes=0-2047")
			req.Header.Set("User-Agent", "msf-accelerator-probe/1.0")
			resp, err := client.Do(req)
			latency := time.Since(start).Milliseconds()
			if err != nil {
				results[index] = acceleratorProbeResult{Prefix: candidate.Prefix, Source: candidate.Source, Error: err.Error()}
				return
			}
			body, _ := io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			ok := (resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent) && body > 100
			results[index] = acceleratorProbeResult{Prefix: candidate.Prefix, Source: candidate.Source, OK: ok, LatencyMS: latency, Status: resp.StatusCode}
		}(i, candidate)
	}
	wg.Wait()
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].OK != results[j].OK {
			return results[i].OK
		}
		return results[i].LatencyMS < results[j].LatencyMS
	})
	return results
}

func (a *App) refreshAcceleratorSnapshot(ctx context.Context) acceleratorSnapshot {
	accelerators.mu.Lock()
	if accelerators.probing || (time.Since(accelerators.probedAt) < acceleratorCacheTTL && accelerators.snapshot.Best != "") {
		snapshot := accelerators.snapshot
		accelerators.mu.Unlock()
		return snapshot
	}
	accelerators.probing = true
	accelerators.mu.Unlock()

	results := a.probeGitHubAccelerators(ctx)
	snapshot := acceleratorSnapshot{Mode: a.acceleratorMode(), ProbedAt: time.Now(), Results: results}
	for _, result := range results {
		if result.OK {
			snapshot.Best = result.Prefix
			break
		}
	}
	accelerators.mu.Lock()
	accelerators.snapshot = snapshot
	accelerators.probedAt = snapshot.ProbedAt
	accelerators.probing = false
	accelerators.mu.Unlock()
	if snapshot.Best != "" {
		a.setSetting(settingAcceleratorBest, snapshot.Best)
	}
	return snapshot
}

// bestGitHubAccelerator returns the prefix to prepend to GitHub download
// URLs, or "" when downloads should go direct.  It NEVER blocks on probing:
// a stale cache is warmed in the background, and until the winner is known
// downloads go direct (the failure-retry path is the one allowed to probe
// synchronously).
func (a *App) bestGitHubAccelerator() string {
	switch a.acceleratorMode() {
	case "off":
		return ""
	case "manual":
		// The operator pinned an endpoint: honour it verbatim.  Reachability
		// is still protected by the download-failure retry, which switches
		// routes when the pinned accelerator actually breaks.
		return a.manualAcceleratorPrefix()
	}
	accelerators.mu.Lock()
	fresh := time.Since(accelerators.probedAt) < acceleratorCacheTTL
	best := accelerators.snapshot.Best
	warming := accelerators.probing
	accelerators.mu.Unlock()
	if fresh || warming {
		return best
	}
	go func() {
		defer func() { recover() }()
		a.refreshAcceleratorSnapshot(context.Background())
	}()
	return best
}

func (a *App) manualAcceleratorPrefix() string {
	var enabled bool
	var manual sql.NullString
	if err := a.DB.QueryRow(`select github_accelerator_enabled,github_accelerator_url from system_setups order by id desc limit 1`).Scan(&enabled, &manual); err != nil || !enabled {
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(manual.String), "/")
}

// markAcceleratorFailure invalidates the cached winner after a real download
// through it failed, so the next attempt picks the runner-up.
func markAcceleratorFailure(prefix string) {
	accelerators.mu.Lock()
	defer accelerators.mu.Unlock()
	if accelerators.snapshot.Best == prefix {
		accelerators.snapshot.Best = ""
		accelerators.probedAt = time.Time{}
	}
}

// handleGitHubAccelerators reports (and optionally re-probes) the accelerator
// pool for the panel, and accepts PUT to update the accelerator settings and
// the optional GitHub token.
func (a *App) handleGitHubAccelerators(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPut {
		var body struct {
			Mode             string   `json:"mode"`
			ExtraPrefixes    []string `json:"extra_prefixes"`
			ExtraPrefixesRaw string   `json:"extra_prefixes_raw"`
			ManualPrefix     *string  `json:"manual_prefix"`
			GitHubToken      string   `json:"github_token"`
			ResetToken       bool     `json:"reset_token"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 64*1024)).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "invalid request body: " + err.Error()})
			return
		}
		if strings.TrimSpace(body.Mode) != "" {
			switch strings.ToLower(strings.TrimSpace(body.Mode)) {
			case "auto", "manual", "off":
				a.setSetting(settingAcceleratorMode, strings.ToLower(strings.TrimSpace(body.Mode)))
			default:
				writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "mode must be auto, manual or off"})
				return
			}
		}
		if body.ExtraPrefixes != nil || strings.TrimSpace(body.ExtraPrefixesRaw) != "" {
			fields := body.ExtraPrefixes
			if fields == nil {
				fields = strings.Split(body.ExtraPrefixesRaw, ",")
			}
			cleaned := make([]string, 0, len(fields))
			seen := map[string]bool{}
			for _, field := range fields {
				field = strings.TrimRight(strings.TrimSpace(field), "/")
				if field == "" || !(strings.HasPrefix(field, "https://") || strings.HasPrefix(field, "http://")) || seen[field] {
					continue
				}
				seen[field] = true
				cleaned = append(cleaned, field)
			}
			if len(cleaned) > 16 {
				writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "too many extra prefixes (max 16)"})
				return
			}
			if err := a.setSettingChecked(settingAcceleratorExtraPrefixes, strings.Join(cleaned, ",")); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "error": "save extra prefixes: " + err.Error()})
				return
			}
		}
		if body.ManualPrefix != nil {
			manual := strings.TrimRight(strings.TrimSpace(*body.ManualPrefix), "/")
			if manual != "" && !(strings.HasPrefix(manual, "https://") || strings.HasPrefix(manual, "http://")) {
				writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "manual prefix must start with http:// or https://"})
				return
			}
			if _, err := a.DB.Exec(`update system_setups set github_accelerator_enabled=?, github_accelerator_url=? where id=(select id from system_setups order by id desc limit 1)`, manual != "", manual); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "error": "save manual prefix: " + err.Error()})
				return
			}
		}
		if body.ResetToken {
			a.setSetting(settingGitHubToken, "")
		} else if strings.TrimSpace(body.GitHubToken) != "" {
			token := strings.TrimSpace(body.GitHubToken)
			if len(token) < 16 || len(token) > 255 {
				writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "github token length looks invalid"})
				return
			}
			a.setSetting(settingGitHubToken, token)
		}
		// The candidate pool may have changed; force a fresh probe.
		accelerators.mu.Lock()
		accelerators.probedAt = time.Time{}
		accelerators.mu.Unlock()
	} else if r.Method == http.MethodPost {
		accelerators.mu.Lock()
		accelerators.probedAt = time.Time{}
		accelerators.mu.Unlock()
	}
	snapshot := a.refreshAcceleratorSnapshot(r.Context())
	extras := a.acceleratorExtraPrefixes()
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "data": map[string]any{
		"best_prefix":        snapshot.Best,
		"mode":               snapshot.Mode,
		"probed_at":          snapshot.ProbedAt,
		"results":            snapshot.Results,
		"manual_prefix":      a.manualAcceleratorPrefix(),
		"extra_prefixes":     extras,
		"github_token_masked": maskGitHubToken(a.githubToken()),
		"rate_limit":         lastGitHubRateLimit.snapshot(),
	}})
}

func (a *App) acceleratorExtraPrefixes() []string {
	raw := strings.TrimSpace(a.setting(settingAcceleratorExtraPrefixes, ""))
	if raw == "" {
		return []string{}
	}
	fields := strings.Split(raw, ",")
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if field = strings.TrimSpace(field); field != "" {
			out = append(out, field)
		}
	}
	return out
}

// setSettingChecked persists a settings KV with the DB error surfaced, for
// request handlers that must report failures instead of silently dropping.
func (a *App) setSettingChecked(key, value string) error {
	_, err := a.DB.Exec(`insert or replace into settings(key,value,updated_at) values(?,?,?)`, key, value, time.Now())
	return err
}

// githubToken returns the optional Personal Access Token used for
// api.github.com quota. Never send it through a public accelerator mirror.
func (a *App) githubToken() string {
	if a == nil || a.DB == nil {
		return ""
	}
	return strings.TrimSpace(a.setting(settingGitHubToken, ""))
}

func maskGitHubToken(token string) string {
	if token = strings.TrimSpace(token); token == "" {
		return ""
	}
	if len(token) <= 8 {
		return strings.Repeat("*", 4)
	}
	return token[:4] + strings.Repeat("*", 6) + token[len(token)-4:]
}

// githubRateLimitObservation remembers the X-RateLimit headers of the most
// recent api.github.com response so the panel can show how much anonymous
// quota the current egress IP has left.
type githubRateLimitObservation struct {
	mu         sync.Mutex
	limit      int64
	remaining  int64
	resetUnix  int64
	via        string
	observedAt time.Time
}

var lastGitHubRateLimit = &githubRateLimitObservation{}

func (o *githubRateLimitObservation) record(resp *http.Response, via string) {
	if resp == nil {
		return
	}
	limit, err1 := strconv.ParseInt(resp.Header.Get("X-RateLimit-Limit"), 10, 64)
	remaining, err2 := strconv.ParseInt(resp.Header.Get("X-RateLimit-Remaining"), 10, 64)
	reset, err3 := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return
	}
	o.mu.Lock()
	o.limit, o.remaining, o.resetUnix, o.via, o.observedAt = limit, remaining, reset, via, time.Now()
	o.mu.Unlock()
}

func (o *githubRateLimitObservation) snapshot() map[string]any {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.observedAt.IsZero() {
		return nil
	}
	return map[string]any{
		"limit":        o.limit,
		"remaining":    o.remaining,
		"reset_unix":   o.resetUnix,
		"via":          o.via,
		"observed_at":  o.observedAt,
	}
}

// nextAcceleratorPrefix returns the first live accelerator other than the
// failed one, from the freshest probe snapshot available.  It honours ctx so
// cancelled callers never block on a synchronous probe round.
func (a *App) nextAcceleratorPrefix(ctx context.Context, failed string) string {
	if ctx == nil || ctx.Err() != nil {
		return ""
	}
	snapshot := a.refreshAcceleratorSnapshot(ctx)
	for _, result := range snapshot.Results {
		if result.OK && result.Prefix != failed {
			return result.Prefix
		}
	}
	return ""
}
