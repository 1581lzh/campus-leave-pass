package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"leavepass/internal/app"
	"leavepass/internal/secure"
	"leavepass/internal/validate"
)

type loginData struct {
	baseData
	Mode     string
	Ident    string
	Error    string
	NextHint string
}

type registerData struct {
	baseData
	Code     string
	Username string
	Phone    string
	Error    string
}

type forgotData struct {
	baseData
	Kind        string
	Ident       string
	Phone       string
	Name        string
	MaskedPhone string
	Error       string
	Notice      string
}

type recoverStatusData struct {
	baseData
	Request app.ResetRequest
	Code    string
	Notice  string
	Error   string
}

type resetData struct {
	baseData
	Token string
	Error string
	Admin bool
}

type gateData struct {
	baseData
	Found bool
	Gate  app.GatePass
}

type proxyData struct {
	baseData
	Found    bool
	View     app.ProxyView
	Token    string
	Error    string
	Done     bool
	Decision string
	Approver string
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	if u, ok := s.authed(r); ok {
		s.redirect(w, r, roleHome(u))
		return
	}
	b := s.base(w, r, "landing", "校园请假通行系统")
	s.renderPage(w, r, "landing", struct{ baseData }{b})
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	if u, ok := s.authed(r); ok {
		s.redirect(w, r, roleHome(u))
		return
	}
	s.renderLogin(w, r, http.StatusOK, "student", "", "")
}

func (s *Server) staffLoginPage(w http.ResponseWriter, r *http.Request) {
	if u, ok := s.authed(r); ok {
		s.redirect(w, r, roleHome(u))
		return
	}
	s.renderLogin(w, r, http.StatusOK, "staff", "", "")
}

func (s *Server) adminLoginPage(w http.ResponseWriter, r *http.Request) {
	if u, ok := s.authed(r); ok {
		s.redirect(w, r, roleHome(u))
		return
	}
	s.renderLogin(w, r, http.StatusOK, "admin", "", "")
}

func (s *Server) renderLogin(w http.ResponseWriter, r *http.Request, status int, mode, ident, msg string) {
	b := s.base(w, r, "login", "登录")
	title := "学生登录"
	switch mode {
	case "staff":
		title = "教职工登录"
	case "admin":
		title = "管理员登录"
	}
	b.Title = title
	s.render(w, r, status, "login", loginData{baseData: b, Mode: mode, Ident: ident, Error: msg})
}

func (s *Server) loginPost(w http.ResponseWriter, r *http.Request) {
	ident := strings.TrimSpace(r.FormValue("ident"))
	password := r.FormValue("password")
	// Check the coarse IP budget before any bcrypt work. The per-account lock
	// below still provides the tighter 10-attempt limit.
	if locked, msg := s.loginLocked(r, "login_global_ip", clientIP(r)); locked {
		s.renderLogin(w, r, http.StatusTooManyRequests, "student", ident, msg)
		return
	}
	key := loginAttemptKeyStudent(r, ident)
	if locked, msg := s.loginLocked(r, "login_ip", key); locked {
		s.renderLogin(w, r, http.StatusTooManyRequests, "student", ident, msg)
		return
	}
	no, _, err := validate.NormalizeStudentNo(ident)
	var u app.User
	if err == nil {
		u, err = s.store.GetUserByStudentNo(r.Context(), no)
	}
	if err != nil {
		secure.CheckDummy(password)
		s.loginFail(w, r, "student", ident, "学号不存在，请检查后重试；学生账号由班主任或辅导员导入创建。", "login_ip", key, 10*time.Minute, 30*time.Minute, 10)
		return
	}
	if u.Status == "soft_deleted" && u.PurgeAfter <= s.store.NowUnix() {
		s.loginFail(w, r, "student", ident, "账号已停用；如需恢复，请联系管理员。", "login_ip", key, 10*time.Minute, 30*time.Minute, 10)
		return
	}
	if !secure.CheckPassword(u.PasswordHash, password) {
		s.loginFail(w, r, "student", ident, "密码错误，请重试；若忘记密码，可使用“忘记密码”。", "login_ip", key, 10*time.Minute, 30*time.Minute, 10)
		return
	}
	_ = s.store.ClearLoginFailures(r.Context(), "login_ip", key)
	if err := s.startSession(w, r, u); err != nil {
		s.error(w, r, http.StatusInternalServerError, "登录失败", "创建会话失败，请稍后重试。")
		return
	}
	s.redirect(w, r, roleHome(u))
}

