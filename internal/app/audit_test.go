package app

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func searchOneAudit(t *testing.T, s *Store, action string) AuditLog {
	t.Helper()
	logs, total, err := s.SearchAuditLogs(ctx(), AuditFilter{Action: action})
	if err != nil {
		t.Fatalf("SearchAuditLogs(%s): %v", action, err)
	}
	if total != 1 || len(logs) != 1 {
		t.Fatalf("SearchAuditLogs(%s) total=%d len=%d", action, total, len(logs))
	}
	return logs[0]
}

func TestAuditCapturesTargetSnapshots(t *testing.T) {
	s, _, admin := newTestStore(t)
	teacher := addStaff(t, s, "teacher", "AS1", "13960000001")
	class := makeClass(t, s, admin, teacher, User{}, "审计快照班")
	student := addStudent(t, s, "审计快照学生", "20246001", class.ID, "21-601")

	tests := []struct {
		name           string
		action         string
		targetType     string
		targetID       int64
		wantClassID    int64
		wantClassName  string
		wantDepartment string
		wantBuilding   string
	}{
		{
			name: "class target", action: "audit.snapshot.class", targetType: "class", targetID: class.ID,
			wantClassID: class.ID, wantClassName: class.Name, wantDepartment: class.Department,
		},
		{
			name: "student target", action: "audit.snapshot.student", targetType: "student", targetID: student.ID,
			wantClassID: class.ID, wantClassName: class.Name, wantDepartment: class.Department, wantBuilding: "21",
		},
		{
			name: "user target with student relation", action: "audit.snapshot.user_student", targetType: "user", targetID: student.ID,
			wantClassID: class.ID, wantClassName: class.Name, wantDepartment: class.Department, wantBuilding: "21",
		},
		{
			name: "user target with teacher relation", action: "audit.snapshot.user_teacher", targetType: "user", targetID: teacher.ID,
			wantClassID: class.ID, wantClassName: class.Name, wantDepartment: class.Department,
		},
		{
			name: "other target", action: "audit.snapshot.other", targetType: "ip", targetID: 123,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.Audit(ctx(), nil, admin, tc.action, tc.targetType, tc.targetID, "详情", "127.0.0.1"); err != nil {
				t.Fatalf("Audit: %v", err)
			}
			got := searchOneAudit(t, s, tc.action)
			if got.TargetID != tc.targetID || got.TargetType != tc.targetType {
				t.Fatalf("target = %s/%d，期望 %s/%d", got.TargetType, got.TargetID, tc.targetType, tc.targetID)
			}
			if got.TargetClassID != tc.wantClassID || got.TargetClassName != tc.wantClassName ||
				got.TargetDepartment != tc.wantDepartment || got.TargetBuilding != tc.wantBuilding {
				t.Fatalf("snapshot = %d/%q/%q/%q，期望 %d/%q/%q/%q",
					got.TargetClassID, got.TargetClassName, got.TargetDepartment, got.TargetBuilding,
					tc.wantClassID, tc.wantClassName, tc.wantDepartment, tc.wantBuilding)
			}
		})
	}

	// A missing lookup target must not block the audit write.
	if err := s.Audit(ctx(), nil, admin, "audit.snapshot.missing", "class", 999999, "详情", ""); err != nil {
		t.Fatalf("Audit with missing target: %v", err)
	}
	got := searchOneAudit(t, s, "audit.snapshot.missing")
	if got.TargetClassID != 0 || got.TargetClassName != "" || got.TargetDepartment != "" || got.TargetBuilding != "" {
		t.Fatalf("缺失目标快照 = %+v", got)
	}
}

