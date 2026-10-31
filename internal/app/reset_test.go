package app

import (
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"leavepass/internal/secure"
)

var sixDigitRE = regexp.MustCompile(`^[0-9]{6}$`)

func TestStudentResetRequestAndCodeVerification(t *testing.T) {
	s, clock, admin := newTestStore(t)
	teacher := addStaff(t, s, "teacher", "RT1", "13980000001")
	counselor := addStaff(t, s, "counselor", "RC1", "13980000002")
	c := makeClass(t, s, admin, teacher, counselor, "重置测试1班")
	student := addStudent(t, s, "重置学生甲", "20242001", c.ID, "21-801")
	now := clock.Unix()

	q, code, browser, err := s.CreateStudentResetRequest(ctx(), "20242001", "重置学生甲", "198.51.100.1")
	if err != nil {
		t.Fatalf("CreateStudentResetRequest: %v", err)
	}
	if !sixDigitRE.MatchString(code) {
		t.Fatalf("申请码格式 = %q", code)
	}
	if browser == "" || len(browser) < 20 {
		t.Fatalf("browser token 异常: %q", browser)
	}
	if q.ID == 0 || q.UserID != student.ID || q.Status != "pending" || q.ClassID != c.ID ||
		q.TeacherID != teacher.ID || q.CounselorID != counselor.ID {
		t.Fatalf("申请单 = %+v", q)
	}
	if q.RequestedAt != now || q.ExpiresAt != now+86400 {
		t.Fatalf("申请时间 = %d/%d", q.RequestedAt, q.ExpiresAt)
	}
	pending, err := s.PendingResetForUser(ctx(), student.ID)
	if err != nil || pending.ID != q.ID {
		t.Fatalf("PendingResetForUser = %+v err=%v", pending, err)
	}
	if _, err := s.GetResetRequestByBrowser(ctx(), browser); err != nil {
		t.Fatalf("GetResetRequestByBrowser: %v", err)
	}

	got, err := s.VerifyResetCode(ctx(), teacher, "20242001", code)
	if err != nil {
		t.Fatalf("VerifyResetCode: %v", err)
	}
	if got.ID != q.ID {
		t.Fatalf("VerifyResetCode 返回 = %+v", got)
	}
	// 非审批人即使拿到正确申请码也不能通过。
	if _, err := s.VerifyResetCode(ctx(), addStaff(t, s, "community", "RM1", "13980000003"), "20242001", code); err == nil ||
		!strings.Contains(err.Error(), "无权批准") {
		t.Fatalf("无权批准错误 = %v", err)
	}

	// 新申请会让旧 pending 失效。
	q2, code2, _, err := s.CreateStudentResetRequest(ctx(), "20242001", "重置学生甲", "198.51.100.2")
	if err != nil {
		t.Fatalf("第二次申请: %v", err)
	}
	if q2.ID == q.ID {
		t.Fatal("应创建新申请单")
	}
	old, err := s.GetResetRequest(ctx(), q.ID)
	if err != nil || old.Status != "expired" {
		t.Fatalf("旧申请状态 = %+v err=%v", old, err)
	}

	// 连续 5 次错误申请码后申请作废，正确码也不能再用。
	student2 := addStudent(t, s, "重置学生乙", "20242002", c.ID, "21-802")
	_, code3, _, err := s.CreateStudentResetRequest(ctx(), "20242002", "重置学生乙", "198.51.100.3")
	if err != nil {
		t.Fatalf("学生乙申请: %v", err)
	}
	wrong := "000000"
	if code3 == wrong {
		wrong = "000001"
	}
	for i := 0; i < 5; i++ {
		if _, err := s.VerifyResetCode(ctx(), teacher, "20242002", wrong); err == nil {
			t.Fatalf("第 %d 次错误码应失败", i+1)
		}
	}
	reqB, _ := s.PendingResetForUser(ctx(), student2.ID)
	// 第 5 次错误后 status 已变 expired，pending 查询不到。
	if reqB.ID == 0 {
		var status string
		var attempts int
		_ = s.DB.QueryRow(`SELECT status, attempts FROM password_reset_requests WHERE user_id=? ORDER BY id DESC LIMIT 1`, student2.ID).
			Scan(&status, &attempts)
		if status != "expired" || attempts != 5 {
			t.Fatalf("错误次数后状态 = %s/%d", status, attempts)
		}
	}
	if _, err := s.VerifyResetCode(ctx(), teacher, "20242002", code3); err == nil {
		t.Fatal("申请作废后正确码也不应通过")
	}
	_ = code2
}

