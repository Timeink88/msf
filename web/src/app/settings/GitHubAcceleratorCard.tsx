"use client";

import { useCallback, useEffect, useState } from "react";
import { Loader2, Plus, RefreshCw, ShieldCheck, Trash2, TriangleAlert, Zap } from "lucide-react";
import { api, apiData } from "@/lib/api";
import { cn } from "@/lib/utils";

type AcceleratorMode = "auto" | "manual" | "off";

interface AcceleratorProbeResult {
  prefix: string;
  source: string;
  ok: boolean;
  latency_ms: number;
  status?: number;
  error?: string;
}

interface AcceleratorSnapshot {
  best_prefix: string;
  mode: AcceleratorMode;
  probed_at: string;
  results: AcceleratorProbeResult[];
  manual_prefix: string;
  extra_prefixes: string[];
  github_token_masked: string;
  rate_limit?: {
    limit: number;
    remaining: number;
    reset_unix: number;
    via: string;
    observed_at: string;
  } | null;
}

const inputClass =
  "gary-field w-full px-3 py-2 text-sm text-foreground placeholder:text-muted-foreground";

function sourceLabel(source: string) {
  switch (source) {
    case "builtin":
      return "内置";
    case "extra":
      return "自建";
    case "manual":
      return "手动";
    default:
      return source || "未知";
  }
}

function viaLabel(via: string) {
  switch (via) {
    case "mirror":
      return "镜像线路";
    case "token":
      return "Token 认证";
    case "proxy":
      return "代理/直连";
    default:
      return via || "";
  }
}

const modeOptions: Array<{ value: AcceleratorMode; label: string; hint: string }> = [
  { value: "auto", label: "自动探测", hint: "内置公共镜像 + 自建镜像一起测速，自动选最快可用线路；失败自动换线，最后回退代理/直连" },
  { value: "manual", label: "手动指定", hint: "只使用下方手动填写的加速地址，不经测速；失败仍会自动回退代理/直连" },
  { value: "off", label: "关闭", hint: "不使用任何加速镜像，GitHub 请求只走代理或直连" },
];

