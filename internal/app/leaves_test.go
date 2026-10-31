package app

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func submitTestLeave(t *testing.T, s *Store, student User, start, end int64, reason string) int64 {
	t.Helper()
	id, err := s.SubmitLeave(ctx(), student, SubmitLeaveInput{
		ContactPhone: "13800000001",
		Reason:       reason,
		StartAt:      start,
		EndAt:        end,
	})
	if err != nil {
		t.Fatalf("SubmitLeave: %v", err)
	}
	return id
}

func TestSubmitLeaveSnapshotAndDeadline(t *testing.T) {
	s, clock, admin := newTestStore(t)
	teacher := addStaff(t, s, "teacher", "LT1", "13970000001")
	counselor := addStaff(t, s, "counselor", "LC1", "13970000002")
	c := makeClass(t, s, admin, teacher, counselor, "请假测试1班")
	student := addStudent(t, s, "学生甲", "20241001", c.ID, "21-601")

	now := clock.Unix()
	id := submitTestLeave(t, s, student, now+3600, now+7200, "回家处理事务")
	l := mustGetLeave(t, s, id)
	if l.Status != "pending" {
		t.Fatalf("状态 = %s", l.Status)
	}
	if l.DeadlineAt != now+LeaveDeadlineTTL {
		t.Fatalf("deadline = %d，期望提交后 24 小时 %d", l.DeadlineAt, now+LeaveDeadlineTTL)
	}
	if l.StudentName != "学生甲" || l.StudentNo != "20241001" || l.StudentUserID != student.ID {
		t.Fatalf("学生快照错误: %+v", l)
	}
	if l.ClassID != c.ID || l.ClassName != c.Name || l.Department != c.Department {
		t.Fatalf("班级快照错误: %+v", l)
	}
	if l.TeacherUserID != teacher.ID || l.CounselorUserID != counselor.ID ||
		l.TeacherName != teacher.Username || l.CounselorName != counselor.Username {
		t.Fatalf("审批人快照错误: %+v", l)
	}
	if l.DormBuilding != "21" || l.DormRoom != "601" || l.ContactPhone != "13800000001" {
		t.Fatalf("宿舍/电话快照错误: %+v", l)
	}
	if l.CreatedAt != now {
		t.Fatalf("created_at = %d", l.CreatedAt)
	}
	if countNotifications(t, s, teacher.ID, "leave_new") != 1 ||
		countNotifications(t, s, counselor.ID, "leave_new") != 1 {
		t.Fatal("提交后应通知班主任和辅导员")
	}

	// 即使开始时间在 20 小时后，截止时间仍然是提交后 24 小时。
	student2 := addStudent(t, s, "学生乙", "20241002", c.ID, "21-602")
	id2 := submitTestLeave(t, s, student2, now+20*3600, now+21*3600, "参加校外考试")
	if got := mustGetLeave(t, s, id2).DeadlineAt; got != now+LeaveDeadlineTTL {
		t.Fatalf("晚开始假条 deadline = %d", got)
	}
}

