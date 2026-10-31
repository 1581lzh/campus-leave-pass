package app

import (
	"errors"
	"strings"
	"testing"

	"leavepass/internal/secure"
)

func resetAttempts(t *testing.T, s *Store, requestID int64) int {
	t.Helper()
	var attempts int
	if err := s.DB.QueryRow(`SELECT attempts FROM password_reset_requests WHERE id=?`, requestID).Scan(&attempts); err != nil {
		t.Fatalf("读取申请尝试次数: %v", err)
	}
	return attempts
}

// TestApproveResetRequestRequiresMatchingCodeAndBrowser covers the security
// fix that an approver must present the applicant's 6-digit code and cannot
// approve a request created in the same browser session.
func TestApproveResetRequestRequiresMatchingCodeAndBrowser(t *testing.T) {
	s, _, admin := newTestStore(t)
	teacher := addStaff(t, s, "teacher", "RS1", "13982000001")
	counselor := addStaff(t, s, "counselor", "RS2", "13982000002")
	c := makeClass(t, s, admin, teacher, counselor, "重置回归班")

	addStudent(t, s, "回归学生甲", "20245001", c.ID, "21-1201")
	addStudent(t, s, "回归学生乙", "20245002", c.ID, "21-1202")
	addStudent(t, s, "回归学生丙", "20245003", c.ID, "21-1203")
	addStudent(t, s, "回归学生丁", "20245004", c.ID, "21-1204")

	newReq := func(t *testing.T, no, name, ip string) (ResetRequest, string, string) {
		t.Helper()
		q, code, browser, err := s.CreateStudentResetRequest(ctx(), no, name, ip)
		if err != nil {
			t.Fatalf("CreateStudentResetRequest(%s): %v", no, err)
		}
		return q, code, browser
	}

	t.Run("正确申请码可批准", func(t *testing.T) {
		q, code, _ := newReq(t, "20245001", "回归学生甲", "198.51.100.51")
		if err := s.ApproveResetRequest(ctx(), teacher, q.ID, code, "another-browser"); err != nil {
			t.Fatalf("正确 code 应可批准: %v", err)
		}
		got, err := s.GetResetRequest(ctx(), q.ID)
		if err != nil || got.Status != "approved" || got.ApprovedBy != teacher.ID {
			t.Fatalf("批准后申请 = %+v err=%v", got, err)
		}
	})

	t.Run("错误申请码拒绝并增加尝试次数", func(t *testing.T) {
		q, code, _ := newReq(t, "20245002", "回归学生乙", "198.51.100.52")
		wrong := "000000"
		if code == wrong {
			wrong = "000001"
		}
		err := s.ApproveResetRequest(ctx(), counselor, q.ID, wrong, "")
		if err == nil || !strings.Contains(err.Error(), "申请码无效") {
			t.Fatalf("错误 code 错误 = %v", err)
		}
		if got := resetAttempts(t, s, q.ID); got != 1 {
			t.Fatalf("错误 code 后 attempts = %d，期望 1", got)
		}
		got, _ := s.GetResetRequest(ctx(), q.ID)
		if got.Status != "pending" {
			t.Fatalf("错误 code 后状态 = %s，期望 pending", got.Status)
		}
		// 之后用正确 code 仍可批准。
		if err := s.ApproveResetRequest(ctx(), counselor, q.ID, code, ""); err != nil {
			t.Fatalf("错误一次后用正确 code 应可批准: %v", err)
		}
	})

	t.Run("空申请码拒绝", func(t *testing.T) {
		q, code, _ := newReq(t, "20245003", "回归学生丙", "198.51.100.53")
		err := s.ApproveResetRequest(ctx(), teacher, q.ID, "", "")
		if err == nil || !strings.Contains(err.Error(), "申请码无效") {
			t.Fatalf("空 code 错误 = %v", err)
		}
		if got := resetAttempts(t, s, q.ID); got != 0 {
			t.Fatalf("空 code 后 attempts = %d，期望 0", got)
		}
		if err := s.ApproveResetRequest(ctx(), teacher, q.ID, code, ""); err != nil {
			t.Fatalf("空 code 被拒后正确 code 应可批准: %v", err)
		}
	})

	t.Run("同一浏览器不能批准自己的申请", func(t *testing.T) {
		q, code, browser := newReq(t, "20245004", "回归学生丁", "198.51.100.54")
		err := s.ApproveResetRequest(ctx(), counselor, q.ID, code, browser)
		if err == nil || !strings.Contains(err.Error(), "本浏览器") {
			t.Fatalf("同浏览器批准错误 = %v", err)
		}
		if got := resetAttempts(t, s, q.ID); got != 0 {
			t.Fatalf("同浏览器拒绝不应消耗申请码尝试: %d", got)
		}
		got, _ := s.GetResetRequest(ctx(), q.ID)
		if got.Status != "pending" {
			t.Fatalf("同浏览器拒绝后状态 = %s", got.Status)
		}
		// 换一个浏览器（browserToken 不同）即可批准。
		if err := s.ApproveResetRequest(ctx(), counselor, q.ID, code, "different-browser-token"); err != nil {
			t.Fatalf("不同浏览器应可批准: %v", err)
		}
	})
}

