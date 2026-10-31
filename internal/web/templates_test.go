package web

import "testing"

// TestTemplatesCompile ensures every routed page compiles together with the
// shared layout and partials, and that no page referenced by a handler is
// missing from the embedded template set.
func TestTemplatesCompile(t *testing.T) {
	s, err := New(nil)
	if err != nil {
		t.Fatalf("compile templates: %v", err)
	}
	want := []string{
		"admin_audit", "admin_class", "admin_classes", "admin_codes",
		"admin_community", "admin_dashboard", "admin_dorm_edits", "admin_forgot",
		"admin_imports", "admin_leave", "admin_leaves", "admin_recover",
		"admin_reports", "admin_security", "admin_user", "admin_users",
		"archived_leave", "archived_leaves",
		"class_claim", "class_counselor", "class_detail", "class_imports",
		"class_invitations", "class_new", "class_students",
		"community_buildings", "community_dashboard", "community_leave",
		"community_leaves", "community_students", "export_leaves",
		"error", "forgot", "gate", "landing", "login", "notifications",
		"profile", "proxy", "recover", "recover_status", "register", "reset",
		"staff_recover", "staff_register",
		"student_dashboard", "student_leave_detail", "student_leave_new",
		"student_leaves", "student_recover",
		"teacher_classes", "teacher_dashboard", "teacher_leave", "teacher_leave_new",
	}
	for _, page := range want {
		if s.pages[page] == nil {
			t.Errorf("missing template for page %q", page)
		}
	}
}
