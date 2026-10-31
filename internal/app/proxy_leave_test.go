package app

import (
	"strings"
	"testing"
)

// TestSubmitLeaveForStudentAndSelfApprove covers staff proxy submission:
// the snapshot belongs to the student, CreatedBy records the staff member, and
// the same teacher may approve the request.
func TestSubmitLeaveForStudentAndSelfApprove(t *testing.T) {
	s, clock, admin := newTestStore(t)
	teacher := addStaff(t, s, "teacher", "PX1", "13993000001")
	counselor := addStaff(t, s, "counselor", "PX2", "13993000002")
	otherTeacher := addStaff(t, s, "teacher", "PX3", "13993000003")

	class := makeClass(t, s, admin, teacher, counselor, "代提交测试班")
	makeClass(t, s, admin, otherTeacher, User{}, "代提交其他班")
	student := addStudent(t, s, "代提交学生", "20247001", class.ID, "21-1701")
	now := clock.Unix()

	id, err := s.SubmitLeaveForStudent(ctx(), teacher, student.ID, SubmitLeaveInput{
		ContactPhone: "13800000021",
		Reason:       "班主任代学生提交请假",
		StartAt:      now + 3600,
		EndAt:        now + 7200,
	})
	if err != nil {
		t.Fatalf("班主任代提交: %v", err)
	}
	l := mustGetLeave(t, s, id)
	if l.StudentUserID != student.ID || l.StudentName != student.Username || l.StudentNo != "20247001" {
		t.Fatalf("学生快照错误: %+v", l)
	}
	if l.DormBuilding != "21" || l.DormRoom != "1701" {
		t.Fatalf("宿舍快照错误: %+v", l)
	}
	if l.ClassID != class.ID || l.ClassName != class.Name || l.Department != class.Department {
		t.Fatalf("班级快照错误: %+v", l)
	}
	if l.CreatedBy != teacher.ID {
		t.Fatalf("CreatedBy = %d，期望班主任 %d", l.CreatedBy, teacher.ID)
	}
	if l.Status != "pending" || l.DeadlineAt != now+LeaveDeadlineTTL {
		t.Fatalf("状态/截止时间 = %s/%d", l.Status, l.DeadlineAt)
	}

	if err := s.DecideLeave(ctx(), teacher, id, DecideInput{
		Decision: "approved", ApproverName: "代提交班主任", MajorOpinion: "同意",
	}); err != nil {
		t.Fatalf("班主任自审代提交假条: %v", err)
	}
	l = mustGetLeave(t, s, id)
	if l.Status != "approved" || l.Decision != "approved" || l.ApproverName != "代提交班主任" || l.DecidedBy != teacher.ID {
		t.Fatalf("自审结果 = %+v", l)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM qr_tokens WHERE leave_id=? AND revoked_at IS NULL`, id); n != 1 {
		t.Fatalf("通过后二维码行 = %d", n)
	}

	// 其他班班主任不能代本班学生提交。
	_, err = s.SubmitLeaveForStudent(ctx(), otherTeacher, student.ID, SubmitLeaveInput{
		Reason: "其他班代提交应被拒绝", StartAt: now + 10800, EndAt: now + 14400,
	})
	if err == nil || !strings.Contains(err.Error(), "只有该生当前班级") {
		t.Fatalf("其他班班主任代提交错误 = %v", err)
	}

	// 本班辅导员可以代提交。
	id2, err := s.SubmitLeaveForStudent(ctx(), counselor, student.ID, SubmitLeaveInput{
		Reason: "辅导员代学生提交请假", StartAt: now + 10800, EndAt: now + 14400,
	})
	if err != nil {
		t.Fatalf("辅导员代提交: %v", err)
	}
	if got := mustGetLeave(t, s, id2); got.CreatedBy != counselor.ID || got.Status != "pending" {
		t.Fatalf("辅导员代提交结果 = %+v", got)
	}

	// 已有 pending 时再次代提交重叠时间应被拒绝。
	_, err = s.SubmitLeaveForStudent(ctx(), teacher, student.ID, SubmitLeaveInput{
		Reason: "与待审批假条重叠", StartAt: now + 11000, EndAt: now + 12000,
	})
	if err == nil || !strings.Contains(err.Error(), "重叠") {
		t.Fatalf("重叠代提交错误 = %v", err)
	}

	// 学生自己仍然可以正常提交，CreatedBy 为学生本人。
	id3, err := s.SubmitLeave(ctx(), student, SubmitLeaveInput{
		Reason: "学生自己提交请假", StartAt: now + 20000, EndAt: now + 24000,
	})
	if err != nil {
		t.Fatalf("学生自己提交: %v", err)
	}
	got := mustGetLeave(t, s, id3)
	if got.CreatedBy != student.ID {
		t.Fatalf("学生自提 CreatedBy = %d，期望 %d", got.CreatedBy, student.ID)
	}
	if got.Status != "pending" || got.StudentUserID != student.ID {
		t.Fatalf("学生自提结果 = %+v", got)
	}
}