func TestResetFailureLadder(t *testing.T) {
	s, clock, _ := newTestStore(t)
	teacher := addStaff(t, s, "teacher", "RT2", "13980000011")
	const ip = "198.51.100.11"
	key := s.resetKey(teacher.ID, teacher.UsernameNorm, ip)
	now := clock.Unix()

	for i := 0; i < 3; i++ {
		if err := s.VerifyStaffPhone(ctx(), teacher, "13999999999", ip); err == nil ||
			!strings.Contains(err.Error(), "信息不匹配") {
			t.Fatalf("第 %d 次错误手机号 = %v", i+1, err)
		}
	}
	err := s.VerifyStaffPhone(ctx(), teacher, "13999999999", ip)
	if err == nil || !strings.Contains(err.Error(), "10 分钟") {
		t.Fatalf("第 4 次失败应锁 10 分钟，实际 %v", err)
	}
	if until, _ := s.ResetLocked(ctx(), key); until != now+600 {
		t.Fatalf("锁定至 = %d，期望 %d", until, now+600)
	}

	clock.Advance(601 * time.Second)
	if err := s.VerifyStaffPhone(ctx(), teacher, "13999999999", ip); err == nil ||
		!strings.Contains(err.Error(), "信息不匹配") {
		t.Fatalf("解锁后第 1 次失败 = %v", err)
	}
	if until, _ := s.ResetLocked(ctx(), key); until != 0 {
		t.Fatalf("第 1 次失败不应立即锁定: %d", until)
	}
	err = s.VerifyStaffPhone(ctx(), teacher, "13999999999", ip)
	if err == nil || !strings.Contains(err.Error(), "30 分钟") {
		t.Fatalf("第二阶段第 2 次失败应锁 30 分钟，实际 %v", err)
	}
	if until, _ := s.ResetLocked(ctx(), key); until != clock.Unix()+1800 {
		t.Fatalf("第二阶段锁定至 = %d，期望 %d", until, clock.Unix()+1800)
	}

	// 正确手机号在锁定期内仍被拒绝。
	if err := s.VerifyStaffPhone(ctx(), teacher, teacher.Phone, ip); err == nil ||
		!strings.Contains(err.Error(), "30 分钟") {
		t.Fatalf("锁定期内正确手机号 = %v", err)
	}
	// 锁定到期后正确手机号通过并清除计数。
	clock.Advance(1801 * time.Second)
	if err := s.VerifyStaffPhone(ctx(), teacher, teacher.Phone, ip); err != nil {
		t.Fatalf("解锁后正确手机号: %v", err)
	}
	if until, _ := s.ResetLocked(ctx(), key); until != 0 {
		t.Fatalf("成功后锁定残留: %d", until)
	}
}