func (s *Server) staffLoginPost(w http.ResponseWriter, r *http.Request) {
	ident := strings.TrimSpace(r.FormValue("ident"))
	password := r.FormValue("password")
	if locked, msg := s.loginLocked(r, "login_global_ip", clientIP(r)); locked {
		s.renderLogin(w, r, http.StatusTooManyRequests, "staff", ident, msg)
		return
	}
	key := loginAttemptKeyStaff(r, ident)
	if locked, msg := s.loginLocked(r, "login_ip", key); locked {
		s.renderLogin(w, r, http.StatusTooManyRequests, "staff", ident, msg)
		return
	}
	u, err := s.store.FindStaffLogin(r.Context(), ident)
	if err != nil {
		secure.CheckDummy(password)
		s.loginFail(w, r, "staff", ident, "账号不存在，请检查用户名或手机号；教职工需先使用注册码注册。", "login_ip", key, 10*time.Minute, 30*time.Minute, 10)
		return
	}
	if u.Status != "active" {
		s.loginFail(w, r, "staff", ident, "账号已停用；请使用“忘记密码”或联系管理员。", "login_ip", key, 10*time.Minute, 30*time.Minute, 10)
		return
	}
	if !secure.CheckPassword(u.PasswordHash, password) {
		s.loginFail(w, r, "staff", ident, "密码错误，请重试；若忘记密码，可使用“忘记密码”。", "login_ip", key, 10*time.Minute, 30*time.Minute, 10)
		return
	}
	_ = s.store.ClearLoginFailures(r.Context(), "login_ip", key)
	if err := s.startSession(w, r, u); err != nil {
		s.error(w, r, http.StatusInternalServerError, "登录失败", "创建会话失败，请稍后重试。")
		return
	}
	s.redirect(w, r, roleHome(u))
}

func (s *Server) adminLoginPost(w http.ResponseWriter, r *http.Request) {
	ident := strings.TrimSpace(r.FormValue("ident"))
	password := r.FormValue("password")
	adminKey := clientIP(r)
	if locked, msg := s.loginLocked(r, "admin_login_ip", adminKey); locked {
		s.renderLogin(w, r, http.StatusTooManyRequests, "admin", ident, msg)
		return
	}
	u, err := s.store.FindAdminLogin(r.Context(), ident)
	if err != nil {
		secure.CheckDummy(password)
		s.loginFail(w, r, "admin", ident, "管理员账号不存在，请检查用户名或手机号。", "admin_login_ip", adminKey, 10*time.Minute, 10*time.Minute, 4)
		return
	}
	if u.Status != "active" {
		s.loginFail(w, r, "admin", ident, "账号已停用，请联系另一位管理员。", "admin_login_ip", adminKey, 10*time.Minute, 10*time.Minute, 4)
		return
	}
	if !secure.CheckPassword(u.PasswordHash, password) {
		s.loginFail(w, r, "admin", ident, "密码错误，请重试；连续失败 4 次将锁定当前 IP 10 分钟。", "admin_login_ip", adminKey, 10*time.Minute, 10*time.Minute, 4)
		return
	}
	_ = s.store.ClearLoginFailures(r.Context(), "admin_login_ip", adminKey)
	if err := s.startSession(w, r, u); err != nil {
		s.error(w, r, http.StatusInternalServerError, "登录失败", "创建会话失败，请稍后重试。")
		return
	}
	s.redirect(w, r, "/admin")
}

func (s *Server) loginFail(w http.ResponseWriter, r *http.Request, mode, ident, msg, scope, key string, window, lock time.Duration, threshold int) {
	// Administrators already have a strict per-IP limit. For student and staff
	// logins, also cap failures across changing identifiers so an attacker
	// cannot force unbounded bcrypt work by rotating the account name.
	if scope != "admin_login_ip" {
		_ = s.store.RecordLoginFailure(r.Context(), "login_global_ip", clientIP(r), 10*time.Minute, 10*time.Minute, 100)
	}
	_ = s.store.RecordLoginFailure(r.Context(), scope, key, window, lock, threshold)
	if until, _ := s.store.LoginLocked(r.Context(), scope, key); until > s.store.NowUnix() {
		mins := (until - s.store.NowUnix() + 59) / 60
		msg = msg + " 尝试次数过多，请在 " + itoa(mins) + " 分钟后重试。"
	}
	status := http.StatusUnauthorized
	if strings.Contains(msg, "分钟后重试") {
		status = http.StatusTooManyRequests
	}
	s.renderLogin(w, r, status, mode, ident, msg)
}

