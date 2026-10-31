package web

import (
	"encoding/csv"
	"net/http"
	"strconv"
	"strings"
	"time"

	"leavepass/internal/app"
	"leavepass/internal/validate"
)

type adminDashData struct {
	baseData
	Stats     app.AdminStats
	Unclaimed []app.Class
	Archived  []app.ArchivedClass
	Imports   []app.ImportBatch
	Audit     []app.AuditLog
}

type adminLeavesData struct {
	baseData
	Leaves    []app.Leave
	Total     int
	Filter    app.LeaveFilter
	Buildings []string
	Classes   []app.Class
	PageNo    int
	Pages     int
	Query     string
}

type adminLeaveData struct {
	baseData
	Leave app.Leave
	Edits []leaveEdit
}

type adminReportsData struct {
	baseData
	Dimension string
	Rows      []app.ReportRow
	Total     int
	Approved  int
	Pending   int
	Rejected  int
	Cancelled int
}

type adminClassesData struct {
	baseData
	Classes  []app.Class
	Archived []app.ArchivedClass
	Query    string
}

type adminClassData struct {
	baseData
	Class      app.Class
	Students   []app.StudentRow
	Leaves     []app.Leave
	Pending    int
	Teachers   []app.User
	Counselors []app.User
	Invite     app.InvitationRow
	HasInvite  bool
	Error      string
}

type adminUsersData struct {
	baseData
	Users []app.User
	Role  string
	Query string
	Error string
}

type adminUserData struct {
	baseData
	User         app.User
	Student      app.StudentRow
	HasStudent   bool
	Edits        []app.DormEditRow
	Classes      []app.Class
	PendingReset app.ResetRequest
	HasReset     bool
	ResetLink    string
	Error        string
	Notice       string
}

type adminDormEditsData struct {
	baseData
	Student app.StudentRow
	Edits   []app.DormEditRow
}

type adminImportsData struct {
	baseData
	Batches []app.ImportBatch
}

type adminCodesData struct {
	baseData
	Codes          []app.RegistrationCode
	NewCode        string
	NewRole        string
	Error          string
	FormRole       string
	FormMaxUses    string
	FormValidFrom  string
	FormValidUntil string
	FormNote       string
}

type adminRecoverData struct {
	baseData
	Requests []app.ResetRequest
	Ident    string
	Code     string
	Found    app.ResetRequest
	HasFound bool
	Error    string
	Notice   string
}

type adminCommunityData struct {
	baseData
	Rows    []communityAdminRow
	Options []string
	Error   string
	Notice  string
}

type communityAdminRow struct {
	User      app.User
	Buildings []string
	Options   []string
}

type adminSecurityData struct {
	baseData
	Locked   []lockedRow
	Attempts []app.RegistrationAttemptRow
	Notice   string
}

type lockedRow struct {
	Key     string
	Scope   string
	Until   int64
	Count   int64
	Updated int64
}

type adminAuditData struct {
	baseData
	Logs []app.AuditLog
}

func pagesOf(total, size int) int {
	if size <= 0 {
		return 1
	}
	n := (total + size - 1) / size
	if n < 1 {
		return 1
	}
	return n
}

// activeUsers filters out soft-deleted accounts; disabled accounts cannot be assigned.
func activeUsers(list []app.User) []app.User {
	var out []app.User
	for _, u := range list {
		if u.Status == "active" {
			out = append(out, u)
		}
	}
	return out
}

func (s *Server) adminDashboard(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "admin")
	if !ok {
		return
	}
	_ = u
	classes, _ := s.store.AllClasses(r.Context())
	var unclaimed []app.Class
	for _, c := range classes {
		if c.Unclaimed() {
			unclaimed = append(unclaimed, c)
		}
	}
	archived, _ := s.store.ArchivedClassList(r.Context())
	imports, _ := s.store.ListAllImportBatches(r.Context(), 8)
	audit, _ := s.store.ListAuditLogs(r.Context(), 8)
	b := s.base(w, r, "admin_dashboard", "管理员概览")
	s.renderPage(w, r, "admin_dashboard", adminDashData{
		baseData: b, Stats: s.store.Stats(r.Context()), Unclaimed: unclaimed,
		Archived: archived, Imports: imports, Audit: audit,
	})
}