func TestSubmitLeaveValidation(t *testing.T) {
	s, clock, admin := newTestStore(t)
	teacher := addStaff(t, s, "teacher", "LT2", "13970000011")
	counselor := addStaff(t, s, "counselor", "LC2", "13970000012")
	c := makeClass(t, s, admin, teacher, counselor, "请假测试2班")
	student := addStudent(t, s, "学生丙", "20241011", c.ID, "21-603")
	now := clock.Unix()

	tests := []struct {
		name    string
		in      SubmitLeaveInput
		wantSub string
	}{
		{
			name:    "开始时间过早",
			in:      SubmitLeaveInput{Reason: "回家处理事务", StartAt: now - 301, EndAt: now + 3600},
			wantSub: "5 分钟",
		},
		{
			name:    "结束不晚于开始",
			in:      SubmitLeaveInput{Reason: "回家处理事务", StartAt: now + 3600, EndAt: now + 3600},
			wantSub: "结束时间",
		},
		{
			name:    "超过 30 天",
			in:      SubmitLeaveInput{Reason: "回家处理事务", StartAt: now, EndAt: now + LeaveMaxDuration + 1},
			wantSub: "30 天",
		},
		{
			name:    "原因过短",
			in:      SubmitLeaveInput{Reason: "四字", StartAt: now, EndAt: now + 3600},
			wantSub: "5–500",
		},
		{
			name:    "联系电话非法",
			in:      SubmitLeaveInput{ContactPhone: "abc", Reason: "回家处理事务", StartAt: now, EndAt: now + 3600},
			wantSub: "联系电话",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.SubmitLeave(ctx(), student, tc.in); err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("错误 = %v，期望包含 %q", err, tc.wantSub)
			}
		})
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM leave_requests WHERE student_user_id=?`, student.ID); n != 0 {
		t.Fatalf("校验失败不应写假条: %d", n)
	}

	// 5 分钟回溯宽限边界允许。
	if _, err := s.SubmitLeave(ctx(), student, SubmitLeaveInput{
		Reason: "回家处理事务", StartAt: now - LeaveBackdateGrace, EndAt: now + 3600,
	}); err != nil {
		t.Fatalf("5 分钟回溯边界应允许: %v", err)
	}
	if _, err := s.SubmitLeave(ctx(), student, SubmitLeaveInput{
		Reason: "时间重叠测试", StartAt: now + 100, EndAt: now + 2000,
	}); err == nil || !strings.Contains(err.Error(), "重叠") {
		t.Fatalf("重叠请假错误 = %v", err)
	}
}

func TestLeaveApprovalPermissionsAndQR(t *testing.T) {
	s, clock, admin := newTestStore(t)
	teacher := addStaff(t, s, "teacher", "LT3", "13970000021")
	counselor := addStaff(t, s, "counselor", "LC3", "13970000022")
	otherTeacher := addStaff(t, s, "teacher", "LO3", "13970000023")
	community := addStaff(t, s, "community", "LM3", "13970000024")
	c := makeClass(t, s, admin, teacher, counselor, "请假测试3班")
	otherClass := makeClass(t, s, admin, otherTeacher, User{}, "请假测试3班B")
	student := addStudent(t, s, "学生丁", "20241021", c.ID, "21-604")
	otherStudent := addStudent(t, s, "学生戊", "20241022", c.ID, "21-605")
	now := clock.Unix()

	lid := submitTestLeave(t, s, student, now-60, now+3600, "身体不适需要休息")
	l := mustGetLeave(t, s, lid)

	checks := []struct {
		name string
		user User
		want bool
	}{
		{name: "本班班主任", user: teacher, want: true},
		{name: "本班辅导员", user: counselor, want: true},
		{name: "其他班班主任", user: otherTeacher, want: false},
		{name: "社区老师", user: community, want: false},
		{name: "管理员", user: admin, want: false},
		{name: "学生", user: student, want: false},
	}
	for _, tc := range checks {
		t.Run("CanApprove/"+tc.name, func(t *testing.T) {
			if got := s.CanApprove(ctx(), tc.user, l); got != tc.want {
				t.Fatalf("CanApprove = %v，期望 %v", got, tc.want)
			}
		})
	}

	// 生成代理链接后由班主任网页审批，代理链接应立即失效。
	proxy, err := s.GenerateProxyLink(ctx(), teacher, lid, true)
	if err != nil {
		t.Fatalf("GenerateProxyLink: %v", err)
	}
	if err := s.DecideLeave(ctx(), teacher, lid, DecideInput{
		Decision: "approved", ApproverName: "王老师", MajorOpinion: "同意",
	}); err != nil {
		t.Fatalf("DecideLeave: %v", err)
	}
	l = mustGetLeave(t, s, lid)
	if l.Status != "approved" || l.Decision != "approved" || l.DecidedBy != teacher.ID || l.DecidedAt != now {
		t.Fatalf("审批结果 = %+v", l)
	}
	if l.MajorOpinion != "同意" || l.ApproverName != "王老师" || l.DecidedVia != "web" {
		t.Fatalf("审批意见 = %+v", l)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM qr_tokens WHERE leave_id=? AND revoked_at IS NULL`, lid); n != 1 {
		t.Fatalf("批准后二维码行 = %d", n)
	}
	g, err := s.GetGatePass(ctx(), s.QRToken(lid))
	if err != nil || g.State != "valid" || g.StateLabel != "有效" {
		t.Fatalf("门卫视图 = %+v err=%v", g, err)
	}
	if _, err := s.GetProxyView(ctx(), proxy); !errors.Is(err, ErrNotFound) {
		t.Fatalf("审批后代理链接应失效: %v", err)
	}
	if countNotifications(t, s, student.ID, "leave_decided") != 1 {
		t.Fatal("审批后应通知学生")
	}

	// 非本班/社区/管理员/学生都不能审批。
	for _, actor := range []User{otherTeacher, community, admin, otherStudent} {
		lid2 := submitTestLeave(t, s, otherStudent, now+7200, now+10800, "去医院检查身体")
		if err := s.DecideLeave(ctx(), actor, lid2, DecideInput{Decision: "approved", ApproverName: "X"}); err == nil ||
			!strings.Contains(err.Error(), "无法审批") {
			t.Fatalf("%s 审批错误 = %v", actor.Role, err)
		}
		// 每次提交后时间不同，避免重叠。
		now += 20000
	}
	_ = otherClass
}

