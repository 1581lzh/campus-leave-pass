package web

import (
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"leavepass/internal/app"
	"leavepass/internal/validate"
)

type classCard struct {
	Class        app.Class
	Pending      int
	CanImport    bool
	ImportReason string
}

type teacherDashData struct {
	baseData
	Classes     []classCard
	Pending     []app.Leave
	Invitations []app.InvitationRow
	Notices     []app.Notification
	Popup       string
}

type classNewData struct {
	baseData
	Name          string
	Department    string
	Error         string
	NeedSwitch    bool
	ConfirmSwitch bool
}

type claimData struct {
	baseData
	Classes        []app.Class
	Search         string
	Found          app.Class
	HasFound       bool
	Error          string
	TakeoverNeeded bool
	SwitchNeeded   bool
}

type classDetailData struct {
	baseData
	Class        app.Class
	Students     []app.StudentRow
	Pending      []app.Leave
	Recent       []app.Leave
	Invite       app.InvitationRow
	HasInvite    bool
	CanManage    bool
	CanImport    bool
	ImportReason string
	Error        string
	Notice       string
}

type classStudentsData struct {
	baseData
	Class        app.Class
	Students     []app.StudentRow
	Query        string
	CanImport    bool
	ImportReason string
	ImportErrors []app.ImportError
	ImportDone   string
	Error        string
}

type teacherImportsData struct {
	baseData
	Class   app.Class
	Batches []app.ImportBatch
}

type counselorPageData struct {
	baseData
	Class     app.Class
	Invite    app.InvitationRow
	HasInvite bool
	Search    string
	Found     app.User
	HasFound  bool
	Error     string
	Notice    string
}

type invitationsData struct {
	baseData
	Invitations []app.InvitationRow
}

type staffRecoverData struct {
	baseData
	Base     string
	Requests []app.ResetRequest
	Ident    string
	Code     string
	Found    app.ResetRequest
	HasFound bool
	Error    string
	Notice   string
}

type teacherLeaveData struct {
	baseData
	Leave     app.Leave
	Edits     []leaveEdit
	CanDecide bool
	ProxyURL  string
	Error     string
	Notice    string
}

func (s *Server) teacherDashboard(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "teacher", "counselor")
	if !ok {
		return
	}
	classes, _ := s.store.ClassesFor(r.Context(), u)
	var cards []classCard
	var ids []int64
	missing := []string{}
	for _, c := range classes {
		ids = append(ids, c.ID)
		card := classCard{Class: c, Pending: s.store.PendingCountForClass(r.Context(), c.ID)}
		card.CanImport, card.ImportReason = importAvailability(u, c)
		cards = append(cards, card)
		if u.Role == "teacher" && c.CounselorUserID == 0 {
			missing = append(missing, c.Name)
		}
	}
	pending, _ := s.store.PendingLeavesForClasses(r.Context(), ids)
	var invites []app.InvitationRow
	if u.Role == "counselor" {
		invites, _ = s.store.InvitationsFor(r.Context(), u.ID)
	}
	notices, _ := s.store.ListNotifications(r.Context(), u.ID, 5)
	popup := ""
	if len(missing) > 0 && !s.store.PopupAlreadyShown(r.Context(), u.ID, "no_counselor") {
		popup = "当前班级暂无辅导员（" + strings.Join(missing, "、") + "），审批将只能由你完成，且暂时不能导入学生；请在班级设置中搜索并邀请辅导员加入。"
	}
	b := s.base(w, r, "teacher_dashboard", "教职工首页")
	s.renderPage(w, r, "teacher_dashboard", teacherDashData{
		baseData: b, Classes: cards, Pending: pending, Invitations: invites, Notices: notices, Popup: popup,
	})
}

func importAvailability(u app.User, c app.Class) (bool, string) {
	if u.Role == "teacher" && c.TeacherUserID != u.ID || u.Role == "counselor" && c.CounselorUserID != u.ID {
		return false, "无法导入：只有本班当前班主任或辅导员可以导入学生。"
	}
	switch {
	case c.TeacherUserID == 0 && c.CounselorUserID == 0:
		return false, "无法导入：本班尚未配齐班主任和辅导员（当前缺少：班主任、辅导员）。缺少班主任时由班主任认领或管理员指派；缺少辅导员时在“班级设置”中搜索并邀请。"
	case c.TeacherUserID == 0:
		return false, "无法导入：本班尚未配齐班主任和辅导员（当前缺少：班主任）。缺少班主任时由班主任认领或由管理员指派。"
	case c.CounselorUserID == 0:
		return false, "无法导入：本班尚未配齐班主任和辅导员（当前缺少：辅导员）。请在“班级设置”中搜索并邀请辅导员。"
	}
	return true, ""
}

