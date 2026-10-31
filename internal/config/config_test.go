package config

import (
	"strings"
	"testing"
)

var configEnvKeys = []string{
	"APP_BASE_URL", "APP_LISTEN", "DB_PATH", "SESSION_SECRET",
	"REGISTRATION_CODE_SECRET", "ADMIN_INITIAL_NAME", "ADMIN_INITIAL_PASSWORD",
	"ADMIN_INITIAL_PHONE", "CAMPUS_BUILDINGS", "APP_INSECURE_COOKIES",
}

func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, k := range configEnvKeys {
		t.Setenv(k, "")
	}
}

func TestLoadValidConfig(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("APP_BASE_URL", "https://leave.example.com/")
	t.Setenv("SESSION_SECRET", strings.Repeat("s", 16))
	t.Setenv("REGISTRATION_CODE_SECRET", strings.Repeat("r", 24))
	t.Setenv("ADMIN_INITIAL_PASSWORD", "AA")
	t.Setenv("ADMIN_INITIAL_PHONE", "13800000000")
	t.Setenv("CAMPUS_BUILDINGS", " 21 , ,22, 23 ")
	t.Setenv("APP_INSECURE_COOKIES", "1")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.BaseURL != "https://leave.example.com" {
		t.Fatalf("BaseURL = %q", c.BaseURL)
	}
	if len(c.SessionSecret) != 16 || len(c.RegistrationSecret) != 24 {
		t.Fatalf("secret 长度异常: %d / %d", len(c.SessionSecret), len(c.RegistrationSecret))
	}
	if c.CookieSecure {
		t.Fatal("APP_INSECURE_COOKIES=1 时 CookieSecure 应为 false")
	}
	want := []string{"21", "22", "23"}
	if len(c.CampusBuildings) != len(want) {
		t.Fatalf("CampusBuildings = %#v", c.CampusBuildings)
	}
	for i := range want {
		if c.CampusBuildings[i] != want[i] {
			t.Fatalf("CampusBuildings[%d] = %q，期望 %q", i, c.CampusBuildings[i], want[i])
		}
	}
	if c.ClockNow().IsZero() {
		t.Fatal("ClockNow 不应为零值")
	}
}

func TestLoadRejectsMissingOrWeakConfig(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*testing.T)
		wantSub string
	}{
		{
			name:    "缺少两个 secret",
			setup:   func(t *testing.T) { t.Setenv("ADMIN_INITIAL_PASSWORD", "AA") },
			wantSub: "SESSION_SECRET",
		},
		{
			name: "session secret 过短",
			setup: func(t *testing.T) {
				t.Setenv("SESSION_SECRET", strings.Repeat("s", 15))
				t.Setenv("REGISTRATION_CODE_SECRET", strings.Repeat("r", 16))
				t.Setenv("ADMIN_INITIAL_PASSWORD", "AA")
			},
			wantSub: "SESSION_SECRET",
		},
		{
			name: "registration secret 过短",
			setup: func(t *testing.T) {
				t.Setenv("SESSION_SECRET", strings.Repeat("s", 16))
				t.Setenv("REGISTRATION_CODE_SECRET", strings.Repeat("r", 15))
				t.Setenv("ADMIN_INITIAL_PASSWORD", "AA")
			},
			wantSub: "REGISTRATION_CODE_SECRET",
		},
		{
			name: "非法 BaseURL",
			setup: func(t *testing.T) {
				t.Setenv("SESSION_SECRET", strings.Repeat("s", 16))
				t.Setenv("REGISTRATION_CODE_SECRET", strings.Repeat("r", 16))
				t.Setenv("ADMIN_INITIAL_PASSWORD", "AA")
				t.Setenv("APP_BASE_URL", "ftp://example.com")
			},
			wantSub: "APP_BASE_URL",
		},
		{
			name: "管理员密码过长",
			setup: func(t *testing.T) {
				t.Setenv("SESSION_SECRET", strings.Repeat("s", 16))
				t.Setenv("REGISTRATION_CODE_SECRET", strings.Repeat("r", 16))
				t.Setenv("ADMIN_INITIAL_PASSWORD", strings.Repeat("A", 20))
			},
			wantSub: "ADMIN_INITIAL_PASSWORD",
		},
		{
			name: "非法 APP_INSECURE_COOKIES",
			setup: func(t *testing.T) {
				t.Setenv("SESSION_SECRET", strings.Repeat("s", 16))
				t.Setenv("REGISTRATION_CODE_SECRET", strings.Repeat("r", 16))
				t.Setenv("ADMIN_INITIAL_PASSWORD", "AA")
				t.Setenv("APP_INSECURE_COOKIES", "yes")
			},
			wantSub: "APP_INSECURE_COOKIES",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearConfigEnv(t)
			tc.setup(t)
			_, err := Load()
			if err == nil {
				t.Fatal("期望 Load 返回错误")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("错误 %q 未包含 %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestLoadCookieSecureDefault(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("SESSION_SECRET", strings.Repeat("s", 16))
	t.Setenv("REGISTRATION_CODE_SECRET", strings.Repeat("r", 16))
	t.Setenv("ADMIN_INITIAL_PASSWORD", "AA")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !c.CookieSecure {
		t.Fatal("默认 CookieSecure 应为 true")
	}

	t.Setenv("APP_INSECURE_COOKIES", "0")
	c, err = Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !c.CookieSecure {
		t.Fatal("APP_INSECURE_COOKIES=0 时 CookieSecure 应为 true")
	}
}