export function GitHubAcceleratorCard({
  onManualPrefixSaved,
}: {
  onManualPrefixSaved?: (prefix: string) => void;
}) {
  const [snapshot, setSnapshot] = useState<AcceleratorSnapshot | null>(null);
  const [loadError, setLoadError] = useState("");
  const [busy, setBusy] = useState(false);
  const [extraDraft, setExtraDraft] = useState("");
  const [manualDraft, setManualDraft] = useState("");
  const [tokenDraft, setTokenDraft] = useState("");

  const load = useCallback(async (probe = false) => {
    setBusy(true);
    try {
      const payload = await api(
        probe ? "/api/v1/github/accelerators/probe" : "/api/v1/github/accelerators",
        probe ? { method: "POST" } : undefined
      );
      if (payload?.success) {
        const data = apiData<AcceleratorSnapshot>(payload);
        setSnapshot(data);
        setManualDraft(data.manual_prefix || "");
        setLoadError("");
      } else {
        setLoadError(payload?.error || "加载加速镜像状态失败");
      }
    } catch (error) {
      setLoadError(error instanceof Error ? error.message : String(error));
    } finally {
      setBusy(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const save = async (body: Record<string, unknown>) => {
    setBusy(true);
    try {
      const payload = await api("/api/v1/github/accelerators", {
        method: "PUT",
        body: JSON.stringify(body),
      });
      if (payload?.success) {
        const data = apiData<AcceleratorSnapshot>(payload);
        setSnapshot(data);
        setManualDraft(data.manual_prefix || "");
        setLoadError("");
        return true;
      }
      setLoadError(payload?.error || "保存失败");
      return false;
    } catch (error) {
      setLoadError(error instanceof Error ? error.message : String(error));
      return false;
    } finally {
      setBusy(false);
    }
  };

  const mode = snapshot?.mode || "auto";
  const rateLimit = snapshot?.rate_limit || null;

  return (
    <div className="rounded-lg border border-border/60 bg-card/50 p-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h4 className="text-base font-semibold text-foreground">GitHub 加速镜像</h4>
          <p className="mt-1 text-xs text-muted-foreground">
            组件下载与版本更新默认优先走加速镜像，失败自动换线并回退代理/直连
          </p>
        </div>
        <button
          type="button"
          disabled={busy}
          onClick={() => void load(true)}
          className="inline-flex h-9 items-center gap-1.5 rounded-md border border-border bg-background px-3 text-xs font-medium text-muted-foreground transition hover:text-foreground disabled:cursor-not-allowed disabled:opacity-50"
        >
          {busy ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <RefreshCw className="h-3.5 w-3.5" />}
          立即重新探测
        </button>
      </div>

      {loadError ? (
        <p className="mt-3 flex items-start gap-1.5 text-xs text-destructive">
          <TriangleAlert className="mt-0.5 h-3.5 w-3.5 shrink-0" />
          {loadError}
        </p>
      ) : null}

      <div className="mt-4 space-y-3">
        <label className="block space-y-1.5">
          <span className="block text-xs font-medium text-foreground">加速模式</span>
          <select
            value={mode}
            disabled={busy}
            onChange={(event) => void save({ mode: event.target.value })}
            className={`${inputClass} h-11`}
          >
            {modeOptions.map((option) => (
              <option key={option.value} value={option.value}>
                {option.label}
              </option>
            ))}
          </select>
          <span className="block text-xs text-muted-foreground">
            {modeOptions.find((option) => option.value === mode)?.hint}
          </span>
        </label>

        {mode === "manual" ? (
          <div className="space-y-2 rounded-md border border-border/60 bg-background/60 p-3">
            <label className="block space-y-1.5">
              <span className="block text-xs font-medium text-foreground">加速地址（手动指定）</span>
              <div className="flex gap-2">
                <input
                  value={manualDraft}
                  onChange={(event) => setManualDraft(event.target.value)}
                  placeholder="例如: https://gh-proxy.com"
                  className={`${inputClass} h-10`}
                />
                <button
                  type="button"
                  disabled={busy}
                  onClick={async () => {
                    const saved = await save({ manual_prefix: manualDraft.trim() });
                    if (saved) onManualPrefixSaved?.(manualDraft.trim());
                  }}
                  className="h-10 shrink-0 rounded-md bg-primary px-3 text-xs font-medium text-primary-foreground disabled:cursor-not-allowed disabled:opacity-50"
                >
                  保存
                </button>
              </div>
              <span className="block text-xs text-muted-foreground">留空保存即关闭手动地址</span>
            </label>
          </div>
        ) : null}

        {mode === "auto" ? (
          <div className="space-y-2 rounded-md border border-border/60 bg-background/60 p-3">
            <span className="block text-xs font-medium text-foreground">附加加速地址（参与自动探测）</span>
            <div className="flex gap-2">
              <input
                value={extraDraft}
                onChange={(event) => setExtraDraft(event.target.value)}
                placeholder="https://your-mirror.example"
                className={`${inputClass} h-10`}
              />
              <button
                type="button"
                disabled={busy || !extraDraft.trim()}
                onClick={async () => {
                  const next = [...(snapshot?.extra_prefixes || []), extraDraft.trim()];
                  if (await save({ extra_prefixes: next })) setExtraDraft("");
                }}
                className="inline-flex h-10 shrink-0 items-center gap-1 rounded-md border border-border bg-background px-3 text-xs font-medium text-muted-foreground transition hover:text-foreground disabled:cursor-not-allowed disabled:opacity-50"
              >
                <Plus className="h-3.5 w-3.5" />
                添加
              </button>
            </div>
            {(snapshot?.extra_prefixes?.length || 0) > 0 ? (
              <div className="flex flex-wrap gap-2 pt-1">
                {snapshot?.extra_prefixes.map((prefix) => (
                  <span
                    key={prefix}
                    className="inline-flex items-center gap-1.5 rounded-md border border-border bg-card px-2 py-1 text-xs text-foreground"
                  >
                    {prefix}
                    <button
                      type="button"
                      disabled={busy}
                      onClick={() =>
                        void save({
                          extra_prefixes: (snapshot?.extra_prefixes || []).filter((item) => item !== prefix),
                        })
                      }
                      className="text-muted-foreground transition hover:text-destructive"
                      aria-label={`删除 ${prefix}`}
                    >
                      <Trash2 className="h-3 w-3" />
                    </button>
                  </span>
                ))}
              </div>
            ) : null}
          </div>
        ) : null}

        <div className="rounded-md border border-border/60 bg-background/60 p-3">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <span className="text-xs font-medium text-foreground">镜像状态</span>
            {snapshot?.probed_at ? (
              <span className="text-[11px] text-muted-foreground">
                探测于 {new Date(snapshot.probed_at).toLocaleTimeString()}
              </span>
            ) : null}
          </div>
          {busy && !snapshot ? (
            <div className="flex items-center gap-2 py-3 text-xs text-muted-foreground">
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
              正在探测镜像可用性…
            </div>
          ) : (snapshot?.results?.length || 0) === 0 ? (
            <p className="py-3 text-xs text-muted-foreground">
              {mode === "off" ? "加速已关闭，没有候选镜像" : "暂无候选镜像，请添加或重新探测"}
            </p>
          ) : (
            <div className="mt-2 space-y-1.5">
              {snapshot?.results.map((result) => {
                const isBest = Boolean(snapshot.best_prefix) && result.prefix === snapshot.best_prefix;
                return (
                  <div
                    key={result.prefix}
                    className={cn(
                      "flex flex-wrap items-center gap-x-2 gap-y-1 rounded-md border px-2.5 py-1.5 text-xs",
                      isBest
                        ? "border-primary/60 bg-primary/10"
                        : result.ok
                          ? "border-border bg-card"
                          : "border-border/50 bg-card/40 opacity-70"
                    )}
                  >
                    <span className="font-mono text-[11px] text-foreground">{result.prefix}</span>
                    <span className="rounded border border-border px-1.5 py-0.5 text-[10px] text-muted-foreground">
                      {sourceLabel(result.source)}
                    </span>
                    {isBest ? (
                      <span className="inline-flex items-center gap-1 rounded bg-primary/15 px-1.5 py-0.5 text-[10px] font-medium text-primary">
                        <Zap className="h-3 w-3" />
                        当前生效
                      </span>
                    ) : null}
                    <span className={cn("ml-auto", result.ok ? "text-emerald-600" : "text-destructive")}>
                      {result.ok ? `可用 ${result.latency_ms}ms` : result.error ? "不可用" : "不可用"}
                    </span>
                  </div>
                );
              })}
            </div>
          )}
          {rateLimit ? (
            <p className="mt-2 text-[11px] text-muted-foreground">
              最近一次 GitHub API 配额（{viaLabel(rateLimit.via)}）：剩余 {rateLimit.remaining}/{rateLimit.limit}
              {rateLimit.reset_unix > 0
                ? `，${new Date(rateLimit.reset_unix * 1000).toLocaleTimeString()} 重置`
                : ""}
            </p>
          ) : null}
        </div>

        <div className="rounded-md border border-border/60 bg-background/60 p-3">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <span className="text-xs font-medium text-foreground">GitHub Token（可选）</span>
            {snapshot?.github_token_masked ? (
              <span className="inline-flex items-center gap-1 text-[11px] text-emerald-600">
                <ShieldCheck className="h-3.5 w-3.5" />
                已配置 {snapshot.github_token_masked}
              </span>
            ) : (
              <span className="text-[11px] text-muted-foreground">未配置，匿名限流 60 次/小时/IP</span>
            )}
          </div>
          <div className="mt-2 flex gap-2">
            <input
              type="password"
              value={tokenDraft}
              onChange={(event) => setTokenDraft(event.target.value)}
              placeholder="ghp_ 开头的 Personal Access Token"
              autoComplete="off"
              className={`${inputClass} h-10`}
            />
            <button
              type="button"
              disabled={busy || tokenDraft.trim().length === 0}
              onClick={async () => {
                if (await save({ github_token: tokenDraft.trim() })) setTokenDraft("");
              }}
              className="h-10 shrink-0 rounded-md bg-primary px-3 text-xs font-medium text-primary-foreground disabled:cursor-not-allowed disabled:opacity-50"
            >
              保存
            </button>
            {snapshot?.github_token_masked ? (
              <button
                type="button"
                disabled={busy}
                onClick={() => void save({ reset_token: true })}
                className="h-10 shrink-0 rounded-md border border-border px-3 text-xs font-medium text-muted-foreground transition hover:text-destructive disabled:cursor-not-allowed disabled:opacity-50"
              >
                清除
              </button>
            ) : null}
          </div>
          <p className="mt-2 text-[11px] leading-relaxed text-muted-foreground">
            配置后 API 限额提升至 5000 次/小时，且认证请求不再经过公共镜像（防止 Token 泄漏）。获取步骤：GitHub
            网页 → 右上角头像 → Settings → Developer settings → Personal access tokens → Tokens (classic) →
            Generate new token，权限全部不勾（公开仓库只读即可），复制生成的一次性令牌粘贴到此处。
          </p>
        </div>
      </div>
    </div>
  );
}
