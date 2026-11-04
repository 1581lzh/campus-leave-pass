// Package web implements the server-rendered HTTP layer.
package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"leavepass/internal/app"
	"leavepass/internal/config"
)

//go:embed templates/*.html templates/partials/*.html static/*
var assets embed.FS

// Server holds the store and compiled templates. Each page is compiled with
// the shared layout so that every page can define its own "content" block.
type Server struct {
	store *app.Store
	pages map[string]*template.Template
}

// New builds the web server.
func New(store *app.Store) (*Server, error) {
	// Content-hashed asset URLs let nginx/Cloudflare cache assets for a week
	// without ever serving stale JavaScript or CSS after a deploy.
	assetVersions := make(map[string]string, 4)
	for _, name := range []string{"app.css", "app.js", "motion.js", "layout.js"} {
		data, err := assets.ReadFile("static/" + name)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(data)
		assetVersions[name] = hex.EncodeToString(sum[:6])
	}

	funcs := template.FuncMap{
		"asset": func(p string) string {
			if v, ok := assetVersions[strings.TrimPrefix(p, "/static/")]; ok {
				return p + "?v=" + v
			}
			return p
		},
		"fmtDateTime":  fmtDateTime,
		"fmtDate":      func(ts int64) string { return time.Unix(ts, 0).In(config.Location).Format("2006-01-02") },
		"fmtTime":      func(ts int64) string { return time.Unix(ts, 0).In(config.Location).Format("15:04") },
		"fmtMonthDay":  func(ts int64) string { return time.Unix(ts, 0).In(config.Location).Format("01-02 15:04") },
		"fmtInput":     fmtInput,
		"roleName":     roleName,
		"statusName":   statusName,
		"decisionName": decisionName,
		"codeStatus":   func(c app.RegistrationCode, now int64) string { return c.Status(now) },
		"daysLeft": func(due int64) int {
			left := due - time.Now().Unix()
			if left <= 0 {
				return 0
			}
			return int((left + 86399) / 86400)
		},
		"pct": func(a, b int) int {
			if b == 0 {
				return 0
			}
			return a * 100 / b
		},
		"maskPhone":  maskPhone,
		"dormString": func(b, r string) string { return b + "-" + r },
		"add":        func(a, b int) int { return a + b },
		"notZero":    func(v int64) bool { return v != 0 },
		"eqStr":      func(a, b string) bool { return a == b },
		"nowUnix":    func() int64 { return time.Now().Unix() },
		// Native datetime-local pickers use `max`; keep the widget aligned with
		// the server-side registration-code lifetime limit.
		"codeMaxValidUntil": func() string {
			return time.Now().Add(90 * 24 * time.Hour).In(config.Location).Format("2006-01-02T15:04")
		},
		"decisionText":         decisionText,
		"hasPrefix":            strings.HasPrefix,
		"queryPage":            queryPage,
		"auditActionLabel":     auditActionLabel,
		"auditTargetTypeLabel": auditTargetTypeLabel,
		"contains": func(list []string, want string) bool {
			for _, v := range list {
				if v == want {
					return true
				}
			}
			return false
		},
	}
	files, err := fs.Glob(assets, "templates/*.html")
	if err != nil {
		return nil, err
	}
	pages := make(map[string]*template.Template)
	for _, file := range files {
		base := path.Base(file)
		if base == "layout.html" {
			continue
		}
		tpl, err := template.New("layout").Funcs(funcs).ParseFS(assets,
			"templates/layout.html", "templates/partials/*.html", file)
		if err != nil {
			return nil, err
		}
		pages[strings.TrimSuffix(base, ".html")] = tpl
	}
	return &Server{store: store, pages: pages}, nil
}

// Handler returns the fully wrapped HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.routes(mux)
	static, _ := fs.Sub(assets, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(static))))
	var h http.Handler = mux
	// Session must run before CSRF: the CSRF token is derived from the session
	// token hash once a user is authenticated.
	h = s.withCSRF(h)
	h = s.withSession(h)
	h = s.withSecurity(h)
	h = s.recoverPanic(h)
	h = s.withLogging(h)
	return h
}

// baseData is embedded in every page model.
type baseData struct {
	Page      string
	Title     string
	Path      string
	User      app.User
	Authed    bool
	CSRF      string
	Unread    int
	FlashKind string
	FlashMsg  string
	Buildings []string
}