func (s *Server) teacherClasses(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "teacher", "counselor")
	if !ok {
		return
	}
	classes, _ := s.store.ClassesFor(r.Context(), u)
	var cards []classCard
	for _, c := range classes {
		card := classCard{Class: c, Pending: s.store.PendingCountForClass(r.Context(), c.ID)}
		card.CanImport, card.ImportReason = importAvailability(u, c)
		cards = append(cards, card)
	}
	b := s.base(w, r, "teacher_classes", "我的班级")
	s.renderPage(w, r, "teacher_classes", struct {
		baseData
		Classes []classCard
	}{b, cards})
}

func (s *Server) teacherClassNew(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "teacher", "counselor")
	if !ok {
		return
	}
	_ = u
	s.renderClassNew(w, r, http.StatusOK, classNewData{baseData: s.base(w, r, "class_new", "创建班级")})
}

func (s *Server) renderClassNew(w http.ResponseWriter, r *http.Request, status int, d classNewData) {
	s.render(w, r, status, "class_new", d)
}

func (s *Server) teacherClassCreate(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "teacher", "counselor")
	if !ok {
		return
	}
	d := classNewData{
		baseData:      s.base(w, r, "class_new", "创建班级"),
		Name:          strings.TrimSpace(r.FormValue("name")),
		Department:    strings.TrimSpace(r.FormValue("department")),
		ConfirmSwitch: r.FormValue("confirm_switch") == "1",
	}
	c, err := s.store.CreateClass(r.Context(), u, d.Name, d.Department, d.ConfirmSwitch)
	if err != nil {
		d.Error = err.Error()
		d.NeedSwitch = errors.Is(err, app.ErrClassSwitchNeeded)
		status := http.StatusUnprocessableEntity
		if errors.Is(err, app.ErrClassSwitchNeeded) {
			status = http.StatusConflict
		}
		s.renderClassNew(w, r, status, d)
		return
	}
	s.redirectFlash(w, r, "/t/classes/"+itoa(c.ID), "ok", "班级已创建。")
}

func (s *Server) claimableClasses(r *http.Request, u app.User) []app.Class {
	if u.Role == "counselor" {
		classes, _ := s.store.CounselorClaimableClasses(r.Context())
		return classes
	}
	classes, _ := s.store.ClaimableClasses(r.Context())
	return classes
}

func (s *Server) teacherClaimPage(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "teacher", "counselor")
	if !ok {
		return
	}
	classes := s.claimableClasses(r, u)
	d := claimData{baseData: s.base(w, r, "class_claim", "认领班级"), Classes: classes,
		Search: strings.TrimSpace(r.URL.Query().Get("q"))}
	if d.Search != "" {
		_, key, err := validate.NormalizeClassName(d.Search)
		if err == nil {
			if c, err := s.store.GetClassByName(r.Context(), key); err == nil {
				d.Found, d.HasFound = c, true
			}
		}
		if !d.HasFound {
			d.Error = "未找到该班级；请确认班级名完全一致（支持大小写/全半角规范化）。"
		}
	}
	s.renderClaim(w, r, http.StatusOK, d)
}

func (s *Server) renderClaim(w http.ResponseWriter, r *http.Request, status int, d claimData) {
	s.render(w, r, status, "class_claim", d)
}

