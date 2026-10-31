package app

import (
	"errors"
	"testing"
	"time"

	"leavepass/internal/config"
)

type exportFixture struct {
	store     *Store
	admin     User
	teacher   User
	other     User
	counselor User
	community User
	classA    Class
	classB    Class
	stuA      User
	stuB      User
	leaveA1   int64
	leaveA2   int64
	leaveOld  int64
	leaveB1   int64
}

func newExportFixture(t *testing.T) exportFixture {
	t.Helper()
	s, clock, admin := newTestStore(t)
	teacher := addStaff(t, s, "teacher", "ET1", "13970000001")
	other := addStaff(t, s, "teacher", "ET2", "13970000002")
	counselor := addStaff(t, s, "counselor", "EC1", "13970000003")
	community := addStaff(t, s, "community", "ECO1", "13970000004")
	if err := s.SetCommunityBuildings(ctx(), community.ID, []string{"21"}); err != nil {
		t.Fatalf("SetCommunityBuildings: %v", err)
	}

	classA, err := s.CreateClass(ctx(), teacher, "导出A班", "计算机系", false)
	if err != nil {
		t.Fatalf("CreateClass A: %v", err)
	}
	if err := s.AssignCounselor(ctx(), admin, classA.ID, counselor.ID, "127.0.0.1"); err != nil {
		t.Fatalf("AssignCounselor A: %v", err)
	}
	classB, err := s.CreateClass(ctx(), other, "导出B班", "软件系", false)
	if err != nil {
		t.Fatalf("CreateClass B: %v", err)
	}
	if err := s.AssignCounselor(ctx(), admin, classB.ID, counselor.ID, "127.0.0.1"); err != nil {
		t.Fatalf("AssignCounselor B: %v", err)
	}

	stuA := addStudent(t, s, "张三", "20240001", classA.ID, "21-601")
	stuB := addStudent(t, s, "李四", "20240002", classB.ID, "22-602")

	at := func(y int, m time.Month, d, hour int) int64 {
		return time.Date(y, m, d, hour, 0, 0, 0, config.Location).Unix()
	}
	submit := func(stu User, phone, reason string, start int64) int64 {
		id, err := s.SubmitLeave(ctx(), stu, SubmitLeaveInput{
			ContactPhone: phone, Reason: reason, StartAt: start, EndAt: start + 30*60,
		})
		if err != nil {
			t.Fatalf("SubmitLeave(%s): %v", reason, err)
		}
		return id
	}
	clock.Set(time.Date(2024, 5, 30, 10, 0, 0, 0, config.Location))
	leaveOld := submit(stuA, "13900000001", "历史病假记录测试", at(2024, 6, 1, 8))
	clock.Set(time.Date(2026, 3, 15, 10, 0, 0, 0, config.Location))
	leaveA1 := submit(stuA, "13900000001", "因病需要请假休息", at(2026, 3, 15, 10))
	leaveA2 := submit(stuA, "13900000001", "家中有事需要请假", at(2026, 3, 15, 11))
	leaveB1 := submit(stuB, "13900000002", "参加学校公派活动", at(2026, 3, 15, 12))

	if err := s.DecideLeave(ctx(), teacher, leaveA2, DecideInput{
		Decision: "approved", ApproverName: teacher.Username, MajorOpinion: "同意", IP: "127.0.0.1",
	}); err != nil {
		t.Fatalf("DecideLeave A2: %v", err)
	}
	if err := s.DecideLeave(ctx(), other, leaveB1, DecideInput{
		Decision: "approved", ApproverName: other.Username, MajorOpinion: "同意", IP: "127.0.0.1",
	}); err != nil {
		t.Fatalf("DecideLeave B1: %v", err)
	}
	classA, _ = s.GetClass(ctx(), classA.ID)
	return exportFixture{
		store: s, admin: admin, teacher: teacher, other: other, counselor: counselor, community: community,
		classA: classA, classB: classB, stuA: stuA, stuB: stuB,
		leaveA1: leaveA1, leaveA2: leaveA2, leaveOld: leaveOld, leaveB1: leaveB1,
	}
}

func exportIDs(rows []Leave) map[int64]bool {
	out := make(map[int64]bool, len(rows))
	for _, l := range rows {
		out[l.ID] = true
	}
	return out
}

func TestExportLeavesRoleVisibility(t *testing.T) {
	f := newExportFixture(t)
	all := ExportQuery{}

	cases := []struct {
		name string
		user User
		want []int64
	}{
		{"teacher only own class", f.teacher, []int64{f.leaveA1, f.leaveA2, f.leaveOld}},
		{"other teacher only their class", f.other, []int64{f.leaveB1}},
		{"counselor all assigned classes", f.counselor, []int64{f.leaveA1, f.leaveA2, f.leaveOld, f.leaveB1}},
		{"community approved in own buildings", f.community, []int64{f.leaveA2}},
		{"admin sees everything", f.admin, []int64{f.leaveA1, f.leaveA2, f.leaveOld, f.leaveB1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := f.store.ExportLeaves(ctx(), tc.user, all)
			if err != nil {
				t.Fatalf("ExportLeaves: %v", err)
			}
			got := exportIDs(rows)
			if len(got) != len(tc.want) {
				t.Fatalf("拿到 %d 条，期望 %d 条: %v", len(got), len(tc.want), got)
			}
			for _, id := range tc.want {
				if !got[id] {
					t.Fatalf("缺少假条 %d，实际 %v", id, got)
				}
			}
		})
	}
}

