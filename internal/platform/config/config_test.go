package config

import (
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Profile != ProfileLow {
		t.Fatalf("default profile = %q, want low", cfg.Profile)
	}
	if cfg.CookieSecure != CookieSecureAuto {
		t.Fatalf("default cookie-secure = %q, want auto", cfg.CookieSecure)
	}
	if cfg.PProf != "" {
		t.Fatalf("default pprof = %q, want empty", cfg.PProf)
	}
	b := cfg.Budget()
	if b.DBMaxConns != 2 || b.DBCacheKiB != 8192 || b.GOGC != 50 ||
		b.MemLimitBytes != 280<<20 || b.UploadConcurrency != 2 || b.CopyBufferBytes != 64<<10 {
		t.Fatalf("low budget = %+v", b)
	}
}

func TestLoadHighProfile(t *testing.T) {
	cfg, err := Load([]string{"-profile", "high"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b := cfg.Budget()
	if b.Profile != ProfileHigh || b.DBMaxConns != 8 || b.DBCacheKiB != 65536 ||
		b.GOGC != 100 || b.MemLimitBytes != 3<<30 || b.UploadConcurrency != 8 {
		t.Fatalf("high budget = %+v", b)
	}
}

func TestLoadProfileCaseInsensitive(t *testing.T) {
	cfg, err := Load([]string{"-profile", "HIGH"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Profile != ProfileHigh {
		t.Fatalf("profile = %q, want high", cfg.Profile)
	}
}

func TestLoadRejectsBadProfile(t *testing.T) {
	if _, err := Load([]string{"-profile", "medium"}); err == nil {
		t.Fatal("expected error for unknown profile")
	}
}

func TestLoadRejectsBadCookieSecure(t *testing.T) {
	if _, err := Load([]string{"-cookie-secure", "sometimes"}); err == nil {
		t.Fatal("expected error for unknown cookie-secure policy")
	}
}

func TestLoadPProfLoopbackRules(t *testing.T) {
	cases := []struct {
		addr string
		ok   bool
	}{
		{"", true},
		{"127.0.0.1:6060", true},
		{"localhost:6060", true},
		{"[::1]:6060", true},
		{"0.0.0.0:6060", false},
		{"203.0.113.10:6060", false},
		{"127.0.0.1", false},
		{"127.0.0.1:0", false},
	}
	for _, tc := range cases {
		_, err := Load([]string{"-pprof", tc.addr})
		if tc.ok && err != nil {
			t.Errorf("pprof %q rejected: %v", tc.addr, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("pprof %q accepted, want rejection", tc.addr)
		}
	}
}

func TestLoadEnvAndFlagPrecedence(t *testing.T) {
	t.Setenv("RAPIG_PROFILE", "high")
	t.Setenv("RAPIG_COOKIE_SECURE", "always")
	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Profile != ProfileHigh || cfg.CookieSecure != CookieSecureAlways {
		t.Fatalf("env not applied: %+v", cfg)
	}

	cfg, err = Load([]string{"-profile", "low", "-cookie-secure", "never"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Profile != ProfileLow || cfg.CookieSecure != CookieSecureNever {
		t.Fatalf("flags should win over env: %+v", cfg)
	}
}

func TestLoadQBDisabledByDefault(t *testing.T) {
	t.Setenv("RAPIG_QB_URL", "")
	t.Setenv("RAPIG_QB_USERNAME", "")
	t.Setenv("RAPIG_QB_PASSWORD", "")
	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.QBURL != "" || cfg.QBUsername != "" || cfg.QBPassword != "" {
		t.Fatalf("qb defaults = %+v, want all empty", cfg)
	}
}

func TestLoadQBEnvAndFlags(t *testing.T) {
	t.Setenv("RAPIG_QB_URL", " http://127.0.0.1:8080 ")
	t.Setenv("RAPIG_QB_USERNAME", "env-user")
	t.Setenv("RAPIG_QB_PASSWORD", "env-pass")
	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.QBURL != "http://127.0.0.1:8080" || cfg.QBUsername != "env-user" || cfg.QBPassword != "env-pass" {
		t.Fatalf("qb env = %+v", cfg)
	}

	cfg, err = Load([]string{
		"-qb-url", "http://127.0.0.1:9090",
		"-qb-username", "flag-user",
		"-qb-password", "flag-pass",
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.QBURL != "http://127.0.0.1:9090" || cfg.QBUsername != "flag-user" || cfg.QBPassword != "flag-pass" {
		t.Fatalf("qb flags = %+v", cfg)
	}
}

func TestSecretFlagHidesValue(t *testing.T) {
	value := "top-secret"
	if got := (secretFlag{target: &value}).String(); got != "" {
		t.Fatalf("secretFlag.String() = %q, want empty", got)
	}
	if err := (secretFlag{target: &value}).Set("next"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if value != "next" {
		t.Fatalf("target = %q", value)
	}
}

func TestLoadAllowOrigins(t *testing.T) {
	t.Setenv("RAPIG_ALLOW_ORIGINS", "http://127.0.0.1:5173")
	cfg, err := Load([]string{"-allow-origin", "https://example.test,https://other.test"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"http://127.0.0.1:5173", "https://example.test", "https://other.test"}
	if len(cfg.AllowOrigins) != len(want) {
		t.Fatalf("allow origins = %v, want %v", cfg.AllowOrigins, want)
	}
	for i := range want {
		if cfg.AllowOrigins[i] != want[i] {
			t.Fatalf("allow origins = %v, want %v", cfg.AllowOrigins, want)
		}
	}
}
