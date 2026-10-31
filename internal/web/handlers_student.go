package web

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/skip2/go-qrcode"

	"leavepass/internal/app"
)

type studentDashData struct {
	baseData
	Student      app.StudentRow
	Class        app.Class
	HasClass     bool
	CanSubmit    bool
	SubmitReason string
	Pending      int
	Approved     int
	Leaves       []app.Leave
	Notices      []app.Notification
}

type leaveFormData struct {
	baseData
	Student      app.StudentRow
	Class        app.Class
	CanSubmit    bool
	SubmitReason string
	ContactPhone string
	Reason       string
	Start        string
	End          string
	Error        string
}

type studentLeavesData struct {
	baseData
	Leaves  []app.Leave
	Student app.StudentRow
}

type studentLeaveDetailData struct {
	baseData
	Leave   app.Leave
	Edits   []leaveEdit
	IsOwner bool
	CanQR   bool
}

type leaveEdit struct {
	EditorName string
	OldStart   int64
	OldEnd     int64
	NewStart   int64
	NewEnd     int64
	CreatedAt  int64
}

type profileData struct {
	baseData
	Profile        app.User
	Student        app.StudentRow
	IsStudent      bool
	Action         string
	RevokeAction   string
	SessionCount   int
	Error          string
	Notice         string
	StudentMissing bool
}

type notificationsData struct {
	baseData
	Notices []app.Notification
	Action  string
}

func (s *Server) studentDashboard(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "student")
	if !ok {
		return
	}
	if u.Status == "soft_deleted" {
		s.redirectFlash(w, r, "/s/leaves", "warn", "账号已停用，仅可查看历史；不能新建/取消请假或查看二维码。")
		return
	}
	st, c, has := s.studentClass(r, u)
	leaves, _ := s.store.StudentLeaves(r.Context(), u.ID)
	pending, approved := 0, 0
	for _, l := range leaves {
		switch l.Status {
		case "pending":
			pending++
		case "approved":
			approved++
		}
	}
	notices, _ := s.store.ListNotifications(r.Context(), u.ID, 5)
	canSubmit := st.ClassID != 0 && (c.TeacherUserID != 0 || c.CounselorUserID != 0)
	reason := ""
	switch {
	case st.ClassID == 0:
		reason = "你当前没有班级；请联系管理员转入班级后再提交请假。"
	case c.TeacherUserID == 0 && c.CounselorUserID == 0:
		reason = "本班暂无班主任和辅导员，没有审批人；请联系管理员指派后再提交。"
	}
	b := s.base(w, r, "student_dashboard", "学生首页")
	s.renderPage(w, r, "student_dashboard", studentDashData{
		baseData: b, Student: st, Class: c, HasClass: has && st.ClassID != 0,
		CanSubmit: canSubmit, SubmitReason: reason, Pending: pending, Approved: approved,
		Leaves: leaves, Notices: notices,
	})
}

func (s *Server) studentLeaveNew(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "student")
	if !ok {
		return
	}
	if s.studentReadOnly(w, r, u) {
		return
	}
	st, c, _ := s.studentClass(r, u)
	canSubmit := st.ClassID != 0 && (c.TeacherUserID != 0 || c.CounselorUserID != 0)
	reason := ""
	switch {
	case st.ClassID == 0:
		reason = "无法提交请假：你当前没有班级，请联系管理员转入班级。"
	case c.TeacherUserID == 0 && c.CounselorUserID == 0:
		reason = "无法提交请假：本班暂无班主任和辅导员，没有审批人；请联系管理员指派。"
	}
	if r.Method == http.MethodGet {
		now := s.store.Now()
		s.renderLeaveForm(w, r, http.StatusOK, leaveFormData{
			baseData: s.base(w, r, "student_leave_new", "新建请假"),
			Student:  st, Class: c, CanSubmit: canSubmit, SubmitReason: reason,
			Start: fmtInput(now.Add(time.Hour).Unix()), End: fmtInput(now.Add(4 * time.Hour).Unix()),
		})
		return
	}
}

