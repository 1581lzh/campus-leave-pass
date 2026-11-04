package web

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestAdminUserBatchPageAndSoftDelete(t *testing.T) {
	store, _, admin := newWebStore(t)
	srv, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	first := addWebStaff(t, store, "teacher", "BU1", "13987000001")
	second := addWebStaff(t, store, "community", "BU2", "13987000002")
	cookie, csrf := webSession(t, store, admin)

	rec := webRequest(t, srv, http.MethodGet, "/admin/users", cookie, nil, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("用户列表状态 = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`id="users-batch"`,
		`action="/admin/users/batch"`,
		`name="ids"`,
		`form="users-batch"`,
		`data-check-all="users-batch"`,
		`批量停用所选账号`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("用户列表缺少 %q", want)
		}
	}
	if !strings.Contains(body, `disabled title="不能停用自己的账号"`) {
		t.Fatal("当前管理员自己的复选框应禁用")
	}

	rec = webRequest(t, srv, http.MethodPost, "/admin/users/batch", cookie, url.Values{
		"ids":     {strconv.FormatInt(first.ID, 10), strconv.FormatInt(second.ID, 10)},
		"action":  {"soft_delete"},
		"confirm": {"1"},
		"reason":  {"批量离校测试"},
	}, csrf, "")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("批量停用状态 = %d，期望 303: %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/admin/users" {
		t.Fatalf("批量停用跳转 = %q", loc)
	}
	for _, id := range []int64{first.ID, second.ID} {
		u, err := store.GetUserByID(context.Background(), id)
		if err != nil {
			t.Fatalf("GetUserByID(%d): %v", id, err)
		}
		if u.Status != "soft_deleted" {
			t.Fatalf("用户 %d 状态 = %q", id, u.Status)
		}
	}
	if got, err := store.GetUserByID(context.Background(), admin.ID); err != nil || got.Status != "active" {
		t.Fatalf("当前管理员状态被改变: %+v, %v", got, err)
	}
}

func TestAdminUserBatchRequiresSelectionAndConfirmation(t *testing.T) {
	store, _, admin := newWebStore(t)
	srv, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	target := addWebStaff(t, store, "teacher", "BU3", "13987000003")
	cookie, csrf := webSession(t, store, admin)

	rec := webRequest(t, srv, http.MethodPost, "/admin/users/batch", cookie, url.Values{
		"action":  {"soft_delete"},
		"confirm": {"1"},
	}, csrf, "")
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "请先勾选至少一项") {
		t.Fatalf("空选择状态 = %d: %s", rec.Code, rec.Body.String())
	}

	rec = webRequest(t, srv, http.MethodPost, "/admin/users/batch", cookie, url.Values{
		"ids":    {strconv.FormatInt(target.ID, 10)},
		"action": {"soft_delete"},
	}, csrf, "")
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "我已核对所选账号并确认停用") {
		t.Fatalf("缺少确认状态 = %d: %s", rec.Code, rec.Body.String())
	}
	if got, err := store.GetUserByID(context.Background(), target.ID); err != nil || got.Status != "active" {
		t.Fatalf("未确认时账号被停用: %+v, %v", got, err)
	}
}