func TestApproveResetPermissionMatrix(t *testing.T) {
	s, _, admin1 := newTestStore(t)
	teacherA := addStaff(t, s, "teacher", "RA1", "13980000021")
	counselorA := addStaff(t, s, "counselor", "RA2", "13980000022")
	teacherB := addStaff(t, s, "teacher", "RA3", "13980000023")
	community := addStaff(t, s, "community", "RA4", "13980000024")
	classA := makeClass(t, s, admin1, teacherA, counselorA, "重置权限A班")
	classB := makeClass(t, s, admin1, teacherB, User{}, "重置权限B班")
	studentA := addStudent(t, s, "权限学生甲", "20242101", classA.ID, "21-901")
	addStudent(t, s, "权限学生乙", "20242102", classB.ID, "21-902")
	addStudent(t, s, "无班学生", "20242103", 0, "")
	admin2 := addAdmin(t, s, admin1, "Admin2", "13980000025")

	newStudentReq := func(t *testing.T, no, name, ip string) (ResetRequest, string) {
		t.Helper()
		q, code, _, err := s.CreateStudentResetRequest(ctx(), no, name, ip)
		if err != nil {
			t.Fatalf("CreateStudentResetRequest(%s): %v", no, err)
		}
		return q, code
	}

	qA, codeA := newStudentReq(t, "20242101", "权限学生甲", "198.51.100.21")
	if err := s.ApproveResetRequest(ctx(), studentA, qA.ID, codeA, ""); err == nil ||
		!strings.Contains(err.Error(), "无权批准") {
		t.Fatalf("本人批准错误 = %v", err)
	}
	if err := s.ApproveResetRequest(ctx(), teacherB, qA.ID, codeA, ""); err == nil ||
		!strings.Contains(err.Error(), "无权批准") {
		t.Fatalf("其他班班主任批准错误 = %v", err)
	}
	if err := s.ApproveResetRequest(ctx(), admin1, qA.ID, codeA, ""); err == nil ||
		!strings.Contains(err.Error(), "无权批准") {
		t.Fatalf("管理员批准有审批人学生错误 = %v", err)
	}
	if err := s.ApproveResetRequest(ctx(), community, qA.ID, codeA, ""); err == nil ||
		!strings.Contains(err.Error(), "无权批准") {
		t.Fatalf("社区老师批准错误 = %v", err)
	}
	if err := s.ApproveResetRequest(ctx(), counselorA, qA.ID, codeA, ""); err != nil {
		t.Fatalf("本班辅导员应可批准: %v", err)
	}

	// 本班班主任也可以批准。
	qA2, codeA2 := newStudentReq(t, "20242101", "权限学生甲", "198.51.100.22")
	if err := s.ApproveResetRequest(ctx(), teacherA, qA2.ID, codeA2, ""); err != nil {
		t.Fatalf("本班班主任应可批准: %v", err)
	}
	// B 班班主任批准本班学生。
	qB, codeB := newStudentReq(t, "20242102", "权限学生乙", "198.51.100.23")
	if err := s.ApproveResetRequest(ctx(), teacherB, qB.ID, codeB, ""); err != nil {
		t.Fatalf("B 班班主任应可批准: %v", err)
	}
	// 无班主任/辅导员的学生由管理员批准。
	qNo, codeNo := newStudentReq(t, "20242103", "无班学生", "198.51.100.24")
	if err := s.ApproveResetRequest(ctx(), admin1, qNo.ID, codeNo, ""); err != nil {
		t.Fatalf("管理员应可批准无审批人学生: %v", err)
	}
	// 管理员可批准另一位管理员的申请。
	qAdmin, codeAdmin, _, err := s.CreateStaffResetRequest(ctx(), "Admin2", admin2.Phone, "198.51.100.25")
	if err != nil {
		t.Fatalf("管理员重置申请: %v", err)
	}
	if err := s.ApproveResetRequest(ctx(), admin2, qAdmin.ID, codeAdmin, ""); err == nil {
		t.Fatal("本人不能批准自己的管理员重置申请")
	}
	if err := s.ApproveResetRequest(ctx(), admin1, qAdmin.ID, codeAdmin, ""); err != nil {
		t.Fatalf("另一管理员应可批准: %v", err)
	}
	// 管理员不能批准有本班审批人的学生申请（已在上方断言）。

	// PendingResetsForStaff 只列出当前审批人有权处理的申请。
	studentC := addStudent(t, s, "权限学生丙", "20242104", classA.ID, "21-903")
	qC, _ := newStudentReq(t, "20242104", "权限学生丙", "198.51.100.26")
	list, err := s.PendingResetsForStaff(ctx(), teacherA)
	if err != nil {
		t.Fatalf("PendingResetsForStaff: %v", err)
	}
	found := false
	for _, q := range list {
		if q.ID == qC.ID && q.UserID == studentC.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("本班班主任的待批列表缺少申请: %+v", list)
	}
	if _, err := s.PendingResetsForStaff(ctx(), admin1); err != nil {
		t.Fatalf("管理员待批列表: %v", err)
	}
}