func (s *Server) teacherClaimPost(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "teacher", "counselor")
	if !ok {
		return
	}
	id, err := parseID(r, "id")
	if err != nil {
		// The claim form posts class_id, not a path id.
		id, err = strconvParse(r.FormValue("class_id"))
	}
	if err != nil {
		s.error(w, r, http.StatusBadRequest, "认领失败", "请选择要认领的班级。")
		return
	}
	// The claim form must carry the exact class name. This prevents claiming a
	// class by enumerating sequential IDs without going through the search step.
	typedName := strings.TrimSpace(r.FormValue("class_name"))
	class, err := s.store.GetClass(r.Context(), id)
	if err != nil {
		s.error(w, r, http.StatusNotFound, "认领失败", "班级不存在或已被清理，请返回认领页重新搜索。")
		return
	}
	_, wantKey, keyErr := validate.NormalizeClassName(class.Name)
	_, gotKey, gotErr := validate.NormalizeClassName(typedName)
	if keyErr != nil || gotErr != nil || gotKey != wantKey {
		s.error(w, r, http.StatusForbidden, "认领失败",
			"无法认领：提交的班级名与该班级不一致。请返回认领页，按完整班级名精确搜索后再操作。")
		return
	}
	confirmTakeover := r.FormValue("confirm_takeover") == "1"
	confirmSwitch := r.FormValue("confirm_switch") == "1"
	var claimErr error
	if u.Role == "counselor" {
		claimErr = s.store.ClaimCounselorClass(r.Context(), u, id, confirmTakeover)
	} else {
		claimErr = s.store.TakeClass(r.Context(), u, id, confirmTakeover, confirmSwitch)
	}
	if claimErr != nil {
		classes := s.claimableClasses(r, u)
		c, _ := s.store.GetClass(r.Context(), id)
		d := claimData{baseData: s.base(w, r, "class_claim", "认领班级"), Classes: classes,
			Found: c, HasFound: c.ID != 0, Error: claimErr.Error(),
			TakeoverNeeded: errors.Is(claimErr, app.ErrTakeoverNeeded) || errors.Is(claimErr, app.ErrCounselorTakeoverNeeded),
			SwitchNeeded:   errors.Is(claimErr, app.ErrClassSwitchNeeded)}
		status := http.StatusUnprocessableEntity
		if d.TakeoverNeeded || d.SwitchNeeded {
			status = http.StatusConflict
		}
		s.renderClaim(w, r, status, d)
		return
	}
	msg := "班级认领成功。"
	if u.Role == "counselor" {
		msg = "班级认领成功，你已成为该班辅导员。"
	}
	s.redirectFlash(w, r, "/t/classes/"+itoa(id), "ok", msg)
}

type teacherLeaveNewData struct {
	baseData
	Class        app.Class
	Student      app.StudentRow
	ContactPhone string
	Reason       string
	Start        string
	End          string
	Error        string
}

func (s *Server) teacherLeaveNewPage(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "teacher", "counselor")
	if !ok {
		return
	}
	c, st, ok := s.teacherLeaveStudent(w, r, u)
	if !ok {
		return
	}
	b := s.base(w, r, "teacher_leave_new", "代学生请假")
	s.renderPage(w, r, "teacher_leave_new", teacherLeaveNewData{baseData: b, Class: c, Student: st})
}

func (s *Server) teacherLeaveNewSubmit(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "teacher", "counselor")
	if !ok {
		return
	}
	c, st, ok := s.teacherLeaveStudent(w, r, u)
	if !ok {
		return
	}
	d := teacherLeaveNewData{
		baseData: s.base(w, r, "teacher_leave_new", "代学生请假"),
		Class:    c, Student: st,
		ContactPhone: strings.TrimSpace(r.FormValue("contact_phone")),
		Reason:       r.FormValue("reason"),
		Start:        r.FormValue("start_at"),
		End:          r.FormValue("end_at"),
	}
	start, err := parseCSTLocal(d.Start)
	if err != nil {
		d.Error = err.Error()
		s.render(w, r, http.StatusUnprocessableEntity, "teacher_leave_new", d)
		return
	}
	end, err := parseCSTLocal(d.End)
	if err != nil {
		d.Error = err.Error()
		s.render(w, r, http.StatusUnprocessableEntity, "teacher_leave_new", d)
		return
	}
	id, err := s.store.SubmitLeaveForStudent(r.Context(), u, st.UserID, app.SubmitLeaveInput{
		ContactPhone: d.ContactPhone, Reason: d.Reason, StartAt: start, EndAt: end,
	})
	if err != nil {
		d.Error = err.Error()
		s.render(w, r, http.StatusUnprocessableEntity, "teacher_leave_new", d)
		return
	}
	s.redirectFlash(w, r, "/t/leaves/"+itoa(id), "ok", "已代学生提交请假；你可以直接审批，也可以由另一位审批人处理。")
}