func (s *Server) registerInfo(w http.ResponseWriter, r *http.Request) {
	b := s.base(w, r, "register", "注册说明")
	s.renderPage(w, r, "register", struct{ baseData }{b})
}

func (s *Server) staffRegisterPage(w http.ResponseWriter, r *http.Request) {
	if u, ok := s.authed(r); ok {
		s.redirect(w, r, roleHome(u))
		return
	}
	s.renderRegister(w, r, http.StatusOK, "", "", "", "")
}

func (s *Server) renderRegister(w http.ResponseWriter, r *http.Request, status int, code, username, phone, msg string) {
	b := s.base(w, r, "staff_register", "教职工注册")
	s.render(w, r, status, "staff_register", registerData{baseData: b, Code: code, Username: username, Phone: phone, Error: msg})
}

func (s *Server) staffRegisterPost(w http.ResponseWriter, r *http.Request) {
	in := app.StaffRegisterInput{
		Code:     r.FormValue("code"),
		Username: r.FormValue("username"),
		Password: r.FormValue("password"),
		Phone:    r.FormValue("phone"),
	}
	confirm := r.FormValue("password2")
	if in.Password != confirm {
		s.renderRegister(w, r, http.StatusUnprocessableEntity, in.Code, in.Username, in.Phone, "两次输入的密码不一致。")
		return
	}
	_, err := s.store.RegisterStaff(r.Context(), in, clientIP(r), r.UserAgent(), s.deviceHash(w, r))
	if err != nil {
		s.renderRegister(w, r, http.StatusUnprocessableEntity, in.Code, in.Username, in.Phone, err.Error())
		return
	}
	s.redirectFlash(w, r, "/staff/login", "ok", "注册成功，请使用新账号登录。")
}

func (s *Server) forgotPage(w http.ResponseWriter, r *http.Request) {
	b := s.base(w, r, "forgot", "忘记密码")
	s.render(w, r, http.StatusOK, "forgot", forgotData{baseData: b})
}

func (s *Server) forgotPost(w http.ResponseWriter, r *http.Request) {
	kind := r.FormValue("kind")
	ident := strings.TrimSpace(r.FormValue("ident"))
	phone := strings.TrimSpace(r.FormValue("phone"))
	name := strings.TrimSpace(r.FormValue("name"))
	errorStatus := http.StatusUnprocessableEntity
	if kind == "student" {
		_, code, browser, err := s.store.CreateStudentResetRequest(r.Context(), ident, name, clientIP(r))
		if err != nil {
			s.renderForgot(w, r, errorStatus, kind, ident, phone, name, err.Error())
			return
		}
		s.setCookie(w, cookieRecover, browser, 86400, true)
		s.redirectFlash(w, r, "/recover/status", "ok",
			"申请已提交，申请码为 "+code+"（24 小时内有效，仅需告知本班班主任/辅导员）。")
		return
	}
	if kind != "staff" {
		s.renderForgot(w, r, http.StatusBadRequest, kind, ident, phone, name, "请选择学生或教职工。")
		return
	}
	u, err := s.store.FindStaffForReset(r.Context(), ident, false)
	if err != nil {
		_ = s.store.RecordLoginFailure(r.Context(), "reset_ip", clientIP(r), time.Hour, 30*time.Minute, 20)
		s.renderForgot(w, r, errorStatus, kind, ident, phone, name, "账号不存在；请检查用户名，或改用“辅助重置”。")
		return
	}
	if u.Status == "soft_deleted" && u.PurgeAfter <= s.store.NowUnix() {
		s.renderForgot(w, r, errorStatus, kind, ident, phone, name, "账号已停用，无法自助重置。")
		return
	}
	if err := s.store.VerifyStaffPhone(r.Context(), u, phone, clientIP(r)); err != nil {
		s.renderForgot(w, r, errorStatus, kind, ident, phone, name, err.Error())
		return
	}
	token, err := s.store.CreatePasswordResetToken(r.Context(), u.ID, 0, clientIP(r))
	if err != nil {
		s.error(w, r, http.StatusInternalServerError, "重置失败", "生成重置链接失败，请稍后重试。")
		return
	}
	s.setCookie(w, cookieReset, token, 600, true)
	s.redirect(w, r, "/reset-password")
}