func TestResetTokenLifecycleAndSessionInvalidation(t *testing.T) {
	s, clock, admin := newTestStore(t)
	teacher := addStaff(t, s, "teacher", "RT3", "13980000031")
	counselor := addStaff(t, s, "counselor", "RC3", "13980000032")
	c := makeClass(t, s, admin, teacher, counselor, "重置令牌班")
	student := addStudent(t, s, "令牌学生甲", "20242201", c.ID, "21-1001")
	addStudent(t, s, "令牌学生乙", "20242202", c.ID, "21-1002")
	now := clock.Unix()

	// 未批准的申请不能领取重置 token。
	q, code, browser, err := s.CreateStudentResetRequest(ctx(), "20242201", "令牌学生甲", "198.51.100.31")
	if err != nil {
		t.Fatalf("学生甲申请: %v", err)
	}
	if _, err := s.IssueResetTokenForBrowser(ctx(), browser, "198.51.100.31"); err == nil {
		t.Fatal("未批准申请不应发放 token")
	}

	// 先建立一个登录会话，重置成功后必须失效。
	sessionRaw, err := s.CreateSession(ctx(), student.ID, "198.51.100.31", "test-agent")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := s.LookupSession(ctx(), sessionRaw); err != nil {
		t.Fatalf("重置前会话应有效: %v", err)
	}

	if err := s.ApproveResetRequest(ctx(), counselor, q.ID, code, ""); err != nil {
		t.Fatalf("辅导员批准: %v", err)
	}
	raw, err := s.IssueResetTokenForBrowser(ctx(), browser, "198.51.100.31")
	if err != nil {
		t.Fatalf("IssueResetTokenForBrowser: %v", err)
	}
	if raw == "" {
		t.Fatal("重置 token 为空")
	}
	if _, err := s.IssueResetTokenForBrowser(ctx(), browser, "198.51.100.31"); err == nil {
		t.Fatal("同一申请不应重复发放 token")
	}
	u, expires, err := s.ResetTokenUser(ctx(), raw)
	if err != nil || u.ID != student.ID || expires != now+600 {
		t.Fatalf("ResetTokenUser = %+v expires=%d err=%v", u, expires, err)
	}

	if _, err := s.ResetPasswordWithToken(ctx(), raw, "NewPassw0rd!", "198.51.100.31"); err != nil {
		t.Fatalf("ResetPasswordWithToken: %v", err)
	}
	updated, err := s.GetUserByID(ctx(), student.ID)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if secure.CheckPassword(updated.PasswordHash, testPassword) {
		t.Fatal("旧密码仍然有效")
	}
	if !secure.CheckPassword(updated.PasswordHash, "NewPassw0rd!") {
		t.Fatal("新密码未生效")
	}
	if _, err := s.LookupSession(ctx(), sessionRaw); !errors.Is(err, ErrNotFound) {
		t.Fatalf("重置后旧 session 应失效: %v", err)
	}
	if _, err := s.ResetPasswordWithToken(ctx(), raw, "AnotherPass1", "198.51.100.31"); err == nil {
		t.Fatal("重置 token 只能使用一次")
	}

	// 10 分钟过期边界。
	q2, code2, browser2, err := s.CreateStudentResetRequest(ctx(), "20242202", "令牌学生乙", "198.51.100.32")
	if err != nil {
		t.Fatalf("学生乙申请: %v", err)
	}
	if err := s.ApproveResetRequest(ctx(), teacher, q2.ID, code2, ""); err != nil {
		t.Fatalf("班主任批准: %v", err)
	}
	raw2, err := s.IssueResetTokenForBrowser(ctx(), browser2, "198.51.100.32")
	if err != nil {
		t.Fatalf("学生乙 token: %v", err)
	}
	clock.Advance(601 * time.Second)
	if _, err := s.ResetPasswordWithToken(ctx(), raw2, "NewPassw0rd!", "198.51.100.32"); err == nil ||
		!strings.Contains(err.Error(), "过期") {
		t.Fatalf("过期 token 错误 = %v", err)
	}
}

func TestChangePasswordDailyLimit(t *testing.T) {
	s, clock, admin := newTestStore(t)
	teacher := addStaff(t, s, "teacher", "RT4", "13980000041")
	c := makeClass(t, s, admin, teacher, User{}, "修改密码班")
	student := addStudent(t, s, "改密学生", "20242301", c.ID, "21-1101")

	// 先推进到周一，避免从周日跨到周一时每周窗口重置，无法验证 3 次/周。
	clock.Advance(24 * time.Hour)

	cur := student
	old := testPassword
	for i := 0; i < 2; i++ {
		newPass := "Changed" + string(rune('1'+i)) + "!"
		if err := s.ChangePassword(ctx(), cur, old, newPass, "198.51.100.41"); err != nil {
			t.Fatalf("第 %d 次改密: %v", i+1, err)
		}
		cur, _ = s.GetUserByID(ctx(), student.ID)
		old = newPass
	}
	if err := s.ChangePassword(ctx(), cur, old, "Changed3!", "198.51.100.41"); err == nil ||
		!strings.Contains(err.Error(), "2 次") {
		t.Fatalf("每日第 3 次改密错误 = %v", err)
	}

	// 次日可再改一次，但本周已达 3 次上限。
	clock.Advance(24 * time.Hour)
	cur, _ = s.GetUserByID(ctx(), student.ID)
	if err := s.ChangePassword(ctx(), cur, old, "Changed4!", "198.51.100.41"); err != nil {
		t.Fatalf("次日改密: %v", err)
	}
	cur, _ = s.GetUserByID(ctx(), student.ID)
	if err := s.ChangePassword(ctx(), cur, "Changed4!", "Changed5!", "198.51.100.41"); err == nil ||
		!strings.Contains(err.Error(), "3 次") {
		t.Fatalf("每周第 4 次改密错误 = %v", err)
	}
}