func (s *Server) adminLeaves(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.require(w, r, "admin"); !ok {
		return
	}
	s.renderAdminLeaves(w, r, http.StatusOK)
}

func (s *Server) renderAdminLeaves(w http.ResponseWriter, r *http.Request, status int) {
	f := leaveFilterFromQuery(r)
	list, total, err := s.store.AdminLeaves(r.Context(), f)
	if err != nil {
		s.error(w, r, http.StatusUnprocessableEntity, "筛选条件有误", err.Error()+"；请检查日期后重试。")
		return
	}
	classes, _ := s.store.AllClasses(r.Context())
	b := s.base(w, r, "admin_leaves", "请假查询")
	s.render(w, r, status, "admin_leaves", adminLeavesData{
		baseData: b, Leaves: list, Total: total, Filter: f,
		Buildings: s.store.CampusBuildings(), Classes: classes,
		PageNo: f.Page, Pages: pagesOf(total, 50), Query: r.URL.RawQuery,
	})
}

func (s *Server) adminLeavesExport(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.require(w, r, "admin"); !ok {
		return
	}
	f := leaveFilterFromQuery(r)
	f.Page, f.PageSize = 1, 100000
	list, _, err := s.store.AdminLeaves(r.Context(), f)
	if err != nil {
		s.error(w, r, http.StatusUnprocessableEntity, "导出失败", err.Error()+"；请检查筛选日期后重试。")
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=leave-requests.csv")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"假条ID", "学生姓名", "学号", "联系电话", "班级", "专业系", "楼栋", "房间",
		"班主任", "辅导员", "开始时间", "结束时间", "状态", "提交时间", "审批结果", "审批人", "驳回原因", "取消原因"})
	for _, l := range list {
		_ = cw.Write(csvRow(
			strconv.FormatInt(l.ID, 10), l.StudentName, l.StudentNo, l.ContactPhone, l.ClassName, l.Department,
			l.DormBuilding, l.DormRoom, l.TeacherName, l.CounselorName,
			csvTime(l.StartAt), csvTime(l.EndAt), statusName(l.Status), csvTime(l.CreatedAt),
			decisionText(l), l.ApproverName, l.RejectReason, l.CancelReason,
		))
	}
	cw.Flush()
}

func csvTime(ts int64) string {
	if ts <= 0 {
		return ""
	}
	return time.Unix(ts, 0).In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04")
}

// csvRow neutralises spreadsheet formulas in every user-controlled cell.
func csvRow(cells ...string) []string {
	out := make([]string, len(cells))
	for i, c := range cells {
		c = strings.TrimSpace(c)
		if c != "" {
			switch c[0] {
			case '=', '+', '-', '@', '\t', '\r':
				c = "'" + c
			}
		}
		out[i] = c
	}
	return out
}

func (s *Server) adminLeaveDetail(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.require(w, r, "admin"); !ok {
		return
	}
	id, err := parseID(r, "id")
	if err != nil {
		s.error(w, r, http.StatusNotFound, "假条不存在", "链接中的假条编号无效。")
		return
	}
	l, err := s.store.GetLeave(r.Context(), id)
	if err != nil {
		s.error(w, r, http.StatusNotFound, "假条不存在", "该假条不存在或已被删除。")
		return
	}
	edits, _ := s.store.LeaveTimeEdits(r.Context(), id)
	var list []leaveEdit
	for _, e := range edits {
		list = append(list, convertLeaveEdit(e))
	}
	b := s.base(w, r, "admin_leave", "假条详情")
	s.renderPage(w, r, "admin_leave", adminLeaveData{baseData: b, Leave: l, Edits: list})
}

func (s *Server) adminLeaveCancel(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "admin")
	if !ok {
		return
	}
	id, err := parseID(r, "id")
	if err != nil {
		s.error(w, r, http.StatusNotFound, "假条不存在", "链接中的假条编号无效。")
		return
	}
	if err := s.store.CancelLeaveByAdmin(r.Context(), u, id, r.FormValue("reason"), clientIP(r)); err != nil {
		s.postFail(w, r, err)
		return
	}
	s.redirectFlash(w, r, "/admin/leaves/"+itoa(id), "ok", "待审批请假已取消，学生与审批人已收到通知。")
}