func (s *Server) renderForgot(w http.ResponseWriter, r *http.Request, status int, kind, ident, phone, name, msg string) {
	b := s.base(w, r, "forgot", "忘记密码")
	s.render(w, r, status, "forgot", forgotData{baseData: b, Kind: kind, Ident: ident, Phone: phone, Name: name, Error: msg})
}

func (s *Server) adminForgotPage(w http.ResponseWriter, r *http.Request) {
	b := s.base(w, r, "admin_forgot", "管理员忘记密码")
	s.render(w, r, http.StatusOK, "admin_forgot", forgotData{baseData: b})
}

func (s *Server) adminForgotPost(w http.ResponseWriter, r *http.Request) {
	ident := strings.TrimSpace(r.FormValue("ident"))
	phone := strings.TrimSpace(r.FormValue("phone"))
	if locked, msg := s.loginLocked(r, "reset_ip", clientIP(r)); locked {
		s.renderAdminForgot(w, r, http.StatusTooManyRequests, ident, phone, msg)
		return
	}
	u, err := s.store.FindStaffForReset(r.Context(), ident, true)
	if err != nil {
		_ = s.store.RecordLoginFailure(r.Context(), "reset_ip", clientIP(r), time.Hour, 30*time.Minute, 20)
		s.renderAdminForgot(w, r, http.StatusUnprocessableEntity, ident, phone, "管理员账号不存在。")
		return
	}
	if u.Status == "soft_deleted" && u.PurgeAfter <= s.store.NowUnix() {
		s.renderAdminForgot(w, r, http.StatusUnprocessableEntity, ident, phone, "账号已停用。")
		return
	}
	if err := s.store.VerifyStaffPhone(r.Context(), u, phone, clientIP(r)); err != nil {
		s.renderAdminForgot(w, r, http.StatusUnprocessableEntity, ident, phone, err.Error())
		return
	}
	token, err := s.store.CreatePasswordResetToken(r.Context(), u.ID, 0, clientIP(r))
	if err != nil {
		s.error(w, r, http.StatusInternalServerError, "重置失败", "生成重置链接失败，请稍后重试。")
		return
	}
	s.setCookie(w, cookieReset, token, 600, true)
	s.redirect(w, r, "/reset-password")
}

func (s *Server) renderAdminForgot(w http.ResponseWriter, r *http.Request, status int, ident, phone, msg string) {
	b := s.base(w, r, "admin_forgot", "管理员忘记密码")
	s.render(w, r, status, "admin_forgot", forgotData{baseData: b, Ident: ident, Phone: phone, Error: msg})
}

func (s *Server) resetPage(w http.ResponseWriter, r *http.Request) {
	token := ""
	if c, err := r.Cookie(cookieReset); err == nil {
		token = c.Value
	}
	if token == "" {
		token = strings.TrimSpace(r.URL.Query().Get("token"))
		if token != "" {
			s.setCookie(w, cookieReset, token, 600, true)
		}
	}
	if token == "" {
		s.renderReset(w, r, http.StatusBadRequest, "", "重置链接缺失或已过期，请重新发起忘记密码。", false)
		return
	}
	u, _, err := s.store.ResetTokenUser(r.Context(), token)
	if err != nil {
		s.clearCookie(w, cookieReset)
		s.renderReset(w, r, http.StatusGone, "", "重置链接已过期或已使用，请重新发起忘记密码。", false)
		return
	}
	s.renderReset(w, r, http.StatusOK, token, "", u.Role == "admin")
}

func (s *Server) renderReset(w http.ResponseWriter, r *http.Request, status int, token, msg string, admin bool) {
	b := s.base(w, r, "reset", "设置新密码")
	s.render(w, r, status, "reset", resetData{baseData: b, Token: token, Error: msg, Admin: admin})
}

func (s *Server) resetPost(w http.ResponseWriter, r *http.Request) {
	token := r.FormValue("token")
	if c, err := r.Cookie(cookieReset); err == nil && c.Value != "" {
		token = c.Value
	}
	password := r.FormValue("password")
	if password != r.FormValue("password2") {
		s.renderReset(w, r, http.StatusUnprocessableEntity, token, "两次输入的密码不一致。", false)
		return
	}
	u, err := s.store.ResetPasswordWithToken(r.Context(), token, password, clientIP(r))
	if err != nil {
		s.renderReset(w, r, http.StatusUnprocessableEntity, token, err.Error(), false)
		return
	}
	s.clearCookie(w, cookieReset)
	s.clearCookie(w, cookieRecover)
	s.clearCookie(w, cookieSession)
	login := "/login"
	switch u.Role {
	case "admin":
		login = "/admin/login"
	case "teacher", "counselor", "community":
		login = "/staff/login"
	}
	s.redirectFlash(w, r, login, "ok", "密码已重置，请用新密码登录。")
}

