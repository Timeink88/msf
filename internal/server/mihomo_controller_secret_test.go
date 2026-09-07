package server

import "testing"

// 回归锚定：活动配置中用户自带的 secret 必须优先于数据库里的生成值。
// injectMihomoControllerSecret 对自带 secret 的配置不注入生成值；若
// mihomoSecret 仍先读生成值，MSF 自身的控制器调用与 zashboard 预设都会
// 用错凭据，控制器整体失联（2026-09 PR review 第 4 项）。
func TestMihomoSecretPrefersActiveConfigUserSecret(t *testing.T) {
	app := newTestApp(t)
	app.ensureMihomoControllerSecret()
	generated := app.mihomoControllerSecret()
	if generated == "" {
		t.Fatal("generated controller secret missing")
	}

	// 基线：配置携带的就是生成值（注入路径的正常结果）。
	injected := "external-controller: 127.0.0.1:9090\nsecret: " + generated + "\n"
	if err := app.writeTextFileDirect(mihomoActiveConfigRelPath, injected); err != nil {
		t.Fatal(err)
	}
	if got := app.mihomoSecret(); got != generated {
		t.Fatalf("mihomoSecret = %q, want injected %q", got, generated)
	}

	// 用户自定义 secret：活动配置优先，绝不回退生成值。
	custom := "user-defined-controller-secret"
	userCfg := "external-controller: 127.0.0.1:9090\nsecret: " + custom + "\n"
	if err := app.writeTextFileDirect(mihomoActiveConfigRelPath, userCfg); err != nil {
		t.Fatal(err)
	}
	if got := app.mihomoSecret(); got != custom {
		t.Fatalf("mihomoSecret = %q, want user secret %q (controller would lock out)", got, custom)
	}

	// 旧部署/被清空的配置：回退生成值，行为与加固前一致。
	noSecret := "external-controller: 127.0.0.1:9090\nmixed-port: 7890\n"
	if err := app.writeTextFileDirect(mihomoActiveConfigRelPath, noSecret); err != nil {
		t.Fatal(err)
	}
	if got := app.mihomoSecret(); got != generated {
		t.Fatalf("mihomoSecret = %q, want generated fallback %q", got, generated)
	}
}