func TestDisbandClassAllowsStudentTransferAndDormSetup(t *testing.T) {
	s, _, admin := newTestStore(t)
	teacher := addStaff(t, s, "teacher", "DS1", "13982000011")
	counselor := addStaff(t, s, "counselor", "DS2", "13982000012")
	teacher2 := addStaff(t, s, "teacher", "DS3", "13982000013")
	counselor2 := addStaff(t, s, "counselor", "DS4", "13982000014")

	oldClass := makeClass(t, s, admin, teacher, counselor, "解散原班")
	newClass := makeClass(t, s, admin, teacher2, counselor2, "转入目标班")
	student := addStudent(t, s, "转班学生", "20245101", oldClass.ID, "21-1301")

	if err := s.DisbandClass(ctx(), admin, oldClass.ID, "127.0.0.1"); err != nil {
		t.Fatalf("DisbandClass: %v", err)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM student_classes WHERE user_id=?`, student.ID); n != 0 {
		t.Fatalf("解散后 student_classes 行 = %d，期望 0", n)
	}
	if _, err := s.GetClass(ctx(), oldClass.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("解散后班级仍存在: %v", err)
	}

	if err := s.AdjustStudentClass(ctx(), admin, student.ID, newClass.ID, "127.0.0.1"); err != nil {
		t.Fatalf("AdjustStudentClass: %v", err)
	}
	var classID int64
	var building, room string
	if err := s.DB.QueryRow(`SELECT class_id, dorm_building, dorm_room FROM student_classes WHERE user_id=?`, student.ID).
		Scan(&classID, &building, &room); err != nil {
		t.Fatalf("读取转班关系: %v", err)
	}
	if classID != newClass.ID || building != "" || room != "" {
		t.Fatalf("转班关系 = class %d dorm %q-%q", classID, building, room)
	}

	if err := s.UpdateDorm(ctx(), admin, student.ID, "", "22-701", "转班后补录宿舍", "127.0.0.1"); err != nil {
		t.Fatalf("转班后 UpdateDorm(old=\"\"): %v", err)
	}
	st, err := s.GetStudent(ctx(), student.ID)
	if err != nil {
		t.Fatalf("GetStudent: %v", err)
	}
	if st.ClassID != newClass.ID || st.DormBuilding != "22" || st.DormRoom != "701" {
		t.Fatalf("补录宿舍后学生 = %+v", st)
	}
}

func TestDisbandClassKeepsApprovedQRDeletesPendingQR(t *testing.T) {
	s, clock, admin := newTestStore(t)
	teacher := addStaff(t, s, "teacher", "DQ1", "13982000021")
	counselor := addStaff(t, s, "counselor", "DQ2", "13982000022")
	c := makeClass(t, s, admin, teacher, counselor, "二维码解散班")
	student := addStudent(t, s, "二维码学生", "20245201", c.ID, "21-1401")
	now := clock.Unix()

	approvedID := submitTestLeave(t, s, student, now+3600, now+7200, "已批准请假原因")
	if err := s.DecideLeave(ctx(), teacher, approvedID, DecideInput{Decision: "approved", ApproverName: "王老师"}); err != nil {
		t.Fatalf("批准假条: %v", err)
	}
	approvedToken := s.QRToken(approvedID)
	if n := countRows(t, s, `SELECT COUNT(*) FROM qr_tokens WHERE leave_id=?`, approvedID); n != 1 {
		t.Fatalf("批准后二维码行 = %d", n)
	}

	pendingID := submitTestLeave(t, s, student, now+10800, now+14400, "待审批请假原因")
	if _, err := s.DB.Exec(`INSERT INTO qr_tokens (leave_id,token_hash,created_at) VALUES (?,?,?)`,
		pendingID, "sha256-pending-token-for-test", now); err != nil {
		t.Fatalf("插入 pending 二维码: %v", err)
	}

	if err := s.DisbandClass(ctx(), admin, c.ID, "127.0.0.1"); err != nil {
		t.Fatalf("DisbandClass: %v", err)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM qr_tokens WHERE leave_id=?`, approvedID); n != 1 {
		t.Fatalf("解散后已批准假条二维码行 = %d，期望保留", n)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM qr_tokens WHERE leave_id=?`, pendingID); n != 0 {
		t.Fatalf("解散后待审批假条二维码行 = %d，期望删除", n)
	}
	if got := mustGetLeave(t, s, approvedID); got.Status != "approved" {
		t.Fatalf("已批准假条状态 = %s", got.Status)
	}
	if got := mustGetLeave(t, s, pendingID); got.Status != "cancelled" || got.Decision != "class_disbanded" {
		t.Fatalf("待审批假条状态 = %s/%s", got.Status, got.Decision)
	}

	// 已批准假条在结束前仍可凭二维码查看门卫页。
	gate, err := s.GetGatePass(ctx(), approvedToken)
	if err != nil {
		t.Fatalf("解散后已批准二维码门卫页: %v", err)
	}
	if gate.Leave.ID != approvedID {
		t.Fatalf("门卫页假条 = %d", gate.Leave.ID)
	}
}

func TestUpdateDormOldValueCAS(t *testing.T) {
	s, _, admin := newTestStore(t)
	teacher := addStaff(t, s, "teacher", "DC1", "13982000031")
	counselor := addStaff(t, s, "counselor", "DC2", "13982000032")
	c := makeClass(t, s, admin, teacher, counselor, "宿舍CAS班")
	student := addStudent(t, s, "CAS学生", "20245301", c.ID, "21-1501")

	if err := s.UpdateDorm(ctx(), admin, student.ID, "21-1501", "21-1502", "第一次修改", "127.0.0.1"); err != nil {
		t.Fatalf("第一次 UpdateDorm: %v", err)
	}
	err := s.UpdateDorm(ctx(), admin, student.ID, "21-1501", "21-1503", "使用过期旧值", "127.0.0.1")
	if !errors.Is(err, ErrDormConflict) {
		t.Fatalf("第二次 UpdateDorm 错误 = %v，期望 ErrDormConflict", err)
	}
	st, err := s.GetStudent(ctx(), student.ID)
	if err != nil {
		t.Fatalf("GetStudent: %v", err)
	}
	if st.DormBuilding != "21" || st.DormRoom != "1502" {
		t.Fatalf("CAS 冲突后宿舍 = %q-%q，期望 21-1502", st.DormBuilding, st.DormRoom)
	}
}

// TestVerifyResetCodeChecksPermissionBeforeConsumingAttempts covers the
// assisted-reset DoS fix: an unrelated staff member must not be able to
// expire another class's pending request with wrong guesses.
func TestVerifyResetCodeChecksPermissionBeforeConsumingAttempts(t *testing.T) {
	s, _, admin := newTestStore(t)
	teacherA := addStaff(t, s, "teacher", "RPA1", "13983000001")
	counselorA := addStaff(t, s, "counselor", "RPA2", "13983000002")
	classA := makeClass(t, s, admin, teacherA, counselorA, "重置权限班")
	addStudent(t, s, "重置权限学生", "20247001", classA.ID, "22-101")
	teacherB := addStaff(t, s, "teacher", "RPB1", "13983000003")

	q, code, _, err := s.CreateStudentResetRequest(ctx(), "20247001", "重置权限学生", "198.51.100.71")
	if err != nil {
		t.Fatalf("CreateStudentResetRequest: %v", err)
	}
	wrong := "000000"
	if code == wrong {
		wrong = "000001"
	}
	for i := 0; i < 5; i++ {
		if _, err := s.VerifyResetCode(ctx(), teacherB, "20247001", wrong); err == nil {
			t.Fatalf("第 %d 次越权校验应失败", i+1)
		}
	}
	got, err := s.GetResetRequest(ctx(), q.ID)
	if err != nil {
		t.Fatalf("GetResetRequest: %v", err)
	}
	if got.Status != "pending" || resetAttempts(t, s, q.ID) != 0 {
		t.Fatalf("越权尝试不应消耗申请: status=%s attempts=%d", got.Status, resetAttempts(t, s, q.ID))
	}
	if _, err := s.VerifyResetCode(ctx(), teacherA, "20247001", code); err != nil {
		t.Fatalf("本班班主任仍应能校验正确申请码: %v", err)
	}
}

// TestDeleteOtherUserSessionsKeepsCurrent covers the per-account "sign out
// other devices" action: it keeps the current session and never touches
// another user's sessions.
func TestDeleteOtherUserSessionsKeepsCurrent(t *testing.T) {
	s, _, admin := newTestStore(t)
	teacher := addStaff(t, s, "teacher", "RVS1", "13984000001")
	raw1, err := s.CreateSession(ctx(), admin.ID, "198.51.100.81", "ua-1")
	if err != nil {
		t.Fatalf("CreateSession 1: %v", err)
	}
	raw2, err := s.CreateSession(ctx(), admin.ID, "198.51.100.82", "ua-2")
	if err != nil {
		t.Fatalf("CreateSession 2: %v", err)
	}
	rawTeacher, err := s.CreateSession(ctx(), teacher.ID, "198.51.100.83", "ua-3")
	if err != nil {
		t.Fatalf("CreateSession teacher: %v", err)
	}
	if n, err := s.CountUserSessions(ctx(), admin.ID); err != nil || n != 2 {
		t.Fatalf("CountUserSessions(admin) = %d, %v; want 2", n, err)
	}
	n, err := s.DeleteOtherUserSessions(ctx(), admin.ID, secure.SHA256Hex(raw1))
	if err != nil || n != 1 {
		t.Fatalf("DeleteOtherUserSessions = %d, %v; want 1", n, err)
	}
	if _, err := s.LookupSession(ctx(), raw1); err != nil {
		t.Fatalf("当前会话不应失效: %v", err)
	}
	if _, err := s.LookupSession(ctx(), raw2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("其他设备会话应失效: %v", err)
	}
	if _, err := s.LookupSession(ctx(), rawTeacher); err != nil {
		t.Fatalf("其他用户会话不应受影响: %v", err)
	}
	if n, err := s.CountUserSessions(ctx(), admin.ID); err != nil || n != 1 {
		t.Fatalf("CountUserSessions(admin) after revoke = %d, %v; want 1", n, err)
	}
}

// TestRunTTLCleansOldLoginAttempts keeps the unauthenticated failure table
// bounded; the index added in schema v3 backs this query.
func TestRunTTLCleansOldLoginAttempts(t *testing.T) {
	s, clock, _ := newTestStore(t)
	old := clock.Unix() - 8*86400
	recent := clock.Unix() - 86400
	if _, err := s.DB.Exec(`INSERT INTO login_attempts (key,scope,window_start,failed_count,locked_until,updated_at)
		VALUES (?,?,?,?,?,?)`, "old-key", "login_ip", old, 1, nil, old); err != nil {
		t.Fatalf("insert old: %v", err)
	}
	if _, err := s.DB.Exec(`INSERT INTO login_attempts (key,scope,window_start,failed_count,locked_until,updated_at)
		VALUES (?,?,?,?,?,?)`, "recent-key", "login_ip", recent, 1, nil, recent); err != nil {
		t.Fatalf("insert recent: %v", err)
	}
	s.RunTTL(ctx())
	var oldCount, recentCount int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM login_attempts WHERE key='old-key'`).Scan(&oldCount); err != nil {
		t.Fatalf("count old: %v", err)
	}
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM login_attempts WHERE key='recent-key'`).Scan(&recentCount); err != nil {
		t.Fatalf("count recent: %v", err)
	}
	if oldCount != 0 || recentCount != 1 {
		t.Fatalf("TTL 清理结果 old=%d recent=%d; want 0/1", oldCount, recentCount)
	}
}