func (s *Server) renderLeaveForm(w http.ResponseWriter, r *http.Request, status int, d leaveFormData) {
	s.render(w, r, status, "student_leave_new", d)
}

func (s *Server) studentLeaveSubmit(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "student")
	if !ok {
		return
	}
	if s.studentReadOnly(w, r, u) {
		return
	}
	st, c, _ := s.studentClass(r, u)
	canSubmit := st.ClassID != 0 && (c.TeacherUserID != 0 || c.CounselorUserID != 0)
	reason := ""
	switch {
	case st.ClassID == 0:
		reason = "无法提交请假：你当前没有班级，请联系管理员转入班级。"
	case c.TeacherUserID == 0 && c.CounselorUserID == 0:
		reason = "无法提交请假：本班暂无班主任和辅导员，没有审批人；请联系管理员指派。"
	}
	d := leaveFormData{
		baseData: s.base(w, r, "student_leave_new", "新建请假"),
		Student:  st, Class: c, CanSubmit: canSubmit, SubmitReason: reason,
		ContactPhone: strings.TrimSpace(r.FormValue("contact_phone")),
		Reason:       r.FormValue("reason"),
		Start:        r.FormValue("start_at"),
		End:          r.FormValue("end_at"),
	}
	if !canSubmit {
		d.Error = reason
		s.renderLeaveForm(w, r, http.StatusForbidden, d)
		return
	}
	start, err := parseCSTLocal(d.Start)
	if err != nil {
		d.Error = err.Error()
		s.renderLeaveForm(w, r, http.StatusUnprocessableEntity, d)
		return
	}
	end, err := parseCSTLocal(d.End)
	if err != nil {
		d.Error = err.Error()
		s.renderLeaveForm(w, r, http.StatusUnprocessableEntity, d)
		return
	}
	id, err := s.store.SubmitLeave(r.Context(), u, app.SubmitLeaveInput{
		ContactPhone: d.ContactPhone, Reason: d.Reason, StartAt: start, EndAt: end,
	})
	if err != nil {
		d.Error = err.Error()
		s.renderLeaveForm(w, r, http.StatusUnprocessableEntity, d)
		return
	}
	s.redirectFlash(w, r, "/s/leaves/"+itoa(id), "ok", "请假申请已提交，等待班主任或辅导员审批。")
}

func (s *Server) studentLeaves(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "student")
	if !ok {
		return
	}
	leaves, _ := s.store.StudentLeaves(r.Context(), u.ID)
	st, _ := s.store.GetStudent(r.Context(), u.ID)
	b := s.base(w, r, "student_leaves", "我的请假")
	s.renderPage(w, r, "student_leaves", studentLeavesData{baseData: b, Leaves: leaves, Student: st})
}

func (s *Server) studentLeaveDetail(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "student")
	if !ok {
		return
	}
	id, err := parseID(r, "id")
	if err != nil {
		s.error(w, r, http.StatusNotFound, "假条不存在", "链接中的假条编号无效。")
		return
	}
	l, err := s.store.GetLeave(r.Context(), id)
	if err != nil || l.StudentUserID != u.ID {
		s.error(w, r, http.StatusForbidden, "无权查看", "无法查看：只能查看自己的请假记录。")
		return
	}
	if l.ArchivedAt != 0 {
		s.error(w, r, http.StatusNotFound, "假条已归档", "该假条所属班级已清理，学生端不可查看；请联系原班主任或管理员。")
		return
	}
	edits, _ := s.store.LeaveTimeEdits(r.Context(), l.ID)
	out := studentLeaveDetailData{
		baseData: s.base(w, r, "student_leave_detail", "请假详情"),
		Leave:    l, IsOwner: true,
		CanQR: l.Status == "approved" && u.Status != "soft_deleted",
	}
	for _, e := range edits {
		out.Edits = append(out.Edits, convertLeaveEdit(e))
	}
	s.renderPage(w, r, "student_leave_detail", out)
}