func (s *Server) adminLeaveDelete(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "admin")
	if !ok {
		return
	}
	id, err := parseID(r, "id")
	if err != nil {
		s.error(w, r, http.StatusNotFound, "假条不存在", "链接中的假条编号无效。")
		return
	}
	if r.FormValue("confirm") != "1" {
		s.error(w, r, http.StatusUnprocessableEntity, "删除未执行", "物理删除不可恢复；请勾选“我已确认删除”后重试。")
		return
	}
	if _, err := s.store.GetLeave(r.Context(), id); err != nil {
		s.error(w, r, http.StatusNotFound, "假条不存在", "该假条不存在或已被删除。")
		return
	}
	if err := s.store.DeleteLeaveByAdmin(r.Context(), u, id); err != nil {
		s.postFail(w, r, err)
		return
	}
	s.redirectFlash(w, r, "/admin/leaves", "ok", "假条已物理删除，且不可恢复。")
}

func (s *Server) adminReports(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.require(w, r, "admin"); !ok {
		return
	}
	dim := strings.TrimSpace(r.URL.Query().Get("dimension"))
	switch dim {
	case "building", "class", "teacher", "counselor":
	default:
		dim = "building"
	}
	rows, err := s.store.AdminReport(r.Context(), dim)
	if err != nil {
		s.error(w, r, http.StatusUnprocessableEntity, "报表生成失败", err.Error())
		return
	}
	d := adminReportsData{baseData: s.base(w, r, "admin_reports", "汇总报表"), Dimension: dim, Rows: rows}
	for _, row := range rows {
		d.Total += row.Total
		d.Approved += row.Approved
		d.Pending += row.Pending
		d.Rejected += row.Rejected
		d.Cancelled += row.Cancelled
	}
	s.renderPage(w, r, "admin_reports", d)
}

func (s *Server) adminClasses(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.require(w, r, "admin"); !ok {
		return
	}
	classes, _ := s.store.AllClasses(r.Context())
	archived, _ := s.store.ArchivedClassList(r.Context())
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q != "" {
		var filtered []app.Class
		needle := strings.ToLower(validate.NFKC(q))
		for _, c := range classes {
			if strings.Contains(strings.ToLower(c.Name), needle) || strings.Contains(strings.ToLower(c.Department), needle) {
				filtered = append(filtered, c)
			}
		}
		classes = filtered
	}
	b := s.base(w, r, "admin_classes", "班级管理")
	s.renderPage(w, r, "admin_classes", adminClassesData{baseData: b, Classes: classes, Archived: archived, Query: q})
}

func (s *Server) adminClassDetail(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.require(w, r, "admin"); !ok {
		return
	}
	id, err := parseID(r, "id")
	if err != nil {
		s.error(w, r, http.StatusNotFound, "班级不存在", "链接中的班级编号无效。")
		return
	}
	c, leaves, err := s.store.AdminClassDetail(r.Context(), id)
	if err != nil {
		s.error(w, r, http.StatusNotFound, "班级不存在", "该班级已被解散或清理；保留假条请在“保留假条”页查看。")
		return
	}
	students, _ := s.store.ClassStudents(r.Context(), id, true, "")
	teachers, _ := s.store.ListUsers(r.Context(), "teacher", "", 500)
	counselors, _ := s.store.ListUsers(r.Context(), "counselor", "", 500)
	teachers = activeUsers(teachers)
	counselors = activeUsers(counselors)
	invite, invErr := s.store.PendingInvite(r.Context(), id)
	pending := s.store.PendingCountForClass(r.Context(), id)
	b := s.base(w, r, "admin_class", c.Name)
	s.renderPage(w, r, "admin_class", adminClassData{
		baseData: b, Class: c, Students: students, Leaves: leaves, Pending: pending,
		Teachers: teachers, Counselors: counselors, Invite: invite, HasInvite: invErr == nil,
	})
}