func (s *Server) recoverPage(w http.ResponseWriter, r *http.Request) {
	b := s.base(w, r, "recover", "辅助重置密码")
	s.renderPage(w, r, "recover", forgotData{baseData: b})
}

func (s *Server) renderRecover(w http.ResponseWriter, r *http.Request, status int, kind, ident, phone, name, msg string) {
	b := s.base(w, r, "recover", "辅助重置密码")
	s.render(w, r, status, "recover", forgotData{baseData: b, Kind: kind, Ident: ident, Phone: phone, Name: name, Error: msg})
}

func (s *Server) recoverPost(w http.ResponseWriter, r *http.Request) {
	kind := r.FormValue("kind")
	ident := strings.TrimSpace(r.FormValue("ident"))
	phone := strings.TrimSpace(r.FormValue("phone"))
	name := strings.TrimSpace(r.FormValue("name"))
	var (
		req     app.ResetRequest
		code    string
		browser string
		err     error
	)
	switch kind {
	case "student":
		req, code, browser, err = s.store.CreateStudentResetRequest(r.Context(), ident, name, clientIP(r))
	case "staff":
		req, code, browser, err = s.store.CreateStaffResetRequest(r.Context(), ident, phone, clientIP(r))
	default:
		s.renderRecover(w, r, http.StatusBadRequest, kind, ident, phone, name, "请选择学生或教职工。")
		return
	}
	_ = req
	if err != nil {
		s.renderRecover(w, r, http.StatusUnprocessableEntity, kind, ident, phone, name, err.Error())
		return
	}
	s.setCookie(w, cookieRecover, browser, 86400, true)
	s.redirectFlash(w, r, "/recover/status", "ok",
		"申请已提交，申请码为 "+code+"（24 小时内有效）。请将申请码线下告知可审批的老师。")
}

func (s *Server) studentRecoverPage(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "student")
	if !ok {
		return
	}
	b := s.base(w, r, "student_recover", "申请重置密码")
	s.render(w, r, http.StatusOK, "student_recover", struct {
		baseData
		Student app.StudentRow
		Error   string
	}{baseData: b, Student: app.StudentRow{Name: u.Username, StudentNo: u.StudentNo}})
}

func (s *Server) studentRecoverPost(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "student")
	if !ok {
		return
	}
	if u.Status == "soft_deleted" && u.PurgeAfter <= s.store.NowUnix() {
		s.error(w, r, http.StatusForbidden, "无法申请重置", "账号已停用超过 7 天，无法重置密码。")
		return
	}
	req, code, browser, err := s.store.CreateStudentResetRequest(r.Context(), u.StudentNo, u.Username, clientIP(r))
	_ = req
	if err != nil {
		s.error(w, r, http.StatusUnprocessableEntity, "申请未提交", err.Error())
		return
	}
	s.setCookie(w, cookieRecover, browser, 86400, true)
	s.redirectFlash(w, r, "/recover/status", "ok",
		"申请已提交，申请码为 "+code+"（24 小时内有效）。请将申请码线下告知本班班主任或辅导员。")
}

func (s *Server) recoverStatus(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(cookieRecover)
	if err != nil || c.Value == "" {
		s.renderRecoverStatus(w, r, http.StatusBadRequest, app.ResetRequest{}, "", "当前浏览器没有待查询的重置申请；请重新发起。", "")
		return
	}
	q, err := s.store.GetResetRequestByBrowser(r.Context(), c.Value)
	if err != nil {
		s.renderRecoverStatus(w, r, http.StatusNotFound, app.ResetRequest{}, "", "找不到该申请，可能已过期；请重新发起。", "")
		return
	}
	switch q.Status {
	case "approved":
		if q.UsedAt != 0 {
			s.renderRecoverStatus(w, r, http.StatusOK, q, "", "重置链接已发放或已使用；如仍需重置，请重新发起申请。", "")
			return
		}
		raw, err := s.store.IssueResetTokenForBrowser(r.Context(), c.Value, clientIP(r))
		if err != nil {
			s.renderRecoverStatus(w, r, http.StatusOK, q, "", "申请已批准，但链接发放失败："+err.Error(), "")
			return
		}
		s.setCookie(w, cookieReset, raw, 600, true)
		s.redirect(w, r, "/reset-password")
	case "pending":
		s.renderRecoverStatus(w, r, http.StatusOK, q, "", "申请待批准：请将申请码告诉本班班主任/辅导员或同角色同事。", "")
	case "rejected":
		s.renderRecoverStatus(w, r, http.StatusOK, q, "", "申请已被拒绝；如确有需要，请重新发起或联系管理员。", "")
	case "expired":
		s.renderRecoverStatus(w, r, http.StatusOK, q, "", "申请已过期（24 小时有效）；请重新发起。", "")
	case "used":
		s.renderRecoverStatus(w, r, http.StatusOK, q, "", "申请已完成，重置链接已使用。", "")
	default:
		s.renderRecoverStatus(w, r, http.StatusOK, q, "", "申请状态："+q.Status, "")
	}
}