func (s *Server) base(w http.ResponseWriter, r *http.Request, page, title string) baseData {
	b := baseData{Page: page, Title: title, Path: r.URL.Path}
	if sc, ok := sessionFrom(r.Context()); ok {
		b.CSRF = sc.CSRF
		if sc.User.ID != 0 {
			b.User = sc.User
			b.Authed = true
			b.Unread = s.store.UnreadNotificationCount(r.Context(), sc.User.ID)
		}
	}
	if kind, msg := s.takeFlash(w, r); msg != "" {
		b.FlashKind, b.FlashMsg = kind, msg
	}
	return b
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, page string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	tpl := s.pages[page]
	if tpl == nil {
		slog.Error("render: unknown page", "page", page)
		return
	}
	if err := tpl.ExecuteTemplate(w, "layout", data); err != nil {
		slog.Error("render", "page", page, "err", err)
	}
}

func (s *Server) renderPage(w http.ResponseWriter, r *http.Request, page string, data any) {
	s.render(w, r, http.StatusOK, page, data)
}

func (s *Server) error(w http.ResponseWriter, r *http.Request, status int, title, reason string) {
	b := s.base(w, r, "error", title)
	s.render(w, r, status, "error", struct {
		baseData
		Status int
		Reason string
	}{b, status, reason})
}

func (s *Server) redirect(w http.ResponseWriter, r *http.Request, path string) {
	http.Redirect(w, r, path, http.StatusSeeOther)
}

func (s *Server) redirectFlash(w http.ResponseWriter, r *http.Request, path, kind, msg string) {
	s.setFlash(w, kind, msg)
	s.redirect(w, r, path)
}

func queryPage(raw string, page int) string {
	q, _ := url.ParseQuery(raw)
	q.Set("page", strconv.Itoa(page))
	return q.Encode()
}

func fmtDateTime(ts int64) string {
	if ts <= 0 {
		return "—"
	}
	return time.Unix(ts, 0).In(config.Location).Format("2006-01-02 15:04")
}

// fmtInput renders a datetime-local value in CST.
func fmtInput(ts int64) string {
	if ts <= 0 {
		return ""
	}
	return time.Unix(ts, 0).In(config.Location).Format("2006-01-02T15:04")
}

func roleName(role string) string {
	switch role {
	case "student":
		return "学生"
	case "teacher":
		return "班主任"
	case "counselor":
		return "辅导员"
	case "community":
		return "社区老师"
	case "admin":
		return "管理员"
	}
	return role
}

func statusName(s string) string {
	switch s {
	case "pending":
		return "待审批"
	case "approved":
		return "已批准"
	case "rejected":
		return "已驳回"
	case "cancelled":
		return "已取消"
	}
	return s
}

func decisionName(d string) string {
	switch d {
	case "approved":
		return "通过"
	case "rejected":
		return "驳回"
	case "timeout":
		return "超时自动驳回"
	case "cancelled_by_student":
		return "学生取消"
	case "cancelled_by_admin":
		return "管理员取消"
	case "class_disbanded":
		return "班级解散"
	case "class_cleaned":
		return "班级清理"
	}
	return d
}

func decisionText(l app.Leave) string {
	if l.Decision == "" {
		return "—"
	}
	return decisionName(l.Decision)
}

func maskPhone(p string) string {
	if len(p) != 11 {
		return "***********"
	}
	return p[:3] + "******" + p[9:]
}

// parseID reads a path id.
func parseID(r *http.Request, name string) (int64, error) {
	return strconv.ParseInt(r.PathValue(name), 10, 64)
}

// clientIP trusts only nginx's X-Real-IP, falling back to the socket address.
func clientIP(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("X-Real-IP")); v != "" {
		if net.ParseIP(v) != nil {
			return v
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// sameOrigin validates a browser Origin header when present.
//
// "null" is sent by browsers for opaque origins such as sandboxed iframes,
// some in-app browsers and isolated preview containers. The CSRF token check
// has already run before this function; accepting null here keeps those
// containers usable without weakening the token binding.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" || strings.EqualFold(origin, "null") {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// abs builds an absolute URL on the configured base URL.
func (s *Server) abs(path string) string { return s.store.BaseURL() + path }