func (s *Server) adminClassTeacher(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "admin")
	if !ok {
		return
	}
	id, err := parseID(r, "id")
	if err != nil {
		s.error(w, r, http.StatusNotFound, "班级不存在", "链接中的班级编号无效。")
		return
	}
	teacherID, err := strconvParse(r.FormValue("teacher_id"))
	if err != nil {
		s.error(w, r, http.StatusUnprocessableEntity, "指派未执行", "请选择一个可用的班主任账号。")
		return
	}
	if r.FormValue("confirm_move") != "1" {
		if current, err := s.store.TeacherClass(r.Context(), teacherID); err == nil && current.ID != id {
			s.error(w, r, http.StatusConflict, "指派未执行",
				"该教师当前是「"+current.Name+"」的班主任。确认指派会让原班立即转为待认领并开始 7 天清理倒计时；请勾选“确认将该教师移出原班级”后重试。")
			return
		}
	}
	if err := s.store.AssignTeacher(r.Context(), u, id, teacherID, clientIP(r)); err != nil {
		s.postFail(w, r, err)
		return
	}
	s.redirectFlash(w, r, "/admin/classes/"+itoa(id), "ok", "班主任已指派；若班级原为待认领，清理倒计时已立即取消。")
}

func (s *Server) adminClassCounselor(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "admin")
	if !ok {
		return
	}
	id, err := parseID(r, "id")
	if err != nil {
		s.error(w, r, http.StatusNotFound, "班级不存在", "链接中的班级编号无效。")
		return
	}
	counselorID, err := strconvParse(r.FormValue("counselor_id"))
	if err != nil {
		s.error(w, r, http.StatusUnprocessableEntity, "指派未执行", "请选择一个可用的辅导员账号。")
		return
	}
	if err := s.store.AssignCounselor(r.Context(), u, id, counselorID, clientIP(r)); err != nil {
		s.postFail(w, r, err)
		return
	}
	s.redirectFlash(w, r, "/admin/classes/"+itoa(id), "ok", "辅导员已调整。")
}

func (s *Server) adminClassDisband(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "admin")
	if !ok {
		return
	}
	id, err := parseID(r, "id")
	if err != nil {
		s.error(w, r, http.StatusNotFound, "班级不存在", "链接中的班级编号无效。")
		return
	}
	c, err := s.store.GetClass(r.Context(), id)
	if err != nil {
		s.error(w, r, http.StatusNotFound, "班级不存在", "该班级已被解散或清理。")
		return
	}
	if strings.TrimSpace(r.FormValue("confirm_name")) != c.Name {
		s.error(w, r, http.StatusUnprocessableEntity, "解散未执行",
			"解散班级不可撤销；请在确认框中输入完整班级名“"+c.Name+"”后重试。学生账号会保留并转为暂无班级。")
		return
	}
	if err := s.store.DisbandClass(r.Context(), u, id, clientIP(r)); err != nil {
		s.postFail(w, r, err)
		return
	}
	s.redirectFlash(w, r, "/admin/classes", "ok", "班级已解散；学生账号保留，待审批假条已取消，学生转为暂无班级。")
}

