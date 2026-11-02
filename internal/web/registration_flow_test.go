package web

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"leavepass/internal/app"
)

func createWebRegistrationCode(t *testing.T, store *app.Store, admin app.User, roles []string, maxUses int64) (app.RegistrationCode, string) {
	t.Helper()
	c, plain, err := store.CreateRegistrationCode(context.Background(), admin, roles, maxUses, 0, 0, "", "127.0.0.1")
	if err != nil {
		t.Fatalf("CreateRegistrationCode: %v", err)
	}
	return c, plain
}

func TestStaffRegisterHidesRolesUntilCodeIsValid(t *testing.T) {
	store, _, _ := newWebStore(t)
	srv, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rec := webRequest(t, srv, http.MethodGet, "/staff/register", nil, nil, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /staff/register 状态 = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, role := range []string{"班主任", "辅导员", "社区老师"} {
		if strings.Contains(body, role) {
			t.Fatalf("未校验注册码的页面泄露角色 %q", role)
		}
	}
	if strings.Contains(body, `name="role"`) {
		t.Fatal("未校验注册码的页面不应包含角色字段")
	}
	if !strings.Contains(body, `name="step" value="code"`) || !strings.Contains(body, `name="code"`) {
		t.Fatalf("注册页缺少第一步注册码表单: %s", body)
	}

	cookie, csrf := anonymousCSRF(t, srv)
	rec = webRequest(t, srv, http.MethodPost, "/staff/register", cookie, url.Values{
		"step": {"code"},
		"code": {"AAAA-BBBB-CCCC"},
	}, csrf, "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("无效注册码状态 = %d，期望 422", rec.Code)
	}
	body = rec.Body.String()
	if !strings.Contains(body, app.ErrCodeUnavailable.Error()) {
		t.Fatalf("无效注册码页面缺少统一错误: %s", body)
	}
	for _, role := range []string{"班主任", "辅导员", "社区老师"} {
		if strings.Contains(body, role) {
			t.Fatalf("无效注册码页面泄露角色 %q", role)
		}
	}
}

func TestStaffRegisterSingleRoleIsFixed(t *testing.T) {
	store, _, admin := newWebStore(t)
	srv, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, plain := createWebRegistrationCode(t, store, admin, []string{"teacher"}, 1)

	cookie, csrf := anonymousCSRF(t, srv)
	rec := webRequest(t, srv, http.MethodPost, "/staff/register", cookie, url.Values{
		"step": {"code"},
		"code": {plain},
	}, csrf, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("校验单角色码状态 = %d，期望 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "班主任") || !strings.Contains(body, `name="role" value="teacher"`) {
		t.Fatalf("单角色码未固定班主任: %s", body)
	}
	if strings.Contains(body, `<select name="role"`) {
		t.Fatal("单角色码不应渲染角色下拉框")
	}
	for _, other := range []string{"辅导员", "社区老师"} {
		if strings.Contains(body, other) {
			t.Fatalf("单角色码页面泄露其他角色 %q", other)
		}
	}
}

func TestStaffRegisterMultiRoleShowsOnlyAllowedRoles(t *testing.T) {
	store, _, admin := newWebStore(t)
	srv, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, plain := createWebRegistrationCode(t, store, admin, []string{"teacher", "community"}, 2)

	cookie, csrf := anonymousCSRF(t, srv)
	rec := webRequest(t, srv, http.MethodPost, "/staff/register", cookie, url.Values{
		"step": {"code"},
		"code": {plain},
	}, csrf, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("校验多角色码状态 = %d，期望 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `<select name="role"`) {
		t.Fatalf("多角色码应渲染角色下拉框: %s", body)
	}
	for _, allowed := range []string{`value="teacher"`, `value="community"`, "班主任", "社区老师"} {
		if !strings.Contains(body, allowed) {
			t.Fatalf("多角色码页面缺少允许角色 %q", allowed)
		}
	}
	if strings.Contains(body, `value="counselor"`) || strings.Contains(body, "辅导员") {
		t.Fatal("多角色码页面泄露了未允许的辅导员角色")
	}
}

func TestStaffRegisterSuccessRedirectsToPrefilledLogin(t *testing.T) {
	store, _, admin := newWebStore(t)
	srv, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	code, plain := createWebRegistrationCode(t, store, admin, []string{"teacher"}, 1)

	cookie, csrf := anonymousCSRF(t, srv)
	rec := webRequest(t, srv, http.MethodPost, "/staff/register", cookie, url.Values{
		"step":      {"register"},
		"code":      {plain},
		"role":      {"teacher"},
		"username":  {"teach1"},
		"phone":     {"13911112222"},
		"password":  {"Passw0rd"},
		"password2": {"Passw0rd"},
	}, csrf, "")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("注册状态 = %d，期望 303: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/staff/login?username=teach1" {
		t.Fatalf("注册后 Location = %q", got)
	}
	u, err := store.GetUserByUsername(context.Background(), "teach1")
	if err != nil {
		t.Fatalf("读取新账号: %v", err)
	}
	if u.Role != "teacher" || u.Status != "active" {
		t.Fatalf("新账号角色/状态 = %s/%s", u.Role, u.Status)
	}
	if got := store.GetRegistrationCode(context.Background(), code.ID).UsedCount; got != 1 {
		t.Fatalf("注册后次数 = %d，期望 1", got)
	}

	rec = webRequest(t, srv, http.MethodGet, "/staff/login?username=teach1", nil, nil, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("登录页状态 = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `name="ident" value="teach1"`) {
		t.Fatalf("登录页未预填用户名: %s", body)
	}
	passwordInput := regexp.MustCompile(`<input type="password" name="password"[^>]*>`).FindString(body)
	if !strings.Contains(passwordInput, "autofocus") {
		t.Fatalf("预填用户名后密码框应自动聚焦: %q", passwordInput)
	}
	identInput := regexp.MustCompile(`<input name="ident"[^>]*>`).FindString(body)
	if strings.Contains(identInput, "autofocus") {
		t.Fatalf("预填用户名后用户名框不应自动聚焦: %q", identInput)
	}
}

func TestStaffRegisterMultiRoleRequiresAllowedSelection(t *testing.T) {
	store, _, admin := newWebStore(t)
	srv, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	code, plain := createWebRegistrationCode(t, store, admin, []string{"teacher", "counselor"}, 2)
	cookie, csrf := anonymousCSRF(t, srv)

	rec := webRequest(t, srv, http.MethodPost, "/staff/register", cookie, url.Values{
		"step":      {"register"},
		"code":      {plain},
		"username":  {"norole"},
		"phone":     {"13911113333"},
		"password":  {"Passw0rd"},
		"password2": {"Passw0rd"},
	}, csrf, "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("未选角色状态 = %d，期望 422", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "请选择注册角色") {
		t.Fatalf("未选角色页面缺少提示: %s", body)
	}
	if strings.Contains(body, "社区老师") {
		t.Fatal("未选角色的重渲染不应包含未允许角色")
	}

	rec = webRequest(t, srv, http.MethodPost, "/staff/register", cookie, url.Values{
		"step":      {"register"},
		"code":      {plain},
		"role":      {"community"},
		"username":  {"badsel"},
		"phone":     {"13911114444"},
		"password":  {"Passw0rd"},
		"password2": {"Passw0rd"},
	}, csrf, "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("越权角色状态 = %d，期望 422", rec.Code)
	}
	body = rec.Body.String()
	if !strings.Contains(body, "不在该注册码允许范围内") {
		t.Fatalf("越权角色页面缺少提示: %s", body)
	}
	if strings.Contains(body, `value="community"`) || strings.Contains(body, "社区老师") {
		t.Fatal("越权角色被回显到页面")
	}
	if got := store.GetRegistrationCode(context.Background(), code.ID).UsedCount; got != 0 {
		t.Fatalf("失败注册消耗次数 = %d，期望 0", got)
	}
	if _, err := store.GetUserByUsername(context.Background(), "norole"); err == nil {
		t.Fatal("未选角色不应创建账号")
	}
	if _, err := store.GetUserByUsername(context.Background(), "badsel"); err == nil {
		t.Fatal("越权角色不应创建账号")
	}
}

func TestAdminCreatesMultiRoleRegistrationCode(t *testing.T) {
	store, _, admin := newWebStore(t)
	srv, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cookie, csrf := webSession(t, store, admin)

	rec := webRequest(t, srv, http.MethodPost, "/admin/settings/registration-codes", cookie, url.Values{
		"roles":       {"teacher", "counselor"},
		"max_uses":    {"3"},
		"valid_until": {""},
		"note":        {"多角色测试"},
	}, csrf, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("创建多角色码状态 = %d，期望 200: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "注册码已生成") {
		t.Fatalf("创建后未显示明文: %s", rec.Body.String())
	}
	codes, err := store.ListRegistrationCodes(context.Background())
	if err != nil {
		t.Fatalf("ListRegistrationCodes: %v", err)
	}
	var got *app.RegistrationCode
	for i := range codes {
		if strings.Join(codes[i].Roles, ",") == "teacher,counselor" {
			got = &codes[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("未找到多角色注册码: %+v", codes)
	}
	if got.MaxUses != 3 || got.Role != "teacher" || !got.AllowsRole("counselor") {
		t.Fatalf("多角色注册码字段 = %+v", got)
	}

	rec = webRequest(t, srv, http.MethodPost, "/admin/settings/registration-codes", cookie, url.Values{
		"max_uses": {"1"},
	}, csrf, "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("未选角色状态 = %d，期望 422", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "请至少选择一个注册角色") {
		t.Fatalf("未选角色缺少提示: %s", rec.Body.String())
	}
}