func TestAuditSnapshotSurvivesClassDisband(t *testing.T) {
	s, _, admin := newTestStore(t)
	teacher := addStaff(t, s, "teacher", "AD1", "13960000011")
	class := makeClass(t, s, admin, teacher, User{}, "审计解散班")
	addStudent(t, s, "解散班学生", "20246011", class.ID, "21-701")

	if err := s.DisbandClass(ctx(), admin, class.ID, "127.0.0.1"); err != nil {
		t.Fatalf("DisbandClass: %v", err)
	}
	if _, err := s.GetClass(ctx(), class.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("解散后班级仍存在: %v", err)
	}
	got := searchOneAudit(t, s, "class.disband")
	if got.TargetClassID != class.ID || got.TargetClassName != class.Name ||
		got.TargetDepartment != class.Department || got.TargetBuilding != "" {
		t.Fatalf("解散审计快照 = %+v", got)
	}

	facets, err := s.AuditFacets(ctx())
	if err != nil {
		t.Fatalf("AuditFacets: %v", err)
	}
	found := false
	for _, c := range facets.Classes {
		if c.ID == class.ID && c.Name == class.Name && c.Department == class.Department {
			found = true
		}
	}
	if !found {
		t.Fatalf("历史班级未出现在 facets: %+v", facets.Classes)
	}
}

func TestAuditSnapshotSurvivesClassCleanup(t *testing.T) {
	s, clock, admin := newTestStore(t)
	counselor := addStaff(t, s, "counselor", "AC1", "13960000021")
	class := makeClass(t, s, admin, User{}, counselor, "审计清理班")
	clock.Advance(8 * 24 * time.Hour)

	if err := s.CleanupClass(ctx(), class.ID); err != nil {
		t.Fatalf("CleanupClass: %v", err)
	}
	if _, err := s.GetClass(ctx(), class.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("清理后班级仍存在: %v", err)
	}
	got := searchOneAudit(t, s, "class.cleanup")
	if got.TargetClassID != class.ID || got.TargetClassName != class.Name ||
		got.TargetDepartment != class.Department || got.TargetBuilding != "" {
		t.Fatalf("清理审计快照 = %+v", got)
	}
}