func (s *Server) renderRecoverStatus(w http.ResponseWriter, r *http.Request, status int, q app.ResetRequest, code, notice, msg string) {
	b := s.base(w, r, "recover_status", "重置申请状态")
	s.render(w, r, status, "recover_status", recoverStatusData{baseData: b, Request: q, Code: code, Notice: notice, Error: msg})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.clearSession(w, r)
	s.redirectFlash(w, r, "/login", "ok", "已退出登录。")
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := s.store.DB.PingContext(r.Context()); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "database unavailable"})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func (s *Server) gatePage(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	g, err := s.store.GetGatePass(r.Context(), token)
	if err != nil {
		b := s.base(w, r, "gate", "通行凭证无效")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		s.render(w, r, http.StatusNotFound, "gate", gateData{baseData: b})
		return
	}
	b := s.base(w, r, "gate", "请假通行凭证")
	s.render(w, r, http.StatusOK, "gate", gateData{baseData: b, Found: true, Gate: g})
}

func (s *Server) proxyPage(w http.ResponseWriter, r *http.Request) {
	if locked, msg := s.loginLocked(r, "proxy_ip", clientIP(r)); locked {
		b := s.base(w, r, "proxy", "代理审批")
		s.render(w, r, http.StatusTooManyRequests, "proxy", proxyData{baseData: b, Token: r.PathValue("token"), Error: msg})
		return
	}
	v, err := s.store.GetProxyView(r.Context(), r.PathValue("token"))
	if err != nil {
		_ = s.store.RecordLoginFailure(r.Context(), "proxy_ip", clientIP(r), 10*time.Minute, 30*time.Minute, 20)
		b := s.base(w, r, "proxy", "代理审批链接无效")
		s.render(w, r, http.StatusNotFound, "proxy", proxyData{baseData: b, Token: r.PathValue("token"),
			Error: "链接无效、已过期、已撤销，或对应假条已处理；请联系班主任重新生成。"})
		return
	}
	b := s.base(w, r, "proxy", "代理审批")
	s.render(w, r, http.StatusOK, "proxy", proxyData{baseData: b, Found: true, View: v, Token: r.PathValue("token")})
}

func (s *Server) proxyPost(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	in := app.DecideInput{
		Decision:     r.FormValue("decision"),
		ApproverName: strings.TrimSpace(r.FormValue("approver_name")),
		MajorOpinion: r.FormValue("major_opinion"),
		DeptOpinion:  r.FormValue("dept_opinion"),
		RejectReason: r.FormValue("reject_reason"),
		IP:           clientIP(r),
	}
	if err := s.store.ProxyDecide(r.Context(), token, in); err != nil {
		if errors.Is(err, app.ErrNotFound) {
			b := s.base(w, r, "proxy", "代理审批链接无效")
			s.render(w, r, http.StatusNotFound, "proxy", proxyData{baseData: b, Token: token, Error: "链接已失效；请联系班主任重新生成。"})
			return
		}
		b := s.base(w, r, "proxy", "代理审批")
		s.render(w, r, http.StatusUnprocessableEntity, "proxy", proxyData{baseData: b, Token: token, Error: err.Error()})
		return
	}
	_ = s.store.ClearLoginFailures(r.Context(), "proxy_ip", clientIP(r))
	b := s.base(w, r, "proxy", "代理审批完成")
	s.render(w, r, http.StatusOK, "proxy", proxyData{baseData: b, Done: true, Decision: in.Decision, Approver: in.ApproverName})
}
