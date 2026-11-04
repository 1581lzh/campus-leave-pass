package app

import (
	"errors"
	"strings"
	"testing"
)

func TestBatchSoftDeleteUsersKeepsSingleItemRules(t *testing.T) {
	s, _, admin := newTestStore(t)
	teacher := addStaff(t, s, "teacher", "BU1", "13986000001")
	community := addStaff(t, s, "community", "BU2", "13986000002")

	res, err := s.BatchSoftDeleteUsers(ctx(), admin, []int64{teacher.ID, community.ID, 999999, admin.ID}, "批量离校", "127.0.0.1")
	if err != nil {
		t.Fatalf("BatchSoftDeleteUsers: %v", err)
	}
	if res.SucceededCount() != 2 {
		t.Fatalf("成功数 = %d，期望 2", res.SucceededCount())
	}
	if res.FailedCount() != 2 {
		t.Fatalf("失败数 = %d，期望 2", res.FailedCount())
	}
	for _, id := range []int64{teacher.ID, community.ID} {
		u, err := s.GetUserByID(ctx(), id)
		if err != nil {
			t.Fatalf("GetUserByID(%d): %v", id, err)
		}
		if u.Status != "soft_deleted" {
			t.Fatalf("用户 %d 状态 = %q，期望 soft_deleted", id, u.Status)
		}
	}
	if u, err := s.GetUserByID(ctx(), admin.ID); err != nil || u.Status != "active" {
		t.Fatalf("管理员自身被停用: %+v, %v", u, err)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM admin_audit_logs WHERE action='user.soft_delete'`); n != 2 {
		t.Fatalf("批量停用审计行 = %d，期望 2", n)
	}
	for _, f := range res.Failures {
		if f.Label == "" || f.Err == nil {
			t.Fatalf("失败项缺少标签或原因: %+v", f)
		}
	}
}

func TestBatchDisbandClassesIsIndependentPerItem(t *testing.T) {
	s, _, admin := newTestStore(t)
	teacher1 := addStaff(t, s, "teacher", "BC1", "13986000011")
	counselor1 := addStaff(t, s, "counselor", "BC2", "13986000012")
	teacher2 := addStaff(t, s, "teacher", "BC3", "13986000013")
	counselor2 := addStaff(t, s, "counselor", "BC4", "13986000014")
	class1 := makeClass(t, s, admin, teacher1, counselor1, "批量解散一班")
	class2 := makeClass(t, s, admin, teacher2, counselor2, "批量解散二班")
	addStudent(t, s, "批量解散学生", "20246101", class1.ID, "21-1501")

	res, err := s.BatchDisbandClasses(ctx(), admin, []int64{class1.ID, 999999, class2.ID}, "127.0.0.1")
	if err != nil {
		t.Fatalf("BatchDisbandClasses: %v", err)
	}
	if res.SucceededCount() != 2 || res.FailedCount() != 1 {
		t.Fatalf("结果 = 成功 %d 失败 %d，期望 2/1", res.SucceededCount(), res.FailedCount())
	}
	for _, id := range []int64{class1.ID, class2.ID} {
		if _, err := s.GetClass(ctx(), id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("班级 %d 未解散: %v", id, err)
		}
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM admin_audit_logs WHERE action='class.disband'`); n != 2 {
		t.Fatalf("批量解散审计行 = %d，期望 2", n)
	}
}

func TestBatchRegistrationCodeStateAndDelete(t *testing.T) {
	s, _, admin := newTestStore(t)
	code1, _ := mustCreateCode(t, s, admin, "teacher", 3)
	code2, _ := mustCreateCode(t, s, admin, "counselor", 2)
	code3, _ := mustCreateCode(t, s, admin, "community", 1)

	off := false
	if err := s.UpdateRegistrationCode(ctx(), admin, code3.ID, 0, 0, &off, "127.0.0.1"); err != nil {
		t.Fatalf("预停用 code3: %v", err)
	}

	res, err := s.BatchSetRegistrationCodeActive(ctx(), admin, []int64{code1.ID, code2.ID, code3.ID}, false, "127.0.0.1")
	if err != nil {
		t.Fatalf("批量停用: %v", err)
	}
	if res.SucceededCount() != 2 || res.SkippedCount() != 1 {
		t.Fatalf("批量停用结果 = 成功 %d 跳过 %d，期望 2/1", res.SucceededCount(), res.SkippedCount())
	}
	for _, id := range []int64{code1.ID, code2.ID, code3.ID} {
		if c := s.GetRegistrationCode(ctx(), id); c.Active {
			t.Fatalf("注册码 %d 仍启用", id)
		}
	}

	res, err = s.BatchSetRegistrationCodeActive(ctx(), admin, []int64{code1.ID, code2.ID, code3.ID}, true, "127.0.0.1")
	if err != nil {
		t.Fatalf("批量启用: %v", err)
	}
	if res.SucceededCount() != 3 || res.SkippedCount() != 0 {
		t.Fatalf("批量启用结果 = 成功 %d 跳过 %d，期望 3/0", res.SucceededCount(), res.SkippedCount())
	}

	res, err = s.BatchDeleteRegistrationCodes(ctx(), admin, []int64{code1.ID, code2.ID, 999999}, "127.0.0.1")
	if err != nil {
		t.Fatalf("批量删除: %v", err)
	}
	if res.SucceededCount() != 2 || res.FailedCount() != 1 {
		t.Fatalf("批量删除结果 = 成功 %d 失败 %d，期望 2/1", res.SucceededCount(), res.FailedCount())
	}
	if got := s.GetRegistrationCode(ctx(), code3.ID); got.ID != code3.ID {
		t.Fatalf("未选中的注册码被删除: %+v", got)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM admin_audit_logs WHERE action='regcode.delete'`); n != 2 {
		t.Fatalf("批量删除审计行 = %d，期望 2", n)
	}
}

func TestBatchMethodsRejectNonAdmin(t *testing.T) {
	s, _, admin := newTestStore(t)
	teacher := addStaff(t, s, "teacher", "BNA", "13986000021")
	code, _ := mustCreateCode(t, s, admin, "teacher", 1)
	if _, err := s.BatchSoftDeleteUsers(ctx(), teacher, []int64{code.ID}, "", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("非管理员批量停用错误 = %v", err)
	}
	if _, err := s.BatchDisbandClasses(ctx(), teacher, []int64{1}, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("非管理员批量解散错误 = %v", err)
	}
	if _, err := s.BatchSetRegistrationCodeActive(ctx(), teacher, []int64{code.ID}, false, ""); err == nil || !strings.Contains(err.Error(), "只有管理员") {
		t.Fatalf("非管理员批量停用注册码错误 = %v", err)
	}
	if _, err := s.BatchDeleteRegistrationCodes(ctx(), teacher, []int64{code.ID}, ""); err == nil || !strings.Contains(err.Error(), "只有管理员") {
		t.Fatalf("非管理员批量删除注册码错误 = %v", err)
	}
}