func (s *Server) teacherLeaveStudent(w http.ResponseWriter, r *http.Request, u app.User) (app.Class, app.StudentRow, bool) {
	classID, err := parseID(r, "id")
	if err != nil {
		s.error(w, r, http.StatusNotFound, "班级不存在", "链接中的班级编号无效。")
		return app.Class{}, app.StudentRow{}, false
	}
	studentID, err := strconvParse(r.PathValue("student_id"))
	if err != nil {
		s.error(w, r, http.StatusNotFound, "学生不存在", "链接中的学生编号无效。")
		return app.Class{}, app.StudentRow{}, false
	}
	c, err := s.store.GetClass(r.Context(), classID)
	if err != nil {
		s.error(w, r, http.StatusNotFound, "班级不存在", "该班级已被解散或清理。")
		return app.Class{}, app.StudentRow{}, false
	}
	if err := s.store.CanManageClass(r.Context(), u, classID); err != nil {
		s.error(w, r, http.StatusForbidden, "无权代提交", "只有该班当前班主任或辅导员可以代学生提交请假。")
		return app.Class{}, app.StudentRow{}, false
	}
	st, err := s.store.GetStudent(r.Context(), studentID)
	if err != nil || st.ClassID != classID || st.Status != "active" {
		s.error(w, r, http.StatusNotFound, "学生不存在", "该学生不在本班或已停用。")
		return app.Class{}, app.StudentRow{}, false
	}
	return c, st, true
}

func (s *Server) teacherClassDetail(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "teacher", "counselor")
	if !ok {
		return
	}
	id, err := parseID(r, "id")
	if err != nil {
		s.error(w, r, http.StatusNotFound, "班级不存在", "链接中的班级编号无效。")
		return
	}
	c, ok := s.classAccess(w, r, u, id)
	if !ok {
		return
	}
	students, _ := s.store.ClassStudents(r.Context(), id, false, "")
	pending, _ := s.store.ClassLeaves(r.Context(), id, "pending")
	recent, _ := s.store.ClassLeaves(r.Context(), id, "")
	invite, invErr := s.store.PendingInvite(r.Context(), id)
	canImport, reason := importAvailability(u, c)
	b := s.base(w, r, "class_detail", c.Name)
	s.renderPage(w, r, "class_detail", classDetailData{
		baseData: b, Class: c, Students: students, Pending: pending, Recent: recent,
		Invite: invite, HasInvite: invErr == nil, CanManage: true,
		CanImport: canImport, ImportReason: reason,
	})
}

func (s *Server) classAccess(w http.ResponseWriter, r *http.Request, u app.User, id int64) (app.Class, bool) {
	c, err := s.store.GetClass(r.Context(), id)
	if err != nil {
		s.error(w, r, http.StatusNotFound, "班级不存在", "该班级不存在，可能已被解散或满 7 天无人认领被清理；学生记录可在“保留假条”页查看。")
		return c, false
	}
	allowed := (u.Role == "teacher" && c.TeacherUserID == u.ID) ||
		(u.Role == "counselor" && c.CounselorUserID == u.ID) ||
		u.Role == "admin"
	if !allowed {
		s.error(w, r, http.StatusForbidden, "无权访问班级", "无法访问：只有本班当前班主任或辅导员可以管理该班级；如需认领，请前往“认领班级”。")
		return c, false
	}
	return c, true
}

func (s *Server) teacherClassRename(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "teacher", "counselor")
	if !ok {
		return
	}
	id, err := parseID(r, "id")
	if err != nil {
		s.error(w, r, http.StatusNotFound, "班级不存在", "链接中的班级编号无效。")
		return
	}
	if _, ok := s.classAccess(w, r, u, id); !ok {
		return
	}
	version, _ := strconvParse(r.FormValue("version"))
	if err := s.store.RenameClass(r.Context(), u, id, version, r.FormValue("name"), r.FormValue("department")); err != nil {
		s.postFail(w, r, err)
		return
	}
	s.redirectFlash(w, r, "/t/classes/"+itoa(id), "ok", "班级信息已更新。")
}

func (s *Server) teacherClassExit(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "teacher", "counselor")
	if !ok {
		return
	}
	id, err := parseID(r, "id")
	if err != nil {
		s.error(w, r, http.StatusNotFound, "班级不存在", "链接中的班级编号无效。")
		return
	}
	if _, ok := s.classAccess(w, r, u, id); !ok {
		return
	}
	switch u.Role {
	case "teacher":
		err = s.store.ExitTeacher(r.Context(), u)
	case "counselor":
		err = s.store.ExitCounselor(r.Context(), u, id)
	}
	if err != nil {
		s.postFail(w, r, err)
		return
	}
	s.redirectFlash(w, r, "/t/classes", "ok", "已退出班级。班主任退出后班级立即进入待认领，7 天内无班主任将清理班级和学生账号。")
}