func TestSearchAuditLogsFiltersAndFacets(t *testing.T) {
	s, clock, admin := newTestStore(t)
	teacher1 := addStaff(t, s, "teacher", "AF1", "13960000031")
	teacher2 := addStaff(t, s, "teacher", "AF2", "13960000032")
	class1 := makeClass(t, s, admin, teacher1, User{}, "审计过滤一班")
	class2, err := s.CreateClass(ctx(), teacher2, "审计过滤二班", "外语系", false)
	if err != nil {
		t.Fatalf("CreateClass: %v", err)
	}
	student1 := addStudent(t, s, "审计学生一", "20246021", class1.ID, "21-801")
	student2 := addStudent(t, s, "审计学生二", "20246022", class2.ID, "23-802")

	entries := []struct {
		actor      User
		action     string
		targetType string
		targetID   int64
		detail     string
		ip         string
	}{
		{teacher1, "audit.alpha", "student", student1.ID, "关键字 Alpha", "10.1.1.1"},
		{admin, "audit.beta", "user", teacher1.ID, "普通记录", "10.2.2.2"},
		{admin, "audit.gamma", "class", class2.ID, "关键字 Gamma", "10.3.3.3"},
		{teacher2, "audit.delta", "student", student2.ID, "其他", "10.4.4.4"},
		{admin, "audit.epsilon", "ip", 123, "关键字 Epsilon", "10.5.5.5"},
	}
	for i, e := range entries {
		if i > 0 {
			clock.Advance(24 * time.Hour)
		}
		if err := s.Audit(ctx(), nil, e.actor, e.action, e.targetType, e.targetID, e.detail, e.ip); err != nil {
			t.Fatalf("Audit(%s): %v", e.action, err)
		}
	}

	tests := []struct {
		name   string
		filter AuditFilter
		want   []string
	}{
		{"actor", AuditFilter{Actor: "AF1"}, []string{"audit.alpha"}},
		{"target student no", AuditFilter{Target: "20246021"}, []string{"audit.alpha"}},
		{"target username", AuditFilter{Target: "AF1"}, []string{"audit.beta"}},
		{"action exact", AuditFilter{Action: "audit.gamma"}, []string{"audit.gamma"}},
		{"target type", AuditFilter{TargetType: "class"}, []string{"audit.gamma"}},
		{"class", AuditFilter{ClassID: class2.ID}, []string{"audit.delta", "audit.gamma"}},
		{"department", AuditFilter{Department: "外语系"}, []string{"audit.delta", "audit.gamma"}},
		{"department exact", AuditFilter{Department: "外语"}, nil},
		{"building", AuditFilter{Building: "23"}, []string{"audit.delta"}},
		{"keyword detail", AuditFilter{Keyword: "关键字"}, []string{"audit.epsilon", "audit.gamma", "audit.alpha"}},
		{"keyword ip", AuditFilter{Keyword: "10.4.4.4"}, []string{"audit.delta"}},
		{"keyword action", AuditFilter{Keyword: "audit.beta"}, []string{"audit.beta"}},
		{"date", AuditFilter{CreatedFrom: "2026-03-16", CreatedTo: "2026-03-16"}, []string{"audit.beta"}},
		{"date range", AuditFilter{CreatedFrom: "2026-03-17", CreatedTo: "2026-03-18"}, []string{"audit.delta", "audit.gamma"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logs, total, err := s.SearchAuditLogs(ctx(), tc.filter)
			if err != nil {
				t.Fatalf("SearchAuditLogs: %v", err)
			}
			if total != len(tc.want) || len(logs) != len(tc.want) {
				t.Fatalf("total=%d len=%d，期望 %d", total, len(logs), len(tc.want))
			}
			var got []string
			for _, l := range logs {
				got = append(got, l.Action)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("actions = %v，期望 %v", got, tc.want)
			}
		})
	}

	if limit, offset := (AuditFilter{}).Paging(); limit != 50 || offset != 0 {
		t.Fatalf("默认分页 = %d/%d", limit, offset)
	}
	page1, total, err := s.SearchAuditLogs(ctx(), AuditFilter{Page: 1, PageSize: 2})
	if err != nil || total != 5 {
		t.Fatalf("分页总数 = %d err=%v", total, err)
	}
	page2, _, _ := s.SearchAuditLogs(ctx(), AuditFilter{Page: 2, PageSize: 2})
	page3, _, _ := s.SearchAuditLogs(ctx(), AuditFilter{Page: 3, PageSize: 2})
	page4, _, _ := s.SearchAuditLogs(ctx(), AuditFilter{Page: 4, PageSize: 2})
	gotPage := func(logs []AuditLog) string {
		var out []string
		for _, l := range logs {
			out = append(out, l.Action)
		}
		return strings.Join(out, ",")
	}
	if got := gotPage(page1); got != "audit.epsilon,audit.delta" {
		t.Fatalf("第 1 页 = %s", got)
	}
	if got := gotPage(page2); got != "audit.gamma,audit.beta" {
		t.Fatalf("第 2 页 = %s", got)
	}
	if got := gotPage(page3); got != "audit.alpha" {
		t.Fatalf("第 3 页 = %s", got)
	}
	if len(page4) != 0 {
		t.Fatalf("第 4 页应无数据: %+v", page4)
	}

	facets, err := s.AuditFacets(ctx())
	if err != nil {
		t.Fatalf("AuditFacets: %v", err)
	}
	findClass := func(id int64) bool {
		for _, c := range facets.Classes {
			if c.ID == id {
				return true
			}
		}
		return false
	}
	if !findClass(class1.ID) || !findClass(class2.ID) {
		t.Fatalf("facet classes = %+v", facets.Classes)
	}
	for _, want := range []string{"21", "22", "23"} {
		found := false
		for _, b := range facets.Buildings {
			if b == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("facet buildings = %v，缺少 %s", facets.Buildings, want)
		}
	}
	for _, want := range []string{"外语系", "计算机系"} {
		found := false
		for _, d := range facets.Departments {
			if d == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("facet departments = %v，缺少 %s", facets.Departments, want)
		}
	}
	if got := strings.Join(facets.Actions, ","); got != "audit.alpha,audit.beta,audit.delta,audit.epsilon,audit.gamma" {
		t.Fatalf("facet actions = %s", got)
	}
	if got := strings.Join(facets.TargetTypes, ","); got != "class,ip,student,user" {
		t.Fatalf("facet target types = %s", got)
	}
}