func TestLeaveTimeout(t *testing.T) {
	s, clock, admin := newTestStore(t)
	teacher := addStaff(t, s, "teacher", "LT4", "13970000031")
	counselor := addStaff(t, s, "counselor", "LC4", "13970000032")
	c := makeClass(t, s, admin, teacher, counselor, "请假测试4班")
	student := addStudent(t, s, "学生己", "20241031", c.ID, "21-606")
	now := clock.Unix()
	lid := submitTestLeave(t, s, student, now+3600, now+7200, "回家处理家庭事务")

	clock.Advance(24*time.Hour - time.Second)
	if n, err := s.TimeoutLeaves(ctx(), 100); err != nil || n != 0 {
		t.Fatalf("截止前 1 秒超时处理 = %d,%v", n, err)
	}
	if got := mustGetLeave(t, s, lid).Status; got != "pending" {
		t.Fatalf("截止前状态 = %s", got)
	}

	clock.Advance(2 * time.Second)
	if n, err := s.TimeoutLeaves(ctx(), 100); err != nil || n != 1 {
		t.Fatalf("超时处理 = %d,%v", n, err)
	}
	l := mustGetLeave(t, s, lid)
	if l.Status != "rejected" || l.Decision != "timeout" || l.DecidedVia != "system" ||
		!strings.Contains(l.RejectReason, "24 小时") || l.DecidedAt != clock.Unix() {
		t.Fatalf("超时结果 = %+v", l)
	}
	if countNotifications(t, s, student.ID, "leave_timeout") != 1 {
		t.Fatal("超时后应通知学生")
	}
	if n, err := s.TimeoutLeaves(ctx(), 100); err != nil || n != 0 {
		t.Fatalf("重复超时处理 = %d,%v", n, err)
	}
}