func (s *Server) teacherStudents(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "teacher", "counselor")
	if !ok {
		return
	}
	id, err := parseID(r, "id")
	if err != nil {
		s.error(w, r, http.StatusNotFound, "班级不存在", "链接中的班级编号无效。")
		return
	}
	c, ok := s.classAccess(w, r, u, id)
	if !ok {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	students, _ := s.store.ClassStudents(r.Context(), id, false, q)
	canImport, reason := importAvailability(u, c)
	b := s.base(w, r, "class_students", c.Name+" · 学生名单")
	s.renderPage(w, r, "class_students", classStudentsData{
		baseData: b, Class: c, Students: students, Query: q,
		CanImport: canImport, ImportReason: reason,
	})
}

func (s *Server) teacherStudentDorm(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "teacher", "counselor")
	if !ok {
		return
	}
	classID, err := parseID(r, "id")
	if err != nil {
		s.error(w, r, http.StatusNotFound, "班级不存在", "链接中的班级编号无效。")
		return
	}
	studentID, err := strconvParse(r.PathValue("student_id"))
	if err != nil {
		s.error(w, r, http.StatusNotFound, "学生不存在", "链接中的学生编号无效。")
		return
	}
	if _, ok := s.classAccess(w, r, u, classID); !ok {
		return
	}
	st, err := s.store.GetStudent(r.Context(), studentID)
	if err != nil || st.ClassID != classID {
		s.error(w, r, http.StatusForbidden, "无法修改宿舍", "只能修改本班学生的宿舍；请刷新学生名单后重试。")
		return
	}
	if err := s.store.UpdateDorm(r.Context(), u, studentID, r.FormValue("old_dorm"), r.FormValue("dorm"), r.FormValue("reason"), clientIP(r)); err != nil {
		s.postFail(w, r, err)
		return
	}
	s.redirectFlash(w, r, "/t/classes/"+itoa(classID)+"/students", "ok", "宿舍已修改，系统已通知学生和班级审批人。")
}

func (s *Server) teacherStudentName(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "teacher", "counselor")
	if !ok {
		return
	}
	classID, err := parseID(r, "id")
	if err != nil {
		s.error(w, r, http.StatusNotFound, "班级不存在", "链接中的班级编号无效。")
		return
	}
	studentID, err := strconvParse(r.PathValue("student_id"))
	if err != nil {
		s.error(w, r, http.StatusNotFound, "学生不存在", "链接中的学生编号无效。")
		return
	}
	if _, ok := s.classAccess(w, r, u, classID); !ok {
		return
	}
	if err := s.store.UpdateStudentName(r.Context(), u, studentID, r.FormValue("name")); err != nil {
		s.postFail(w, r, err)
		return
	}
	s.redirectFlash(w, r, "/t/classes/"+itoa(classID)+"/students", "ok", "学生姓名已修正。")
}