func (s *Server) adminUsers(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.require(w, r, "admin"); !ok {
		return
	}
	role := strings.TrimSpace(r.URL.Query().Get("role"))
	switch role {
	case "student", "teacher", "counselor", "community", "admin":
	default:
		role = ""
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	users, err := s.store.ListUsers(r.Context(), role, q, 300)
	b := s.base(w, r, "admin_users", "用户管理")
	d := adminUsersData{baseData: b, Users: users, Role: role, Query: q}
	if err != nil {
		d.Error = err.Error()
	}
	s.renderPage(w, r, "admin_users", d)
}

func (s *Server) adminUserCreate(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "admin")
	if !ok {
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	phone := strings.TrimSpace(r.FormValue("phone"))
	password := r.FormValue("password")
	created, err := s.store.CreateAdmin(r.Context(), u, username, phone, password, clientIP(r))
	if err != nil {
		users, _ := s.store.ListUsers(r.Context(), "", "", 300)
		role := strings.TrimSpace(r.FormValue("role"))
		switch role {
		case "student", "teacher", "counselor", "community", "admin":
		default:
			role = ""
		}
		b := s.base(w, r, "admin_users", "用户管理")
		s.render(w, r, http.StatusUnprocessableEntity, "admin_users", adminUsersData{
			baseData: b, Users: users, Role: role, Query: strings.TrimSpace(r.FormValue("q")), Error: err.Error(),
		})
		return
	}
	s.redirectFlash(w, r, "/admin/users/"+itoa(created.ID), "ok",
		"管理员已创建；初始密码为你表单中填写的值，请通过安全渠道转交。管理员不能创建学生账号。")
}

func (s *Server) adminUserDetail(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.require(w, r, "admin"); !ok {
		return
	}
	id, err := parseID(r, "id")
	if err != nil {
		s.error(w, r, http.StatusNotFound, "用户不存在", "链接中的用户编号无效。")
		return
	}
	d, ok := s.adminUserModel(w, r, id)
	if !ok {
		return
	}
	s.renderPage(w, r, "admin_user", d)
}

func (s *Server) adminUserModel(w http.ResponseWriter, r *http.Request, id int64) (adminUserData, bool) {
	u, err := s.store.GetUserByID(r.Context(), id)
	if err != nil {
		s.error(w, r, http.StatusNotFound, "用户不存在", "该用户不存在或已被清理。")
		return adminUserData{}, false
	}
	d := adminUserData{baseData: s.base(w, r, "admin_user", "用户详情"), User: u}
	if u.Role == "student" {
		st, err := s.store.GetStudent(r.Context(), id)
		if err == nil {
			d.Student, d.HasStudent = st, true
		}
		d.Edits, _ = s.store.ListDormEdits(r.Context(), id)
		d.Classes, _ = s.store.AllClasses(r.Context())
	}
	q, err := s.store.PendingResetForUser(r.Context(), id)
	if err == nil {
		d.PendingReset, d.HasReset = q, true
	}
	return d, true
}

func (s *Server) adminUserDelete(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "admin")
	if !ok {
		return
	}
	id, err := parseID(r, "id")
	if err != nil {
		s.error(w, r, http.StatusNotFound, "用户不存在", "链接中的用户编号无效。")
		return
	}
	if r.FormValue("confirm") != "1" {
		s.error(w, r, http.StatusUnprocessableEntity, "停用未执行", "请确认已核对账号后勾选确认再重试。")
		return
	}
	if err := s.store.SoftDeleteUser(r.Context(), u, id, r.FormValue("reason"), clientIP(r)); err != nil {
		s.postFail(w, r, err)
		return
	}
	s.redirectFlash(w, r, "/admin/users/"+itoa(id), "ok", "账号已进入 7 天只读保留期，到期后自动清理。")
}

func (s *Server) adminUserFixNo(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "admin")
	if !ok {
		return
	}
	id, err := parseID(r, "id")
	if err != nil {
		s.error(w, r, http.StatusNotFound, "用户不存在", "链接中的用户编号无效。")
		return
	}
	if err := s.store.FixStudentNo(r.Context(), u, id, r.FormValue("student_no"), clientIP(r)); err != nil {
		s.postFail(w, r, err)
		return
	}
	s.redirectFlash(w, r, "/admin/users/"+itoa(id), "ok", "学号已修正，旧学号已释放，该学生所有会话已失效。")
}

func (s *Server) adminUserClass(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "admin")
	if !ok {
		return
	}
	id, err := parseID(r, "id")
	if err != nil {
		s.error(w, r, http.StatusNotFound, "用户不存在", "链接中的用户编号无效。")
		return
	}
	classID, err := strconvParse(r.FormValue("class_id"))
	if err != nil {
		s.error(w, r, http.StatusUnprocessableEntity, "调班未执行", "请选择目标班级。")
		return
	}
	if err := s.store.AdjustStudentClass(r.Context(), u, id, classID, clientIP(r)); err != nil {
		s.postFail(w, r, err)
		return
	}
	s.redirectFlash(w, r, "/admin/users/"+itoa(id), "ok", "学生班级已调整；原班级历史假条仍保留原快照。")
}

