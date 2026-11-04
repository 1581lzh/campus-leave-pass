package web

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"leavepass/internal/app"
)

func TestAdminAuditActionsRenderInChinese(t *testing.T) {
	store, _, admin := newWebStore(t)
	srv, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	rows := []struct {
		action     string
		targetType string
		targetID   int64
		detail     string
	}{
		{"regcode.update", "registration_code", 1, "次数 3，有效期至 2026-03-20 10:00，状态 启用"},
		{"security.revoke_own_sessions", "session", 0, "退出其他设备"},
		{"regcode.delete", "registration_code", 2, "码 TEST，角色 班主任，已用 1 / 2 次"},
	}
	for _, row := range rows {
		if err := store.Audit(ctx, nil, admin, row.action, row.targetType, row.targetID, row.detail, "10.0.0.9"); err != nil {
			t.Fatalf("Audit(%s): %v", row.action, err)
		}
	}
	cookie, _ := webSession(t, store, admin)

	rec := webRequest(t, srv, http.MethodGet, "/admin/audit", cookie, nil, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("审计页状态 = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"更新注册码", "退出其他设备", "删除注册码", "注册码#1", "会话#0"} {
		if !strings.Contains(body, want) {
			t.Fatalf("审计页缺少中文 %q:\n%s", want, body)
		}
	}
	for _, raw := range []string{
		`<code>regcode.update</code>`,
		`<code>security.revoke_own_sessions</code>`,
		`<code>regcode.delete</code>`,
	} {
		if strings.Contains(body, raw) {
			t.Fatalf("审计页仍显示原始动作代码 %q", raw)
		}
	}

	rec = webRequest(t, srv, http.MethodGet, "/admin", cookie, nil, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("概览页状态 = %d", rec.Code)
	}
	dash := rec.Body.String()
	for _, want := range []string{"更新注册码", "退出其他设备", "删除注册码"} {
		if !strings.Contains(dash, want) {
			t.Fatalf("概览最近操作缺少中文 %q:\n%s", want, dash)
		}
	}
}

func TestAdminAuditFilters(t *testing.T) {
	store, _, admin := newWebStore(t)
	srv, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	teacher := addWebStaff(t, store, "teacher", "AUDIT1", "13971000101")
	class := addWebClass(t, store, admin, teacher, app.User{}, "审计过滤班")
	student := addWebStudent(t, store, "审计学生", "20261001", class.ID, "21-601")
	ctx := context.Background()

	if err := store.Audit(ctx, nil, admin, "student.fix_no", "student", student.ID,
		"修正学号 20261001", "10.0.0.1"); err != nil {
		t.Fatalf("Audit(student): %v", err)
	}
	if err := store.Audit(ctx, nil, admin, "security.unlock", "ip", 0,
		"解除 IP 限制 10.0.0.2", "10.0.0.2"); err != nil {
		t.Fatalf("Audit(ip): %v", err)
	}

	cookie, _ := webSession(t, store, admin)
	get := func(query string) (int, string) {
		t.Helper()
		rec := webRequest(t, srv, http.MethodGet, "/admin/audit"+query, cookie, nil, "", "")
		return rec.Code, rec.Body.String()
	}
	mustContain := func(body string, wants ...string) {
		t.Helper()
		for _, want := range wants {
			if !strings.Contains(body, want) {
				t.Fatalf("audit page missing %q:\n%s", want, body)
			}
		}
	}
	mustNotContain := func(body string, unwanted string) {
		t.Helper()
		if strings.Contains(body, unwanted) {
			t.Fatalf("audit page unexpectedly contains %q", unwanted)
		}
	}

	classValue := strconv.FormatInt(class.ID, 10)
	code, body := get("?actor=admin&class_id=" + classValue)
	if code != http.StatusOK {
		t.Fatalf("GET filtered audit: status %d", code)
	}
	mustContain(body, "修正学号 20261001", `value="`+classValue+`" selected`)
	mustNotContain(body, "解除 IP 限制 10.0.0.2")

	code, body = get("?target=" + url.QueryEscape(student.StudentNo))
	if code != http.StatusOK {
		t.Fatalf("GET target filter: status %d", code)
	}
	mustContain(body, "修正学号 20261001")
	mustNotContain(body, "解除 IP 限制 10.0.0.2")

	code, body = get("?department=" + url.QueryEscape("计算机系"))
	if code != http.StatusOK {
		t.Fatalf("GET department filter: status %d", code)
	}
	mustContain(body, "修正学号 20261001")
	mustNotContain(body, "解除 IP 限制 10.0.0.2")

	code, body = get("?action=security.unlock")
	if code != http.StatusOK {
		t.Fatalf("GET action filter: status %d", code)
	}
	mustContain(body, "解除 IP 限制 10.0.0.2", "筛选条件（已启用 1 项）")
	mustNotContain(body, "修正学号 20261001")

	code, body = get("?q=" + url.QueryEscape("10.0.0.2"))
	if code != http.StatusOK {
		t.Fatalf("GET keyword filter: status %d", code)
	}
	mustContain(body, "解除 IP 限制 10.0.0.2")
	mustNotContain(body, "修正学号 20261001")

	if code, _ = get("?class_id=abc"); code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid class_id status = %d, want 422", code)
	}
	if code, _ = get("?range=custom"); code != http.StatusUnprocessableEntity {
		t.Fatalf("empty custom range status = %d, want 422", code)
	}
}