func (s *Server) teacherImportTemplate(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "teacher", "counselor")
	if !ok {
		return
	}
	id, err := parseID(r, "id")
	if err != nil {
		s.error(w, r, http.StatusNotFound, "班级不存在", "链接中的班级编号无效。")
		return
	}
	if _, ok := s.classAccess(w, r, u, id); !ok {
		return
	}
	data, err := s.store.ImportTemplate(r.Context(), u, id)
	if err != nil {
		s.error(w, r, http.StatusConflict, "无法下载模板", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", "attachment; filename=students-template.xlsx")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

func (s *Server) teacherImportStudents(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "teacher", "counselor")
	if !ok {
		return
	}
	id, err := parseID(r, "id")
	if err != nil {
		s.error(w, r, http.StatusNotFound, "班级不存在", "链接中的班级编号无效。")
		return
	}
	c, ok := s.classAccess(w, r, u, id)
	if !ok {
		return
	}
	fail := func(msg string, status int) {
		students, _ := s.store.ClassStudents(r.Context(), id, false, "")
		canImport, reason := importAvailability(u, c)
		b := s.base(w, r, "class_students", c.Name+" · 学生名单")
		s.render(w, r, status, "class_students", classStudentsData{
			baseData: b, Class: c, Students: students, CanImport: canImport,
			ImportReason: reason, Error: msg,
		})
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		fail("请选择要导入的 .xlsx 文件。", http.StatusUnprocessableEntity)
		return
	}
	defer f.Close()
	if hdr.Size > 5<<20 {
		fail("文件不能超过 5MB；请减少行数后重试。", http.StatusRequestEntityTooLarge)
		return
	}
	if strings.ToLower(filepath.Ext(hdr.Filename)) != ".xlsx" {
		fail("只支持 .xlsx 文件；请使用系统下载的模板。", http.StatusUnprocessableEntity)
		return
	}
	data, err := io.ReadAll(io.LimitReader(f, 5<<20+1))
	if err != nil || len(data) > 5<<20 {
		fail("读取文件失败或文件超过 5MB。", http.StatusRequestEntityTooLarge)
		return
	}
	res, errs, err := s.store.ImportStudents(r.Context(), u, id, hdr.Filename, data, clientIP(r))
	if err != nil {
		s.postFail(w, r, err)
		return
	}
	if len(errs) > 0 {
		students, _ := s.store.ClassStudents(r.Context(), id, false, "")
		canImport, reason := importAvailability(u, c)
		b := s.base(w, r, "class_students", c.Name+" · 学生名单")
		s.render(w, r, http.StatusUnprocessableEntity, "class_students", classStudentsData{
			baseData: b, Class: c, Students: students, CanImport: canImport, ImportReason: reason,
			ImportErrors: errs, Error: "导入未执行：任一错误都会导致全有或全无，请修正后重新上传。",
		})
		return
	}
	s.redirectFlash(w, r, "/t/classes/"+itoa(id)+"/students", "ok",
		"导入成功，共创建 "+itoa(int64(res.Created))+" 名学生；请通过安全渠道分发初始密码。")
}

func (s *Server) teacherImports(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "teacher", "counselor")
	if !ok {
		return
	}
	id, err := parseID(r, "id")
	if err != nil {
		s.error(w, r, http.StatusNotFound, "班级不存在", "链接中的班级编号无效。")
		return
	}
	c, ok := s.classAccess(w, r, u, id)
	if !ok {
		return
	}
	batches, _ := s.store.ListImportBatchesForClass(r.Context(), id)
	b := s.base(w, r, "class_imports", c.Name+" · 导入记录")
	s.renderPage(w, r, "class_imports", teacherImportsData{baseData: b, Class: c, Batches: batches})
}

func (s *Server) teacherCounselor(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "teacher", "counselor")
	if !ok {
		return
	}
	id, err := parseID(r, "id")
	if err != nil {
		s.error(w, r, http.StatusNotFound, "班级不存在", "链接中的班级编号无效。")
		return
	}
	c, ok := s.classAccess(w, r, u, id)
	if !ok {
		return
	}
	invite, invErr := s.store.PendingInvite(r.Context(), id)
	d := counselorPageData{baseData: s.base(w, r, "class_counselor", c.Name+" · 辅导员设置"),
		Class: c, Invite: invite, HasInvite: invErr == nil,
		Search: strings.TrimSpace(r.URL.Query().Get("q"))}
	if d.Search != "" {
		if found, err := s.store.StaffByRole(r.Context(), d.Search, "counselor"); err == nil {
			d.Found, d.HasFound = found, true
		} else {
			d.Error = "找不到该辅导员，请确认对方已用辅导员注册码注册，并按用户名精确搜索。"
		}
	}
	s.renderPage(w, r, "class_counselor", d)
}

func (s *Server) teacherCounselorInvite(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "teacher", "counselor")
	if !ok {
		return
	}
	id, err := parseID(r, "id")
	if err != nil {
		s.error(w, r, http.StatusNotFound, "班级不存在", "链接中的班级编号无效。")
		return
	}
	if _, ok := s.classAccess(w, r, u, id); !ok {
		return
	}
	if _, err := s.store.InviteCounselor(r.Context(), u, id, r.FormValue("username"), r.FormValue("note")); err != nil {
		s.postFail(w, r, err)
		return
	}
	s.redirectFlash(w, r, "/t/classes/"+itoa(id)+"/counselor", "ok", "邀请已发送；对方接受后即成为本班辅导员。")
}

func (s *Server) teacherCounselorCancel(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "teacher", "counselor")
	if !ok {
		return
	}
	id, err := parseID(r, "id")
	if err != nil {
		s.error(w, r, http.StatusNotFound, "班级不存在", "链接中的班级编号无效。")
		return
	}
	if _, ok := s.classAccess(w, r, u, id); !ok {
		return
	}
	if err := s.store.CancelCounselorInvite(r.Context(), u, id); err != nil {
		s.postFail(w, r, err)
		return
	}
	s.redirectFlash(w, r, "/t/classes/"+itoa(id)+"/counselor", "ok", "待处理邀请已取消。")
}