func TestStudentCancelRules(t *testing.T) {
	s, clock, admin := newTestStore(t)
	teacher := addStaff(t, s, "teacher", "LT5", "13970000041")
	counselor := addStaff(t, s, "counselor", "LC5", "13970000042")
	c := makeClass(t, s, admin, teacher, counselor, "请假测试5班")
	student := addStudent(t, s, "学生庚", "20241041", c.ID, "21-607")
	other := addStudent(t, s, "学生辛", "20241042", c.ID, "21-608")
	now := clock.Unix()

	pendingID := submitTestLeave(t, s, student, now+3600, now+7200, "回家处理家庭事务")
	if err := s.CancelLeaveByStudent(ctx(), other, pendingID); err == nil ||
		!strings.Contains(err.Error(), "自己的") {
		t.Fatalf("他人取消错误 = %v", err)
	}
	if err := s.CancelLeaveByStudent(ctx(), student, pendingID); err != nil {
		t.Fatalf("本人取消 pending: %v", err)
	}
	l := mustGetLeave(t, s, pendingID)
	if l.Status != "cancelled" || l.Decision != "cancelled_by_student" {
		t.Fatalf("取消结果 = %+v", l)
	}
	if err := s.CancelLeaveByStudent(ctx(), student, pendingID); err == nil ||
		!strings.Contains(err.Error(), "不能取消") {
		t.Fatalf("重复取消错误 = %v", err)
	}

	approvedID := submitTestLeave(t, s, student, now+10800, now+14400, "去医院检查身体")
	if err := s.DecideLeave(ctx(), teacher, approvedID, DecideInput{Decision: "approved", ApproverName: "王老师"}); err != nil {
		t.Fatalf("批准: %v", err)
	}
	if err := s.CancelLeaveByStudent(ctx(), student, approvedID); err == nil ||
		!strings.Contains(err.Error(), "不能取消") {
		t.Fatalf("已批准取消错误 = %v", err)
	}
}

