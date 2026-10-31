package web

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"leavepass/internal/app"
	"leavepass/internal/config"
	"leavepass/internal/secure"
	"leavepass/internal/validate"
)

// roleHome returns the landing page for an authenticated account.
func roleHome(u app.User) string {
	switch u.Role {
	case "student":
		if u.Status == "soft_deleted" {
			return "/s/leaves"
		}
		return "/s"
	case "teacher", "counselor":
		return "/t"
	case "community":
		return "/c"
	case "admin":
		return "/admin"
	}
	return "/"
}

func (s *Server) authed(r *http.Request) (app.User, bool) {
	sc, ok := sessionFrom(r.Context())
	if !ok || sc.User.ID == 0 {
		return app.User{}, false
	}
	return sc.User, true
}

// postFail renders a mutating request failure with the reason and next step.
func (s *Server) postFail(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusUnprocessableEntity
	switch {
	case errors.Is(err, app.ErrForbidden):
		status = http.StatusForbidden
	case errors.Is(err, app.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, app.ErrVersionConflict), errors.Is(err, app.ErrDormConflict):
		status = http.StatusConflict
	case errors.Is(err, app.ErrTakeoverNeeded), errors.Is(err, app.ErrClassSwitchNeeded):
		status = http.StatusConflict
	}
	msg := err.Error()
	if msg == "" {
		msg = "操作未完成，请稍后重试。"
	}
	s.error(w, r, status, "操作未完成", msg+"；请返回上一页检查后重试。")
}

// parseCSTLocal parses a datetime-local value as Asia/Shanghai time.
func parseCSTLocal(v string) (int64, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, errors.New("请填写日期和时间")
	}
	for _, layout := range []string{"2006-01-02T15:04", "2006-01-02T15:04:05"} {
		if t, err := time.ParseInLocation(layout, v, config.Location); err == nil {
			return t.Unix(), nil
		}
	}
	return 0, errors.New("日期时间格式不正确")
}

// parseDate parses an optional YYYY-MM-DD date.
func parseDate(v string) (int64, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, nil
	}
	t, err := time.ParseInLocation("2006-01-02", v, config.Location)
	if err != nil {
		return 0, errors.New("日期格式应为 YYYY-MM-DD")
	}
	return t.Unix(), nil
}

func pageParam(r *http.Request) int {
	n, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if n < 1 {
		return 1
	}
	return n
}

// leaveFilterFromQuery reads the shared admin/community/archive filter set.
func leaveFilterFromQuery(r *http.Request) app.LeaveFilter {
	q := r.URL.Query()
	f := app.LeaveFilter{
		CreatedFrom:   strings.TrimSpace(q.Get("created_from")),
		CreatedTo:     strings.TrimSpace(q.Get("created_to")),
		StartFrom:     strings.TrimSpace(q.Get("start_from")),
		StartTo:       strings.TrimSpace(q.Get("start_to")),
		Building:      strings.TrimSpace(q.Get("building")),
		ClassName:     strings.TrimSpace(q.Get("class")),
		TeacherName:   strings.TrimSpace(q.Get("teacher")),
		CounselorName: strings.TrimSpace(q.Get("counselor")),
		Status:        strings.TrimSpace(q.Get("status")),
		Keyword:       strings.TrimSpace(q.Get("q")),
		CleanedFrom:   strings.TrimSpace(q.Get("cleaned_from")),
		CleanedTo:     strings.TrimSpace(q.Get("cleaned_to")),
		Page:          pageParam(r),
		PageSize:      50,
	}
	switch f.Status {
	case "pending", "approved", "rejected", "cancelled":
	default:
		f.Status = ""
	}
	return f
}

// filterQuery rebuilds the current filter as a query string for pagination.
func filterQuery(r *http.Request, page int) string {
	q := r.URL.Query()
	q.Set("page", strconv.Itoa(page))
	return q.Encode()
}

// deviceHash returns the soft per-device registration identifier.
func (s *Server) deviceHash(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(cookieDevice); err == nil && len(c.Value) >= 16 {
		return secure.SHA256Hex(c.Value)
	}
	raw, err := secure.RandomToken(24)
	if err != nil {
		return ""
	}
	s.setCookie(w, cookieDevice, raw, 365*86400, true)
	return secure.SHA256Hex(raw)
}

// startSession issues a session cookie and records the login.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u app.User) error {
	raw, err := s.store.CreateSession(r.Context(), u.ID, clientIP(r), r.UserAgent())
	if err != nil {
		return err
	}
	_ = s.store.TouchLastLogin(r.Context(), u.ID)
	s.setSessionCookie(w, raw)
	return nil
}

// setSessionCookie expires at the next CST midnight, matching the server-side
// session lifetime.
func (s *Server) setSessionCookie(w http.ResponseWriter, raw string) {
	now := s.store.Now()
	next := app.NextMidnightCST(now)
	maxAge := int(next.Sub(now).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieSession, Value: raw, Path: "/",
		Expires: next, MaxAge: maxAge, HttpOnly: true,
		Secure: s.store.CookieSecure(), SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearSession(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieSession); err == nil {
		_ = s.store.DeleteSession(r.Context(), c.Value)
	}
	s.clearCookie(w, cookieSession)
}

// loginAttemptKeyStudent derives the per-IP, per-student lock key from the
// normalized student number so full-width or case variants share one counter.
func loginAttemptKeyStudent(r *http.Request, ident string) string {
	key := strings.ToLower(strings.TrimSpace(ident))
	if _, norm, err := validate.NormalizeStudentNo(ident); err == nil {
		key = "s:" + norm
	}
	return clientIP(r) + ":" + secure.SHA256Hex(key)[:16]
}

// loginAttemptKeyStaff derives the per-IP, per-staff lock key using the same
// normalization as FindStaffLogin (phone or NFKC username).
func loginAttemptKeyStaff(r *http.Request, ident string) string {
	var key string
	if phone, err := validate.NormalizePhone(ident); err == nil {
		key = "p:" + phone
	} else {
		key = "u:" + strings.ToLower(validate.NFKC(ident))
	}
	return clientIP(r) + ":" + secure.SHA256Hex(key)[:16]
}

func (s *Server) loginLocked(r *http.Request, scope, key string) (bool, string) {
	until, _ := s.store.LoginLocked(r.Context(), scope, key)
	if until <= s.store.NowUnix() {
		return false, ""
	}
	mins := (until - s.store.NowUnix() + 59) / 60
	return true, fmt.Sprintf("尝试次数过多，请在 %d 分钟后重试；若忘记密码，可使用“忘记密码”。", mins)
}

// checkStudentClass validates student class membership fields.
func (s *Server) studentClass(r *http.Request, u app.User) (app.StudentRow, app.Class, bool) {
	st, err := s.store.GetStudent(r.Context(), u.ID)
	if err != nil {
		return st, app.Class{}, false
	}
	if st.ClassID == 0 {
		return st, app.Class{}, true
	}
	c, err := s.store.GetClass(r.Context(), st.ClassID)
	if err != nil {
		return st, app.Class{}, true
	}
	return st, c, true
}

func normalizeIdent(raw string) string {
	return strings.ToLower(validate.NFKC(raw))
}

// itoa is a small dependency-free integer formatter for handlers and CSRF
// messages.
func itoa(v int64) string { return strconv.FormatInt(v, 10) }