func TestAdminClassBatchRequiresPhraseAndDisbands(t *testing.T) {
	store, _, admin := newWebStore(t)
	srv, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	teacher := addWebStaff(t, store, "teacher", "BC1", "13987000011")
	counselor := addWebStaff(t, store, "counselor", "BC2", "13987000012")
	class := addWebClass(t, store, admin, teacher, counselor, "批量解散测试班")
	cookie, csrf := webSession(t, store, admin)

	rec := webRequest(t, srv, http.MethodGet, "/admin/classes", cookie, nil, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("班级列表状态 = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`id="classes-batch"`,
		`action="/admin/classes/batch"`,
		`name="confirm_text"`,
		`form="classes-batch"`,
		`批量解散所选班级`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("班级列表缺少 %q", want)
		}
	}

	rec = webRequest(t, srv, http.MethodPost, "/admin/classes/batch", cookie, url.Values{
		"ids":          {strconv.FormatInt(class.ID, 10)},
		"action":       {"disband"},
		"confirm":      {"1"},
		"confirm_text": {"解散"},
	}, csrf, "")
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "确认解散") {
		t.Fatalf("缺少确认短语状态 = %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := store.GetClass(context.Background(), class.ID); err != nil {
		t.Fatalf("未确认时班级被解散: %v", err)
	}

	rec = webRequest(t, srv, http.MethodPost, "/admin/classes/batch", cookie, url.Values{
		"ids":          {strconv.FormatInt(class.ID, 10)},
		"action":       {"disband"},
		"confirm":      {"1"},
		"confirm_text": {"确认解散"},
	}, csrf, "")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("批量解散状态 = %d，期望 303: %s", rec.Code, rec.Body.String())
	}
	if _, err := store.GetClass(context.Background(), class.ID); err == nil {
		t.Fatal("批量解散后班级仍存在")
	}
	var audits int
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM admin_audit_logs WHERE action='class.disband' AND target_id=?`, class.ID).Scan(&audits); err != nil {
		t.Fatalf("统计解散审计: %v", err)
	}
	if audits != 1 {
		t.Fatalf("解散审计行 = %d，期望 1", audits)
	}
}

func TestAdminRegistrationCodeBatchStateAndDelete(t *testing.T) {
	store, _, admin := newWebStore(t)
	srv, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	first, _ := createWebRegistrationCode(t, store, admin, []string{"teacher"}, 2)
	second, _ := createWebRegistrationCode(t, store, admin, []string{"counselor"}, 2)
	cookie, csrf := webSession(t, store, admin)

	rec := webRequest(t, srv, http.MethodGet, "/admin/settings/registration-codes", cookie, nil, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("注册码列表状态 = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`id="codes-batch"`,
		`action="/admin/settings/registration-codes/batch"`,
		`form="codes-batch"`,
		`data-check-all="codes-batch"`,
		`批量停用`,
		`批量启用`,
		`批量删除`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("注册码列表缺少 %q", want)
		}
	}

	ids := []string{strconv.FormatInt(first.ID, 10), strconv.FormatInt(second.ID, 10)}
	rec = webRequest(t, srv, http.MethodPost, "/admin/settings/registration-codes/batch", cookie, url.Values{
		"ids":    ids,
		"action": {"disable"},
	}, csrf, "")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("批量停用注册码状态 = %d: %s", rec.Code, rec.Body.String())
	}
	for _, id := range []int64{first.ID, second.ID} {
		if c := store.GetRegistrationCode(context.Background(), id); c.Active {
			t.Fatalf("注册码 %d 仍启用", id)
		}
	}

	rec = webRequest(t, srv, http.MethodPost, "/admin/settings/registration-codes/batch", cookie, url.Values{
		"ids":    ids,
		"action": {"enable"},
	}, csrf, "")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("批量启用注册码状态 = %d: %s", rec.Code, rec.Body.String())
	}
	for _, id := range []int64{first.ID, second.ID} {
		if c := store.GetRegistrationCode(context.Background(), id); !c.Active {
			t.Fatalf("注册码 %d 未启用", id)
		}
	}

	rec = webRequest(t, srv, http.MethodPost, "/admin/settings/registration-codes/batch", cookie, url.Values{
		"ids":    ids,
		"action": {"delete"},
	}, csrf, "")
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "我确认永久删除所选注册码") {
		t.Fatalf("缺少删除确认状态 = %d: %s", rec.Code, rec.Body.String())
	}

	rec = webRequest(t, srv, http.MethodPost, "/admin/settings/registration-codes/batch", cookie, url.Values{
		"ids":     ids,
		"action":  {"delete"},
		"confirm": {"1"},
	}, csrf, "")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("批量删除注册码状态 = %d: %s", rec.Code, rec.Body.String())
	}
	for _, id := range []int64{first.ID, second.ID} {
		if c := store.GetRegistrationCode(context.Background(), id); c.ID != 0 {
			t.Fatalf("注册码 %d 未删除", id)
		}
	}
	var audits int
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM admin_audit_logs WHERE action='regcode.delete'`).Scan(&audits); err != nil {
		t.Fatalf("统计删除审计: %v", err)
	}
	if audits != 2 {
		t.Fatalf("删除注册码审计行 = %d，期望 2", audits)
	}
}