func TestProxyLinkOneTimeAndExpiry(t *testing.T) {
	s, clock, admin := newTestStore(t)
	teacher := addStaff(t, s, "teacher", "LT6", "13970000051")
	counselor := addStaff(t, s, "counselor", "LC6", "13970000052")
	community := addStaff(t, s, "community", "LM6", "13970000053")
	c := makeClass(t, s, admin, teacher, counselor, "请假测试6班")
	student := addStudent(t, s, "学生壬", "20241051", c.ID, "21-609")
	now := clock.Unix()

	lid := submitTestLeave(t, s, student, now+3600, now+7200, "回家处理家庭事务")
	if _, err := s.GenerateProxyLink(ctx(), community, lid, false); err == nil ||
		!strings.Contains(err.Error(), "无权") {
		t.Fatalf("社区老师生成代理错误 = %v", err)
	}
	token, err := s.GenerateProxyLink(ctx(), teacher, lid, true)
	if err != nil {
		t.Fatalf("GenerateProxyLink: %v", err)
	}
	v, err := s.GetProxyView(ctx(), token)
	if err != nil {
		t.Fatalf("GetProxyView: %v", err)
	}
	if !v.ShowPhone || v.Leave.ID != lid || v.ExpiresAt != mustGetLeave(t, s, lid).DeadlineAt {
		t.Fatalf("代理视图 = %+v", v)
	}
	if err := s.ProxyDecide(ctx(), token, DecideInput{Decision: "approved", ApproverName: "代理老师"}); err != nil {
		t.Fatalf("ProxyDecide: %v", err)
	}
	l := mustGetLeave(t, s, lid)
	if l.Status != "approved" || l.DecidedVia != "proxy" || l.ApproverName != "代理老师" {
		t.Fatalf("代理审批结果 = %+v", l)
	}
	if _, err := s.GetProxyView(ctx(), token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("一次性代理链接应失效: %v", err)
	}
	if err := s.ProxyDecide(ctx(), token, DecideInput{Decision: "approved", ApproverName: "二次"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("二次代理错误 = %v", err)
	}

	// 新假条的代理链接在 24 小时审批截止后失效。
	student2 := addStudent(t, s, "学生癸", "20241052", c.ID, "21-610")
	lid2 := submitTestLeave(t, s, student2, now+3600, now+7200, "参加校外考试")
	token2, err := s.GenerateProxyLink(ctx(), teacher, lid2, false)
	if err != nil {
		t.Fatalf("GenerateProxyLink 2: %v", err)
	}
	clock.Advance(24*time.Hour + time.Second)
	if _, err := s.GetProxyView(ctx(), token2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("过期代理视图错误 = %v", err)
	}
	if err := s.ProxyDecide(ctx(), token2, DecideInput{Decision: "approved", ApproverName: "过期"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("过期代理审批错误 = %v", err)
	}
}

func TestUnclaimedTickMarksMissingCountdown(t *testing.T) {
	s, clock, _ := newTestStore(t)
	now := clock.Unix()
	res, err := s.DB.Exec(`INSERT INTO classes (name,name_norm,department,teacher_user_id,counselor_user_id,version,created_at,updated_at)
		VALUES ('无倒计时班','无倒计时班','计算机系',NULL,NULL,1,?,?)`, now, now)
	if err != nil {
		t.Fatalf("insert class: %v", err)
	}
	id, _ := res.LastInsertId()
	if err := s.UnclaimedTick(ctx()); err != nil {
		t.Fatalf("UnclaimedTick: %v", err)
	}
	c, err := s.GetClass(ctx(), id)
	if err != nil {
		t.Fatalf("GetClass: %v", err)
	}
	if c.UnclaimedAt != now || c.DeleteDueAt != now+UnclaimedTTL {
		t.Fatalf("待认领倒计时 = unclaimed %d due %d", c.UnclaimedAt, c.DeleteDueAt)
	}
}

func TestArchiveCleanupAndArchivedLeaveVisibility(t *testing.T) {
	s, clock, admin := newTestStore(t)
	teacher := addStaff(t, s, "teacher", "LT7", "13970000061")
	counselor := addStaff(t, s, "counselor", "LC7", "13970000062")
	community := addStaff(t, s, "community", "LM7", "13970000063")
	student := addStudent(t, s, "学生子", "20241061", 0, "")
	student2 := addStudent(t, s, "学生丑", "20241062", 0, "")

	c := makeClass(t, s, admin, teacher, counselor, "待认领测试班")
	// 重新挂到班级（addStudent 先创建账号，便于拿到 ID）。
	if _, err := s.DB.Exec(`INSERT INTO student_classes (user_id,class_id,dorm_building,dorm_room,imported_at,joined_at)
		VALUES (?,?,?,?,?,?),(?,?,?,?,?,?)`, student.ID, c.ID, "21", "701", clock.Unix(), clock.Unix(),
		student2.ID, c.ID, "22", "702", clock.Unix(), clock.Unix()); err != nil {
		t.Fatalf("attach students: %v", err)
	}
	if err := s.SetCommunityBuildings(ctx(), community.ID, []string{"21"}); err != nil {
		t.Fatalf("SetCommunityBuildings: %v", err)
	}
	now := clock.Unix()
	lid1 := submitTestLeave(t, s, student, now+3600, now+7200, "回家处理家庭事务")
	lid2 := submitTestLeave(t, s, student2, now+10800, now+14400, "参加校外考试")
	if err := s.DecideLeave(ctx(), teacher, lid1, DecideInput{Decision: "approved", ApproverName: "王老师"}); err != nil {
		t.Fatalf("批准 lid1: %v", err)
	}

	if err := s.ExitTeacher(ctx(), teacher); err != nil {
		t.Fatalf("ExitTeacher: %v", err)
	}
	unclaimed, err := s.GetClass(ctx(), c.ID)
	if err != nil {
		t.Fatalf("GetClass after exit: %v", err)
	}
	if !unclaimed.Unclaimed() || unclaimed.DeleteDueAt != now+UnclaimedTTL || unclaimed.FormerTeacherID != teacher.ID {
		t.Fatalf("退出后班级 = %+v", unclaimed)
	}

	clock.Advance(UnclaimedTTL * time.Second)
	if err := s.UnclaimedTick(ctx()); err != nil {
		t.Fatalf("UnclaimedTick cleanup: %v", err)
	}
	cleanupAt := clock.Unix()
	if _, err := s.GetClass(ctx(), c.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("清理后班级仍存在: %v", err)
	}
	if _, err := s.GetUserByID(ctx(), student.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("清理后学生仍存在: %v", err)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM student_classes WHERE class_id=?`, c.ID); n != 0 {
		t.Fatalf("清理后学生班级关系 = %d", n)
	}

	for _, id := range []int64{lid1, lid2} {
		l := mustGetLeave(t, s, id)
		if l.ArchivedAt != cleanupAt || l.RetainUntil != cleanupAt+ArchiveRetainTTL || l.ArchivedClassID == 0 {
			t.Fatalf("归档字段 = %+v", l)
		}
		if l.StudentUserID != 0 {
			t.Fatalf("学生账号删除后 leave.student_user_id 应为 NULL，实际 %d", l.StudentUserID)
		}
	}
	if got := mustGetLeave(t, s, lid2); got.Status != "cancelled" || got.Decision != "class_cleaned" {
		t.Fatalf("待审批假条清理结果 = %+v", got)
	}

	archived, err := s.ArchivedClassList(ctx())
	if err != nil {
		t.Fatalf("ArchivedClassList: %v", err)
	}
	if len(archived) != 1 {
		t.Fatalf("归档班级数量 = %d", len(archived))
	}
	ac := archived[0]
	if ac.SourceClassID != c.ID || ac.LastTeacherUserID != teacher.ID || ac.LastCounselorID != counselor.ID ||
		ac.StudentCount != 2 || ac.LeaveCount != 2 || ac.RetainUntil != cleanupAt+ArchiveRetainTTL {
		t.Fatalf("归档班级 = %+v", ac)
	}

	// 权限过滤：管理员全部，原班主任/辅导员全部，社区老师只看负责楼栋的已批准记录，学生拒绝。
	adminList, total, err := s.ArchivedLeaves(ctx(), admin, LeaveFilter{})
	if err != nil || total != 2 || len(adminList) != 2 {
		t.Fatalf("管理员保留假条 = %d/%d err=%v", len(adminList), total, err)
	}
	teacherList, total, err := s.ArchivedLeaves(ctx(), teacher, LeaveFilter{})
	if err != nil || total != 2 || len(teacherList) != 2 {
		t.Fatalf("原班主任保留假条 = %d/%d err=%v", len(teacherList), total, err)
	}
	counselorList, total, err := s.ArchivedLeaves(ctx(), counselor, LeaveFilter{})
	if err != nil || total != 2 || len(counselorList) != 2 {
		t.Fatalf("辅导员保留假条 = %d/%d err=%v", len(counselorList), total, err)
	}
	communityList, total, err := s.ArchivedLeaves(ctx(), community, LeaveFilter{})
	if err != nil || total != 1 || len(communityList) != 1 || communityList[0].ID != lid1 {
		t.Fatalf("社区老师保留假条 = %+v total=%d err=%v", communityList, total, err)
	}
	studentActor := User{ID: student.ID, Role: "student"}
	if _, _, err := s.ArchivedLeaves(ctx(), studentActor, LeaveFilter{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("学生访问保留假条错误 = %v", err)
	}

	l1 := mustGetLeave(t, s, lid1)
	l2 := mustGetLeave(t, s, lid2)
	if !s.CanViewArchived(ctx(), admin, l1) || !s.CanViewArchived(ctx(), teacher, l1) ||
		!s.CanViewArchived(ctx(), counselor, l1) || !s.CanViewArchived(ctx(), community, l1) {
		t.Fatal("授权角色应能查看已批准归档假条")
	}
	if s.CanViewArchived(ctx(), community, l2) || s.CanViewArchived(ctx(), studentActor, l1) {
		t.Fatal("社区老师不应看到已取消归档假条，学生不应看到任何归档假条")
	}

	// 保留 40 天后 TTL 物理删除假条与归档班级。
	clock.Advance(ArchiveRetainTTL * time.Second)
	s.RunTTL(ctx())
	for _, id := range []int64{lid1, lid2} {
		if _, err := s.GetLeave(ctx(), id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("40 天后假条 %d 仍存在: %v", id, err)
		}
	}
	if left, _ := s.ArchivedClassList(ctx()); len(left) != 0 {
		t.Fatalf("40 天后归档班级仍存在: %+v", left)
	}
}