func TestExportLeavesAdminScopes(t *testing.T) {
	f := newExportFixture(t)
	cases := []struct {
		name string
		q    ExportQuery
		want int
	}{
		{"all", ExportQuery{Scope: "all"}, 4},
		{"class A", ExportQuery{Scope: "class", ClassID: f.classA.ID}, 3},
		{"building 22", ExportQuery{Scope: "building", Building: "22"}, 1},
		{"department software", ExportQuery{Scope: "department", Department: "软件系"}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := f.store.ExportLeaves(ctx(), f.admin, tc.q)
			if err != nil {
				t.Fatalf("ExportLeaves: %v", err)
			}
			if len(rows) != tc.want {
				t.Fatalf("拿到 %d 条，期望 %d 条", len(rows), tc.want)
			}
		})
	}
}

func TestExportLeavesStudentAndWindow(t *testing.T) {
	f := newExportFixture(t)
	recentStart := time.Date(2025, 9, 15, 0, 0, 0, 0, config.Location).Unix()

	rows, err := f.store.ExportLeaves(ctx(), f.admin, ExportQuery{Scope: "all", Student: "20240001"})
	if err != nil {
		t.Fatalf("export student no: %v", err)
	}
	if got := exportIDs(rows); len(got) != 3 || !got[f.leaveOld] {
		t.Fatalf("按学号导出应包含该生 3 条（含历史），实际 %v", got)
	}

	rows, err = f.store.ExportLeaves(ctx(), f.admin, ExportQuery{Scope: "all", Student: "李四"})
	if err != nil || len(rows) != 1 || rows[0].ID != f.leaveB1 {
		t.Fatalf("按姓名导出 = %v, %v", exportIDs(rows), err)
	}

	rows, err = f.store.ExportLeaves(ctx(), f.admin, ExportQuery{Scope: "all", Student: "20240001", StartFrom: recentStart})
	if err != nil {
		t.Fatalf("export student window: %v", err)
	}
	if got := exportIDs(rows); len(got) != 2 || got[f.leaveOld] {
		t.Fatalf("默认 6 个月应排除 2024 年记录，实际 %v", got)
	}

	rows, err = f.store.ExportLeaves(ctx(), f.admin, ExportQuery{Scope: "all", StartFrom: recentStart})
	if err != nil || len(rows) != 3 {
		t.Fatalf("最近 6 个月 = %d, %v; want 3", len(rows), err)
	}

	oldEnd := time.Date(2024, 7, 1, 0, 0, 0, 0, config.Location).Unix()
	rows, err = f.store.ExportLeaves(ctx(), f.admin, ExportQuery{
		Scope: "all", StartFrom: time.Date(2024, 6, 1, 0, 0, 0, 0, config.Location).Unix(), StartTo: oldEnd,
	})
	if err != nil || len(rows) != 1 || rows[0].ID != f.leaveOld {
		t.Fatalf("自定义历史窗口 = %v, %v", exportIDs(rows), err)
	}
}

func TestExportLeavesRejectsInvalidInput(t *testing.T) {
	f := newExportFixture(t)
	if _, err := f.store.ExportLeaves(ctx(), User{ID: 999, Role: "student"}, ExportQuery{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("学生导出错误 = %v，期望 ErrForbidden", err)
	}
	if _, err := f.store.ExportLeaves(ctx(), f.admin, ExportQuery{Scope: "class"}); err == nil {
		t.Fatal("缺少班级时应报错")
	}
	if _, err := f.store.ExportLeaves(ctx(), f.admin, ExportQuery{Scope: "building"}); err == nil {
		t.Fatal("缺少楼栋时应报错")
	}
	if _, err := f.store.ExportLeaves(ctx(), f.admin, ExportQuery{Scope: "department"}); err == nil {
		t.Fatal("缺少专业系时应报错")
	}
	if _, err := f.store.ExportLeaves(ctx(), f.admin, ExportQuery{
		Scope: "all", StartFrom: 200, StartTo: 100,
	}); err == nil {
		t.Fatal("开始晚于结束时应报错")
	}
}

func TestLeaveExportFacets(t *testing.T) {
	f := newExportFixture(t)
	buildings, departments, err := f.store.LeaveExportFacets(ctx())
	if err != nil {
		t.Fatalf("LeaveExportFacets: %v", err)
	}
	if len(buildings) != 2 || buildings[0] != "21" || buildings[1] != "22" {
		t.Fatalf("buildings = %v", buildings)
	}
	if len(departments) != 2 || departments[0] != "计算机系" || departments[1] != "软件系" {
		t.Fatalf("departments = %v", departments)
	}
}