func (s *Server) teacherInvitations(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "counselor")
	if !ok {
		return
	}
	invites, _ := s.store.InvitationsFor(r.Context(), u.ID)
	b := s.base(w, r, "class_invitations", "班级邀请")
	s.renderPage(w, r, "class_invitations", invitationsData{baseData: b, Invitations: invites})
}

func (s *Server) teacherInvitationRespond(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "counselor")
	if !ok {
		return
	}
	id, err := parseID(r, "id")
	if err != nil {
		s.error(w, r, http.StatusNotFound, "邀请不存在", "链接中的邀请编号无效。")
		return
	}
	accept := r.FormValue("accept") == "1"
	if err := s.store.RespondInvitation(r.Context(), u, id, accept); err != nil {
		s.postFail(w, r, err)
		return
	}
	if accept {
		s.redirectFlash(w, r, "/t/classes", "ok", "已接受邀请；如该班原有辅导员，对方已收到顶替通知。")
	} else {
		s.redirectFlash(w, r, "/t/class-invitations", "ok", "已拒绝邀请。")
	}
}

func (s *Server) staffRecoverPage(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "teacher", "counselor", "community")
	if !ok {
		return
	}
	reqs, _ := s.store.PendingResetsForStaff(r.Context(), u)
	base := "/t/recover"
	if strings.HasPrefix(r.URL.Path, "/c/") {
		base = "/c/recover"
	}
	b := s.base(w, r, "staff_recover", "辅助重置审批")
	s.renderPage(w, r, "staff_recover", staffRecoverData{baseData: b, Base: base, Requests: reqs})
}

func (s *Server) staffRecoverVerify(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "teacher", "counselor", "community")
	if !ok {
		return
	}
	base := "/t/recover"
	if strings.HasPrefix(r.URL.Path, "/c/") {
		base = "/c/recover"
	}
	ident := strings.TrimSpace(r.FormValue("ident"))
	code := strings.TrimSpace(r.FormValue("code"))
	q, err := s.store.VerifyResetCode(r.Context(), u, ident, code)
	reqs, _ := s.store.PendingResetsForStaff(r.Context(), u)
	d := staffRecoverData{baseData: s.base(w, r, "staff_recover", "辅助重置审批"), Base: base,
		Requests: reqs, Ident: ident, Code: code}
	if err != nil {
		d.Error = err.Error()
		s.render(w, r, http.StatusUnprocessableEntity, "staff_recover", d)
		return
	}
	d.Found, d.HasFound = q, true
	s.renderPage(w, r, "staff_recover", d)
}

func (s *Server) staffRecoverApprove(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "teacher", "counselor", "community")
	if !ok {
		return
	}
	id, err := parseID(r, "id")
	if err != nil {
		s.error(w, r, http.StatusNotFound, "申请不存在", "链接中的申请编号无效。")
		return
	}
	base := "/t/recover"
	if strings.HasPrefix(r.URL.Path, "/c/") {
		base = "/c/recover"
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
	s.redirectFlash(w, r, base, "ok", "申请已批准；学生需回到申请页面获取 10 分钟一次性重置链接。")
}

func (s *Server) staffRecoverReject(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "teacher", "counselor", "community")
	if !ok {
		return
	}
	id, err := parseID(r, "id")
	if err != nil {
		s.error(w, r, http.StatusNotFound, "申请不存在", "链接中的申请编号无效。")
		return
	}
	base := "/t/recover"
	if strings.HasPrefix(r.URL.Path, "/c/") {
		base = "/c/recover"
	}
	if err := s.store.RejectResetRequest(r.Context(), u, id); err != nil {
		s.postFail(w, r, err)
		return
	}
	s.redirectFlash(w, r, base, "ok", "申请已拒绝。")
}

func (s *Server) teacherLeaveDetail(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "teacher", "counselor")
	if !ok {
		return
	}
	id, err := parseID(r, "id")
	if err != nil {
		s.error(w, r, http.StatusNotFound, "假条不存在", "链接中的假条编号无效。")
		return
	}
	l, err := s.store.GetLeave(r.Context(), id)
	if err != nil {
		s.error(w, r, http.StatusNotFound, "假条不存在", "该假条不存在或已被管理员删除。")
		return
	}
	if l.ArchivedAt != 0 {
		s.redirect(w, r, "/archived/leaves/"+itoa(l.ID))
		return
	}
	if !s.store.CanViewLeave(r.Context(), u, l) && !s.store.CanApprove(r.Context(), u, l) {
		s.error(w, r, http.StatusForbidden, "无权查看假条", "无法查看：你不是该假条所属班级的当前或提交时班主任/辅导员。")
		return
	}
	s.renderTeacherLeave(w, r, http.StatusOK, u, l, "", "")
}

