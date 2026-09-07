import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

describe("GitHub download routing UI", () => {
  const settings = readFileSync(new URL("./SettingsClient.tsx", import.meta.url), "utf8");
  const tokenCard = readFileSync(new URL("./GitHubAcceleratorCard.tsx", import.meta.url), "utf8");
  const setup = readFileSync(new URL("../../pages/SetupPage.tsx", import.meta.url), "utf8");

  it("offers only operator-supplied proxy and accelerator values", () => {
    for (const source of [settings, tokenCard, setup]) {
      expect(source).not.toContain("gh-proxy.com");
      expect(source).not.toContain("ghfast.top");
      expect(source).not.toContain("/api/v1/github/accelerators/probe");
    }
    expect(settings).toContain("系统不预置、不探测，也不会自动切换镜像");
    expect(setup).toContain("留空不会启用任何加速源");
  });
});