func (s *Server) adminUserPhone(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "admin")
	if !ok {
		return
	}
	id, err := parseID(r, "id")
	if err != nil {
		s.error(w, r, http.StatusNotFound, "用户不存在", "链接中的用户编号无效。")
		return
	}
	if err := s.store.ChangeStaffPhone(r.Context(), u, id, r.FormValue("phone"), clientIP(r)); err != nil {
		s.postFail(w, r, err)
		return
	}
	s.redirectFlash(w, r, "/admin/users/"+itoa(id), "ok", "手机号已更换；旧号已释放，新号必须保持唯一。")
}

func (s *Server) adminUserResetLink(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "admin")
	if !ok {
		return
	}
	id, err := parseID(r, "id")
	if err != nil {
		s.error(w, r, http.StatusNotFound, "用户不存在", "链接中的用户编号无效。")
		return
	}
	link, err := s.store.GenerateAdminResetLink(r.Context(), u, id, clientIP(r))
	if err != nil {
		s.postFail(w, r, err)
		return
	}
	d, ok := s.adminUserModel(w, r, id)
	if !ok {
		return
	}
	d.ResetLink = link
	d.Notice = "一次性重置链接已生成，10 分钟内有效且只能使用一次；关闭页面后无法再次查看，请立即通过安全渠道发送。"
	s.renderPage(w, r, "admin_user", d)
}

func (s *Server) adminStudentDorm(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "admin")
	if !ok {
		return
	}
	id, err := parseID(r, "id")
	if err != nil {
		s.error(w, r, http.StatusNotFound, "学生不存在", "链接中的学生编号无效。")
		return
	}
	st, err := s.store.GetStudent(r.Context(), id)
	if err != nil {
		s.error(w, r, http.StatusNotFound, "学生不存在", "该学生不存在或已不在读。")
		return
	}
	if err := s.store.UpdateDorm(r.Context(), u, id, r.FormValue("old_dorm"), r.FormValue("dorm"), r.FormValue("reason"), clientIP(r)); err != nil {
		s.postFail(w, r, err)
		return
	}
	s.redirectFlash(w, r, "/admin/students/"+itoa(id)+"/dorm-edits", "ok", "宿舍已修正；学生和班级审批人已收到通知，修改记录已写入审计。")
	_ = st
}

func (s *Server) adminStudentDormEdits(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.require(w, r, "admin"); !ok {
		return
	}
	id, err := parseID(r, "id")
	if err != nil {
		s.error(w, r, http.StatusNotFound, "学生不存在", "链接中的学生编号无效。")
		return
	}
	st, err := s.store.GetStudent(r.Context(), id)
	if err != nil {
		s.error(w, r, http.StatusNotFound, "学生不存在", "该学生不存在或已不在读。")
		return
	}
	edits, _ := s.store.ListDormEdits(r.Context(), id)
	b := s.base(w, r, "admin_dorm_edits", st.Name+" · 宿舍修改记录")
	s.renderPage(w, r, "admin_dorm_edits", adminDormEditsData{baseData: b, Student: st, Edits: edits})
}

func (s *Server) adminImports(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.require(w, r, "admin"); !ok {
		return
	}
	batches, _ := s.store.ListAllImportBatches(r.Context(), 300)
	b := s.base(w, r, "admin_imports", "学生导入记录")
	s.renderPage(w, r, "admin_imports", adminImportsData{baseData: b, Batches: batches})
}

func (s *Server) adminRegistrationCodes(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.require(w, r, "admin"); !ok {
		return
	}
	s.renderAdminCodes(w, r, http.StatusOK, "", "", "", adminCodesData{})
}

func (s *Server) renderAdminCodes(w http.ResponseWriter, r *http.Request, status int, newCode, newRole, msg string, form adminCodesData) {
	codes, _ := s.store.ListRegistrationCodes(r.Context())
	b := s.base(w, r, "admin_codes", "注册码")
	form.baseData = b
	form.Codes = codes
	form.NewCode = newCode
	form.NewRole = newRole
	form.Error = msg
	s.render(w, r, status, "admin_codes", form)
}