func (s *Server) renderTeacherLeave(w http.ResponseWriter, r *http.Request, status int, u app.User, l app.Leave, proxyURL, msg string) {
	edits, _ := s.store.LeaveTimeEdits(r.Context(), l.ID)
	var list []leaveEdit
	for _, e := range edits {
		list = append(list, convertLeaveEdit(e))
	}
	b := s.base(w, r, "teacher_leave", "请假审批")
	s.render(w, r, status, "teacher_leave", teacherLeaveData{
		baseData: b, Leave: l, Edits: list, ProxyURL: proxyURL,
		CanDecide: s.store.CanApprove(r.Context(), u, l) && l.Status == "pending" && l.DeadlineAt > s.store.NowUnix(),
		Error:     msg, Notice: noticeFor(l),
	})
}

func noticeFor(l app.Leave) string {
	if l.Status == "pending" {
		return "请在截止时间前完成审批；超时将由系统自动驳回。"
	}
	return ""
}

func (s *Server) teacherLeaveDecide(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "teacher", "counselor")
	if !ok {
		return
	}
	id, err := parseID(r, "id")
	if err != nil {
		s.error(w, r, http.StatusNotFound, "假条不存在", "链接中的假条编号无效。")
		return
	}
	l, err := s.store.GetLeave(r.Context(), id)
	if err != nil {
		s.error(w, r, http.StatusNotFound, "假条不存在", "该假条不存在或已被管理员删除。")
		return
	}
	in := app.DecideInput{
		Decision:     r.FormValue("decision"),
		ApproverName: r.FormValue("approver_name"),
		MajorOpinion: r.FormValue("major_opinion"),
		DeptOpinion:  r.FormValue("dept_opinion"),
		RejectReason: r.FormValue("reject_reason"),
		IP:           clientIP(r),
	}
	if err := s.store.DecideLeave(r.Context(), u, id, in); err != nil {
		if !s.store.CanViewLeave(r.Context(), u, l) {
			s.postFail(w, r, err)
			return
		}
		s.renderTeacherLeave(w, r, http.StatusConflict, u, l, "", err.Error())
		return
	}
	s.redirectFlash(w, r, "/t/leaves/"+itoa(id), "ok", "审批已提交；如为通过，二维码已生成。")
}

func (s *Server) teacherLeaveTime(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "teacher", "counselor")
	if !ok {
		return
	}
	id, err := parseID(r, "id")
	if err != nil {
		s.error(w, r, http.StatusNotFound, "假条不存在", "链接中的假条编号无效。")
		return
	}
	start, err := parseCSTLocal(r.FormValue("start_at"))
	if err != nil {
		s.error(w, r, http.StatusUnprocessableEntity, "修改时间失败", err.Error())
		return
	}
	end, err := parseCSTLocal(r.FormValue("end_at"))
	if err != nil {
		s.error(w, r, http.StatusUnprocessableEntity, "修改时间失败", err.Error())
		return
	}
	if err := s.store.EditLeaveTime(r.Context(), u, id, start, end); err != nil {
		s.postFail(w, r, err)
		return
	}
	s.redirectFlash(w, r, "/t/leaves/"+itoa(id), "ok", "请假时间已修改并通知学生。")
}

func (s *Server) teacherLeaveProxy(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "teacher", "counselor")
	if !ok {
		return
	}
	id, err := parseID(r, "id")
	if err != nil {
		s.error(w, r, http.StatusNotFound, "假条不存在", "链接中的假条编号无效。")
		return
	}
	l, err := s.store.GetLeave(r.Context(), id)
	if err != nil {
		s.error(w, r, http.StatusNotFound, "假条不存在", "该假条不存在或已被管理员删除。")
		return
	}
	raw, err := s.store.GenerateProxyLink(r.Context(), u, id, r.FormValue("show_phone") == "1")
	if err != nil {
		s.postFail(w, r, err)
		return
	}
	s.renderTeacherLeave(w, r, http.StatusOK, u, l, s.abs("/a/"+raw),
		"代理链接已生成，仅显示这一次；旧链接已立即失效，链接在审批截止前有效。")
}

func strconvParse(s string) (int64, error) {
	var n int64
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("空值")
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, errors.New("非法数字")
		}
		n = n*10 + int64(r-'0')
		if n > 1<<53 {
			return 0, errors.New("数字过大")
		}
	}
	return n, nil
}
