export type GlassQuality = "full" | "balanced" | "reduced";

export type GlassQualityProfile = {
  speed: number;
  detail: "low" | "medium";
  pixels: number;
  proxyPixels: number;
  dpr: number;
};

export const GLASS_QUALITY_PROFILES: Readonly<Record<GlassQuality, GlassQualityProfile>> = Object.freeze({
  full: Object.freeze({ speed: 0.2, detail: "medium", pixels: 2_300_000, proxyPixels: 1_200_000, dpr: 1.5 }),
  balanced: Object.freeze({ speed: 0.16, detail: "low", pixels: 1_200_000, proxyPixels: 900_000, dpr: 1 }),
  reduced: Object.freeze({ speed: 0.12, detail: "low", pixels: 650_000, proxyPixels: 500_000, dpr: 0.75 }),
});

export function normalizeGlassQuality(value: unknown, fallback: GlassQuality = "balanced"): GlassQuality {
  return value === "full" || value === "balanced" || value === "reduced" ? value : fallback;
}