func (s *Server) adminRegistrationCodeCreate(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "admin")
	if !ok {
		return
	}
	role := strings.TrimSpace(r.FormValue("role"))
	maxUses, err := strconvParse(r.FormValue("max_uses"))
	if err != nil {
		maxUses = 0
	}
	validFrom, err := parseCSTLocal(r.FormValue("valid_from"))
	if err != nil {
		validFrom = 0
	}
	validUntil, err := parseCSTLocal(r.FormValue("valid_until"))
	if err != nil {
		validUntil = 0
	}
	_, plain, err := s.store.CreateRegistrationCode(r.Context(), u, role, maxUses, validFrom, validUntil, r.FormValue("note"), clientIP(r))
	if err != nil {
		s.renderAdminCodes(w, r, http.StatusUnprocessableEntity, "", role, err.Error(), adminCodesData{
			FormMaxUses:    r.FormValue("max_uses"),
			FormValidFrom:  r.FormValue("valid_from"),
			FormValidUntil: r.FormValue("valid_until"),
			FormNote:       r.FormValue("note"),
		})
		return
	}
	s.renderAdminCodes(w, r, http.StatusOK, plain, role, "", adminCodesData{})
}

func (s *Server) adminRegistrationCodeUpdate(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "admin")
	if !ok {
		return
	}
	id, err := parseID(r, "id")
	if err != nil {
		s.error(w, r, http.StatusNotFound, "注册码不存在", "链接中的注册码编号无效。")
		return
	}
	var active *bool
	switch r.FormValue("action") {
	case "disable":
		v := false
		active = &v
	case "enable":
		v := true
		active = &v
	}
	maxUses, _ := strconvParse(r.FormValue("max_uses"))
	validUntil, err := parseCSTLocal(r.FormValue("valid_until"))
	if err != nil {
		validUntil = 0
	}
	if err := s.store.UpdateRegistrationCode(r.Context(), u, id, maxUses, validUntil, active, clientIP(r)); err != nil {
		s.renderAdminCodes(w, r, http.StatusUnprocessableEntity, "", "", err.Error(), adminCodesData{})
		return
	}
	s.redirectFlash(w, r, "/admin/settings/registration-codes", "ok", "注册码已更新。")
}

func (s *Server) adminRecoverPage(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "admin")
	if !ok {
		return
	}
	reqs, _ := s.store.AdminPendingResets(r.Context(), u.ID)
	b := s.base(w, r, "admin_recover", "辅助重置处理")
	s.renderPage(w, r, "admin_recover", adminRecoverData{baseData: b, Requests: reqs})
}

func (s *Server) adminRecoverVerify(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "admin")
	if !ok {
		return
	}
	ident := strings.TrimSpace(r.FormValue("ident"))
	code := strings.TrimSpace(r.FormValue("code"))
	q, err := s.store.VerifyResetCode(r.Context(), u, ident, code)
	reqs, _ := s.store.AdminPendingResets(r.Context(), u.ID)
	d := adminRecoverData{baseData: s.base(w, r, "admin_recover", "辅助重置处理"), Requests: reqs, Ident: ident, Code: code}
	if err != nil {
		d.Error = err.Error()
		s.render(w, r, http.StatusUnprocessableEntity, "admin_recover", d)
		return
	}
	d.Found, d.HasFound = q, true
	s.renderPage(w, r, "admin_recover", d)
}

func (s *Server) adminRecoverApprove(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "admin")
	if !ok {
		return
	}
	id, err := parseID(r, "id")
	if err != nil {
		s.error(w, r, http.StatusNotFound, "申请不存在", "链接中的申请编号无效。")
		return
	}
	code := strings.TrimSpace(r.FormValue("code"))
	browser := ""
	if c, err := r.Cookie(cookieRecover); err == nil {
		browser = c.Value
	}
	if err := s.store.ApproveResetRequest(r.Context(), u, id, code, browser); err != nil {
		s.postFail(w, r, err)
		return
	}
	s.redirectFlash(w, r, "/admin/recover", "ok", "申请已批准；申请人回到申请状态页即可获取一次性重置链接。")
}

