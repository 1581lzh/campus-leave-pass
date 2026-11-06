package app

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestClaimCounselorClassReplacesAndNotifies(t *testing.T) {
	s, clock, admin := newTestStore(t)
	teacher := addStaff(t, s, "teacher", "CT1", "13900000001")
	current := addStaff(t, s, "counselor", "CC1", "13900000002")
	claimant := addStaff(t, s, "counselor", "CC2", "13900000003")
	invitee := addStaff(t, s, "counselor", "CC3", "13900000004")
	class := makeClass(t, s, admin, teacher, current, "辅导员认领测试班")

	if _, err := s.InviteCounselor(ctx(), teacher, class.ID, invitee.Username, ""); err != nil {
		t.Fatalf("InviteCounselor: %v", err)
	}
	// Isolate the notifications produced by the claim itself from setup noise.
	if _, err := s.DB.Exec(`DELETE FROM notifications`); err != nil {
		t.Fatalf("clear notifications: %v", err)
	}

	if err := s.ClaimCounselorClass(ctx(), claimant, class.ID, false); !errors.Is(err, ErrCounselorTakeoverNeeded) {
		t.Fatalf("未确认顶替错误 = %v，期望 ErrCounselorTakeoverNeeded", err)
	}
	if got, err := s.GetClass(ctx(), class.ID); err != nil || got.CounselorUserID != current.ID {
		t.Fatalf("未确认顶替后辅导员 = %d, err=%v，期望仍为 %d", got.CounselorUserID, err, current.ID)
	}

	clock.Advance(90 * time.Minute)
	if err := s.ClaimCounselorClass(ctx(), claimant, class.ID, true); err != nil {
		t.Fatalf("ClaimCounselorClass: %v", err)
	}
	got, err := s.GetClass(ctx(), class.ID)
	if err != nil {
		t.Fatalf("GetClass: %v", err)
	}
	if got.CounselorUserID != claimant.ID || got.CounselorName != claimant.Username {
		t.Fatalf("认领后辅导员 = %d/%q，期望 %d/%q", got.CounselorUserID, got.CounselorName, claimant.ID, claimant.Username)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM class_counselor_requests WHERE class_id=? AND status='pending'`, class.ID); n != 0 {
		t.Fatalf("认领后待处理邀请 = %d，期望 0", n)
	}

	notes, err := s.ListNotifications(ctx(), current.ID, 50)
	if err != nil {
		t.Fatalf("ListNotifications: %v", err)
	}
	var takeover *Notification
	for i := range notes {
		if notes[i].Kind == "counselor_takeover" {
			takeover = &notes[i]
			break
		}
	}
	if takeover == nil {
		t.Fatalf("被顶替辅导员未收到 counselor_takeover 通知: %+v", notes)
	}
	for _, want := range []string{"2026-03-15 11:30", claimant.Username, class.Name, "顶替了你的辅导员岗位", "你已不再是"} {
		if !strings.Contains(takeover.Body, want) {
			t.Fatalf("顶替通知缺少 %q: %s", want, takeover.Body)
		}
	}
	if n := countNotifications(t, s, teacher.ID, "class_counselor"); n != 1 {
		t.Fatalf("班主任辅导员调整通知 = %d，期望 1", n)
	}
	if n := countNotifications(t, s, claimant.ID, "class_counselor_claimed"); n != 1 {
		t.Fatalf("认领成功通知 = %d，期望 1", n)
	}

	// Re-claiming the same class is idempotent and must not create another
	// takeover notification or change the class version.
	before := countNotifications(t, s, current.ID, "counselor_takeover")
	if err := s.ClaimCounselorClass(ctx(), claimant, class.ID, false); err != nil {
		t.Fatalf("重复认领: %v", err)
	}
	if after := countNotifications(t, s, current.ID, "counselor_takeover"); after != before {
		t.Fatalf("重复认领新增顶替通知 %d -> %d", before, after)
	}
}

func TestClaimCounselorClassWithoutCurrentCounselor(t *testing.T) {
	s, _, admin := newTestStore(t)
	teacher := addStaff(t, s, "teacher", "CT2", "13900000011")
	counselor := addStaff(t, s, "counselor", "CC4", "13900000012")
	class := makeClass(t, s, admin, teacher, User{}, "辅导员空位班")

	if err := s.ClaimCounselorClass(ctx(), counselor, class.ID, false); err != nil {
		t.Fatalf("空位班级认领: %v", err)
	}
	got, err := s.GetClass(ctx(), class.ID)
	if err != nil || got.CounselorUserID != counselor.ID {
		t.Fatalf("空位班级认领后辅导员 = %d, err=%v，期望 %d", got.CounselorUserID, err, counselor.ID)
	}
}