func (s *Server) studentLeaveCancel(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "student")
	if !ok {
		return
	}
	if s.studentReadOnly(w, r, u) {
		return
	}
	id, err := parseID(r, "id")
	if err != nil {
		s.error(w, r, http.StatusNotFound, "假条不存在", "链接中的假条编号无效。")
		return
	}
	if err := s.store.CancelLeaveByStudent(r.Context(), u, id); err != nil {
		s.postFail(w, r, err)
		return
	}
	s.redirectFlash(w, r, "/s/leaves/"+itoa(id), "ok", "请假已取消。")
}

func (s *Server) studentLeaveQR(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "student")
	if !ok {
		return
	}
	if u.Status == "soft_deleted" {
		s.error(w, r, http.StatusForbidden, "无法查看二维码", "账号已停用，仅可查看历史；不能查看二维码。")
		return
	}
	id, err := parseID(r, "id")
	if err != nil {
		s.error(w, r, http.StatusNotFound, "假条不存在", "链接中的假条编号无效。")
		return
	}
	l, err := s.store.GetLeave(r.Context(), id)
	if err != nil || l.StudentUserID != u.ID {
		s.error(w, r, http.StatusForbidden, "无权查看", "无法查看：只能查看自己的请假记录。")
		return
	}
	if l.Status != "approved" || l.ArchivedAt != 0 {
		s.error(w, r, http.StatusNotFound, "二维码不可用", "只有已批准且未归档的请假才有二维码。")
		return
	}
	png, err := qrcode.Encode(s.abs("/p/"+s.store.QRToken(l.ID)), qrcode.Medium, 512)
	if err != nil {
		s.error(w, r, http.StatusInternalServerError, "二维码生成失败", "请稍后重试；若持续失败，请联系管理员。")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Disposition", "inline; filename=leave-pass.png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

func (s *Server) profilePage(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r)
	if !ok {
		return
	}
	if u.Role == "student" && u.Status == "soft_deleted" {
		s.redirectFlash(w, r, "/s/leaves", "warn", "账号已停用，仅可查看历史；不能修改资料。")
		return
	}
	sessionCount, _ := s.store.CountUserSessions(r.Context(), u.ID)
	d := profileData{
		baseData: s.base(w, r, "profile", "个人资料"),
		Profile:  u, Action: passwordAction(r.URL.Path),
		RevokeAction: profileBase(r.URL.Path) + "/sessions/revoke",
		SessionCount: sessionCount,
	}
	if u.Role == "student" {
		st, err := s.store.GetStudent(r.Context(), u.ID)
		d.IsStudent = true
		if err != nil {
			d.StudentMissing = true
		}
		d.Student = st
	}
	s.renderPage(w, r, "profile", d)
}

func passwordAction(p string) string {
	if strings.HasSuffix(p, "/password") {
		p = strings.TrimSuffix(p, "password")
	}
	return strings.TrimSuffix(p, "/") + "/password"
}

func profileBase(p string) string {
	p = strings.TrimSuffix(p, "/")
	return strings.TrimSuffix(p, "/password")
}

func profilePathForRole(role string) string {
	switch role {
	case "admin":
		return "/admin/profile"
	case "community":
		return "/c/profile"
	case "teacher", "counselor":
		return "/t/profile"
	default:
		return "/s/profile"
	}
}

// profileRevokeOtherSessions signs out the current account's other browsers.
// It never touches another user's sessions.
func (s *Server) profileRevokeOtherSessions(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r)
	if !ok {
		return
	}
	if u.Role == "student" && u.Status == "soft_deleted" {
		s.error(w, r, http.StatusForbidden, "无法退出其他设备", "账号已停用，仅可查看历史。")
		return
	}
	sc, _ := sessionFrom(r.Context())
	n, err := s.store.DeleteOtherUserSessions(r.Context(), u.ID, sc.TokenHash)
	if err != nil {
		s.postFail(w, r, err)
		return
	}
	_ = s.store.Audit(r.Context(), nil, u, "security.revoke_own_sessions", "session", u.ID,
		"退出其他设备（"+strconv.FormatInt(n, 10)+" 条）", clientIP(r))
	msg := "当前没有其他设备会话。"
	if n > 0 {
		msg = "已退出其他设备（" + strconv.FormatInt(n, 10) + " 条）。"
	}
	s.redirectFlash(w, r, profilePathForRole(u.Role), "ok", msg)
}

