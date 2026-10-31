package web

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"leavepass/internal/app"
	"leavepass/internal/secure"
)

const (
	cookieSession = "lp_sess"
	cookieCSRF    = "lp_csrf"
	cookieReset   = "lp_reset"
	cookieRecover = "lp_recover"
	cookieDevice  = "lp_device"
	cookieFlash   = "lp_flash"
	sessionCtxKey = ctxKey(1)
)

type ctxKey int

type sessionCtx struct {
	User      app.User
	TokenHash string
	CSRF      string
}

func sessionFrom(ctx context.Context) (sessionCtx, bool) {
	sc, ok := ctx.Value(sessionCtxKey).(sessionCtx)
	return sc, ok
}

func (s *Server) setCookie(w http.ResponseWriter, name, value string, maxAge int, httpOnly bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: httpOnly,
		Secure:   s.store.CookieSecure(),
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.store.CookieSecure(), SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) withSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sc := sessionCtx{}
		if c, err := r.Cookie(cookieSession); err == nil {
			if su, err := s.store.LookupSession(r.Context(), c.Value); err == nil {
				sc.User = su.User
				sc.TokenHash = su.Session.TokenHash
				sc.CSRF = s.store.CSRFFor(su.Session.TokenHash)
			} else {
				s.clearCookie(w, cookieSession)
			}
		}
		ctx := context.WithValue(r.Context(), sessionCtxKey, sc)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) withCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sc, _ := sessionFrom(r.Context())
		token := sc.CSRF
		if token == "" {
			if c, err := r.Cookie(cookieCSRF); err == nil && len(c.Value) >= 16 {
				token = c.Value
			} else {
				raw, err := secure.RandomToken(24)
				if err == nil {
					token = raw
					s.setCookie(w, cookieCSRF, raw, 86400, true)
				}
			}
		}
		sc.CSRF = token
		ctx := context.WithValue(r.Context(), sessionCtxKey, sc)
		r = r.WithContext(ctx)

		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodDelete {
			if !strings.HasPrefix(r.URL.Path, "/static/") {
				if r.Body != nil {
					r.Body = http.MaxBytesReader(w, r.Body, 6<<20)
				}
				if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
					_ = r.ParseMultipartForm(6 << 20)
				} else {
					_ = r.ParseForm()
				}
				submitted := r.FormValue("csrf")
				if token == "" || !secure.Equal(token, submitted) || !sameOrigin(r) {
					s.error(w, r, http.StatusForbidden, "请求被拒绝",
						"表单已过期或来源校验失败。请刷新页面后重试；若仍失败，请重新登录。")
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) withSecurity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
		h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		if strings.HasPrefix(r.URL.Path, "/p/") || strings.HasPrefix(r.URL.Path, "/a/") {
			h.Set("X-Robots-Tag", "noindex, nofollow")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic", "path", sanitizePath(r.URL.Path), "panic", rec)
				s.error(w, r, http.StatusInternalServerError, "服务器错误", "处理请求时发生错误，请稍后重试。")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		if strings.HasPrefix(r.URL.Path, "/p/") || strings.HasPrefix(r.URL.Path, "/a/") {
			return // never log capability URLs
		}
		slog.Info("http", "method", r.Method, "path", sanitizePath(r.URL.Path), "status", sw.status,
			"ip", clientIP(r), "ms", time.Since(start).Milliseconds())
	})
}

func sanitizePath(p string) string {
	if strings.HasPrefix(p, "/p/") {
		return "/p/[redacted]"
	}
	if strings.HasPrefix(p, "/a/") {
		return "/a/[redacted]"
	}
	return p
}

// require resolves an authenticated user and enforces role membership.
func (s *Server) require(w http.ResponseWriter, r *http.Request, roles ...string) (app.User, bool) {
	sc, ok := sessionFrom(r.Context())
	if !ok || sc.User.ID == 0 {
		s.redirect(w, r, loginForPath(r.URL.Path))
		return app.User{}, false
	}
	u := sc.User
	if len(roles) > 0 {
		allowed := false
		for _, role := range roles {
			if u.Role == role {
				allowed = true
				break
			}
		}
		if !allowed {
			s.error(w, r, http.StatusForbidden, "无权访问",
				"无权访问：该页面仅限"+roleList(roles)+"使用；你当前的角色是"+roleName(u.Role)+"。")
			return u, false
		}
	}
	if u.Status == "soft_deleted" && u.Role != "student" {
		s.redirectFlash(w, r, "/", "warn", "账号已停用，仅可查看历史。")
		return u, false
	}
	return u, true
}

func (s *Server) studentReadOnly(w http.ResponseWriter, r *http.Request, u app.User) bool {
	if u.Status != "soft_deleted" {
		return false
	}
	if r.Method == http.MethodGet && (r.URL.Path == "/s/leaves" || isLeaveDetailPath(r.URL.Path)) {
		return false
	}
	s.redirectFlash(w, r, "/s/leaves", "warn", "账号已停用，仅可查看历史；不能新建/取消请假或查看二维码。")
	return true
}

func isLeaveDetailPath(p string) bool {
	rest := strings.TrimPrefix(p, "/s/leaves/")
	if rest == p || rest == "" {
		return false
	}
	_, err := strconv.ParseInt(rest, 10, 64)
	return err == nil
}

func loginForPath(p string) string {
	switch {
	case strings.HasPrefix(p, "/s"):
		return "/login"
	case strings.HasPrefix(p, "/admin"):
		return "/admin/login"
	case strings.HasPrefix(p, "/archived"):
		return "/staff/login"
	case strings.HasPrefix(p, "/t"), strings.HasPrefix(p, "/c"):
		return "/staff/login"
	default:
		return "/login"
	}
}

func roleList(roles []string) string {
	var out []string
	for _, role := range roles {
		out = append(out, roleName(role))
	}
	return strings.Join(out, "或")
}

func (s *Server) setFlash(w http.ResponseWriter, kind, msg string) {
	v := url.QueryEscape(kind + "|" + msg)
	http.SetCookie(w, &http.Cookie{
		Name: cookieFlash, Value: v, Path: "/", MaxAge: 60, HttpOnly: true,
		Secure: s.store.CookieSecure(), SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) takeFlash(w http.ResponseWriter, r *http.Request) (string, string) {
	c, err := r.Cookie(cookieFlash)
	if err != nil || c.Value == "" {
		return "", ""
	}
	http.SetCookie(w, &http.Cookie{Name: cookieFlash, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.store.CookieSecure(), SameSite: http.SameSiteLaxMode})
	v, err := url.QueryUnescape(c.Value)
	if err != nil {
		return "", ""
	}
	parts := strings.SplitN(v, "|", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}