func (s *Server) adminRecoverReject(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "admin")
	if !ok {
		return
	}
	id, err := parseID(r, "id")
	if err != nil {
		s.error(w, r, http.StatusNotFound, "申请不存在", "链接中的申请编号无效。")
		return
	}
	if err := s.store.RejectResetRequest(r.Context(), u, id); err != nil {
		s.postFail(w, r, err)
		return
	}
	s.redirectFlash(w, r, "/admin/recover", "ok", "申请已拒绝。")
}

func (s *Server) adminCommunity(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.require(w, r, "admin"); !ok {
		return
	}
	users, _ := s.store.ListUsers(r.Context(), "community", "", 500)
	d := adminCommunityData{baseData: s.base(w, r, "admin_community", "社区老师楼栋"), Options: s.store.CampusBuildings()}
	for _, u := range users {
		buildings, _ := s.store.CommunityBuildings(r.Context(), u.ID)
		d.Rows = append(d.Rows, communityAdminRow{
			User: u, Buildings: buildings,
			Options: buildingOptions(s.store.CampusBuildings(), buildings),
		})
	}
	s.renderPage(w, r, "admin_community", d)
}

func (s *Server) adminCommunityPost(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "admin")
	if !ok {
		return
	}
	id, err := parseID(r, "id")
	if err != nil {
		s.error(w, r, http.StatusNotFound, "社区老师不存在", "链接中的用户编号无效。")
		return
	}
	target, err := s.store.GetUserByID(r.Context(), id)
	if err != nil || target.Role != "community" {
		s.error(w, r, http.StatusNotFound, "社区老师不存在", "只能为社区老师设置负责楼栋。")
		return
	}
	buildings := r.Form["buildings"]
	custom := strings.TrimSpace(r.FormValue("custom"))
	if custom != "" {
		buildings = append(buildings, strings.FieldsFunc(custom, func(r rune) bool {
			return r == ',' || r == '，' || r == ' ' || r == '\n' || r == '\t'
		})...)
	}
	if err := s.store.SetCommunityBuildings(r.Context(), id, buildings); err != nil {
		s.error(w, r, http.StatusUnprocessableEntity, "保存失败", err.Error()+"；请检查楼栋名后重试。")
		return
	}
	_ = s.store.Audit(r.Context(), nil, u, "community.buildings", "user", id,
		"管理员调整社区老师 "+target.Username+" 的负责楼栋为 "+strings.Join(buildings, "、"), clientIP(r))
	s.redirectFlash(w, r, "/admin/community", "ok", "负责楼栋已更新，社区老师可见范围立即生效。")
}

func (s *Server) adminSecurity(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.require(w, r, "admin"); !ok {
		return
	}
	locked, _ := s.store.ListLockedIPs(r.Context())
	attempts, _ := s.store.ListRegistrationAttempts(r.Context(), 100)
	var rows []lockedRow
	for _, l := range locked {
		rows = append(rows, lockedRow{l.Key, l.Scope, l.Until, l.Count, l.Updated})
	}
	d := adminSecurityData{baseData: s.base(w, r, "admin_security", "安全与限流"), Locked: rows, Attempts: attempts}
	s.renderPage(w, r, "admin_security", d)
}

func (s *Server) adminSecurityUnlock(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "admin")
	if !ok {
		return
	}
	ip := strings.TrimSpace(r.FormValue("ip"))
	if ip == "" {
		s.error(w, r, http.StatusUnprocessableEntity, "解封失败", "请填写要解封的 IP 地址。")
		return
	}
	if err := s.store.UnlockIP(r.Context(), ip); err != nil {
		s.postFail(w, r, err)
		return
	}
	_ = s.store.Audit(r.Context(), nil, u, "security.unlock", "ip", 0, "解除 IP 限制 "+ip, clientIP(r))
	s.redirectFlash(w, r, "/admin/security", "ok", "该 IP 的登录/注册限流记录已清除。")
}

func (s *Server) adminAudit(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.require(w, r, "admin"); !ok {
		return
	}
	logs, _ := s.store.ListAuditLogs(r.Context(), 300)
	b := s.base(w, r, "admin_audit", "审计日志")
	s.renderPage(w, r, "admin_audit", adminAuditData{baseData: b, Logs: logs})
}