func (s *Server) profilePassword(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r)
	if !ok {
		return
	}
	if u.Role == "student" && u.Status == "soft_deleted" {
		s.error(w, r, http.StatusForbidden, "无法修改密码", "账号已停用，仅可查看历史。")
		return
	}
	if r.FormValue("password") != r.FormValue("password2") {
		s.renderProfileError(w, r, "两次输入的新密码不一致。")
		return
	}
	if err := s.store.ChangePassword(r.Context(), u, r.FormValue("old_password"), r.FormValue("password"), clientIP(r)); err != nil {
		s.renderProfileError(w, r, err.Error())
		return
	}
	s.clearCookie(w, cookieSession)
	s.redirectFlash(w, r, loginForRole(u.Role), "ok", "密码已修改，请用新密码重新登录。")
}

func loginForRole(role string) string {
	switch role {
	case "admin":
		return "/admin/login"
	case "teacher", "counselor", "community":
		return "/staff/login"
	default:
		return "/login"
	}
}

func (s *Server) renderProfileError(w http.ResponseWriter, r *http.Request, msg string) {
	u, _ := s.authed(r)
	sessionCount, _ := s.store.CountUserSessions(r.Context(), u.ID)
	d := profileData{
		baseData: s.base(w, r, "profile", "个人资料"),
		Profile:  u, Action: passwordAction(r.URL.Path), Error: msg,
		RevokeAction: profileBase(r.URL.Path) + "/sessions/revoke",
		SessionCount: sessionCount,
	}
	if u.Role == "student" {
		st, err := s.store.GetStudent(r.Context(), u.ID)
		d.IsStudent = true
		d.Student = st
		d.StudentMissing = err != nil
	}
	s.render(w, r, http.StatusUnprocessableEntity, "profile", d)
}

func (s *Server) notificationsPage(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r)
	if !ok {
		return
	}
	if u.Role == "student" && u.Status == "soft_deleted" {
		s.redirectFlash(w, r, "/s/leaves", "warn", "账号已停用，仅可查看历史；通知入口已隐藏。")
		return
	}
	list, _ := s.store.ListNotifications(r.Context(), u.ID, 100)
	b := s.base(w, r, "notifications", "通知中心")
	s.renderPage(w, r, "notifications", notificationsData{
		baseData: b, Notices: list, Action: notificationsAction(r.URL.Path),
	})
}

func notificationsAction(p string) string {
	if strings.HasSuffix(p, "/read") {
		p = strings.TrimSuffix(p, "read")
	}
	return strings.TrimSuffix(p, "/") + "/read"
}

func (s *Server) notificationsRead(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r)
	if !ok {
		return
	}
	if u.Role == "student" && u.Status == "soft_deleted" {
		s.error(w, r, http.StatusForbidden, "无法操作", "账号已停用，仅可查看历史。")
		return
	}
	_ = s.store.MarkAllNotificationsRead(r.Context(), u.ID)
	s.redirect(w, r, strings.TrimSuffix(r.URL.Path, "/read"))
}

// convertLeaveEdit converts the anonymous row returned by LeaveTimeEdits.
func convertLeaveEdit(e struct {
	EditorName string
	OldStart   int64
	OldEnd     int64
	NewStart   int64
	NewEnd     int64
	CreatedAt  int64
}) leaveEdit {
	return leaveEdit{EditorName: e.EditorName, OldStart: e.OldStart, OldEnd: e.OldEnd,
		NewStart: e.NewStart, NewEnd: e.NewEnd, CreatedAt: e.CreatedAt}
}
