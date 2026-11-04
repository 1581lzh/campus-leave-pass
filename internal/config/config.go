// Package config loads and validates process configuration from the
// environment. Secrets are required: the service refuses to start without
// them so that tokens and registration codes are never derived from weak
// defaults.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
)

// Config is the immutable process configuration.
type Config struct {
	BaseURL            string
	ListenAddr         string
	DBPath             string
	SessionSecret      []byte
	RegistrationSecret []byte
	AdminInitialName   string
	AdminInitialPass   string
	AdminInitialPhone  string
	CampusBuildings    []string
	CookieSecure       bool
	Now                func() time.Time
}

// Location is the single display/date-boundary timezone used by the system.
var Location = mustLocation("Asia/Shanghai")

func mustLocation(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic("timezone " + name + " unavailable: " + err.Error())
	}
	return loc
}

// Load reads configuration from the environment.
func Load() (Config, error) {
	c := Config{
		BaseURL:            env("APP_BASE_URL", "http://127.0.0.1:8080"),
		ListenAddr:         env("APP_LISTEN", "127.0.0.1:8080"),
		DBPath:             env("DB_PATH", "leavepass.db"),
		SessionSecret:      []byte(os.Getenv("SESSION_SECRET")),
		RegistrationSecret: []byte(os.Getenv("REGISTRATION_CODE_SECRET")),
		AdminInitialName:   env("ADMIN_INITIAL_NAME", "admin"),
		AdminInitialPass:   env("ADMIN_INITIAL_PASSWORD", "AA"),
		AdminInitialPhone:  strings.TrimSpace(os.Getenv("ADMIN_INITIAL_PHONE")),
		CookieSecure:       os.Getenv("APP_INSECURE_COOKIES") != "1",
		Now:                time.Now,
	}
	c.BaseURL = strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	for _, b := range strings.Split(os.Getenv("CAMPUS_BUILDINGS"), ",") {
		if b = strings.TrimSpace(b); b != "" {
			c.CampusBuildings = append(c.CampusBuildings, b)
		}
	}
	var errs []string
	if len(c.SessionSecret) < 16 {
		errs = append(errs, "SESSION_SECRET 缺失或过短（至少 16 字节）")
	}
	if len(c.RegistrationSecret) < 16 {
		errs = append(errs, "REGISTRATION_CODE_SECRET 缺失或过短（至少 16 字节）")
	}
	if c.AdminInitialPass == "" || len([]rune(c.AdminInitialPass)) > 19 {
		errs = append(errs, "ADMIN_INITIAL_PASSWORD 必须为 1–19 个字符")
	}
	if v := os.Getenv("APP_INSECURE_COOKIES"); v != "" {
		if _, err := strconv.ParseBool(v); err != nil && v != "1" {
			errs = append(errs, "APP_INSECURE_COOKIES 只能为 1 或 0")
		}
	}
	if len(errs) > 0 {
		return c, errors.New(strings.Join(errs, "；"))
	}
	if !strings.HasPrefix(c.BaseURL, "http://") && !strings.HasPrefix(c.BaseURL, "https://") {
		return c, fmt.Errorf("APP_BASE_URL 必须以 http:// 或 https:// 开头")
	}
	return c, nil
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// Now returns the current time using the injected clock.
func (c Config) ClockNow() time.Time {
	if c.Now == nil {
		return time.Now()
	}
	return c.Now()
}
