package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"leavepass/internal/app"
	"leavepass/internal/config"
	"leavepass/internal/secure"
	"leavepass/internal/validate"
)

type webClock struct {
	mu   sync.Mutex
	unix int64
}

func (c *webClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Unix(c.unix, 0).In(config.Location)
}

func (c *webClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.unix = t.Unix()
}

func newWebStore(t *testing.T) (*app.Store, *webClock, app.User) {
	t.Helper()
	clock := &webClock{unix: time.Date(2026, 3, 15, 10, 0, 0, 0, config.Location).Unix()}
	cfg := config.Config{
		BaseURL:            "http://leave.test",
		DBPath:             filepath.Join(t.TempDir(), "web.db"),
		SessionSecret:      []byte("0123456789abcdef0123456789abcdef"),
		RegistrationSecret: []byte("abcdef0123456789abcdef0123456789"),
		AdminInitialName:   "Admin",
		AdminInitialPass:   "AA",
		AdminInitialPhone:  "13800000000",
		CampusBuildings:    []string{"21", "22"},
		CookieSecure:       false,
		Now:                clock.Now,
	}
	store, err := app.Open(cfg)
	if err != nil {
		t.Fatalf("app.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	admin, err := store.GetUserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatalf("seeded admin: %v", err)
	}
	return store, clock, admin
}

func webHash(t *testing.T, pw string) string {
	t.Helper()
	h, err := secure.HashPassword(pw)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	return h
}

func addWebStaff(t *testing.T, store *app.Store, role, username, phone string) app.User {
	t.Helper()
	display, login, err := validate.NormalizeUsername(username)
	if err != nil {
		t.Fatalf("NormalizeUsername: %v", err)
	}
	p, err := validate.NormalizePhone(phone)
	if err != nil {
		t.Fatalf("NormalizePhone: %v", err)
	}
	now := store.NowUnix()
	res, err := store.DB.Exec(`INSERT INTO users
		(phone,username,username_norm,password_hash,role,status,password_is_initial,created_at,updated_at)
		VALUES (?,?,?,?,?, 'active', 0, ?,?)`,
		p, display, login, webHash(t, "Passw0rd!"), role, now, now)
	if err != nil {
		t.Fatalf("insert staff: %v", err)
	}
	id, _ := res.LastInsertId()
	u, err := store.GetUserByID(context.Background(), id)
	if err != nil {
		t.Fatalf("load staff: %v", err)
	}
	return u
}

func addWebStudent(t *testing.T, store *app.Store, name, no string, classID int64, dorm string) app.User {
	t.Helper()
	display, norm, err := validate.NormalizeStudentNo(no)
	if err != nil {
		t.Fatalf("NormalizeStudentNo: %v", err)
	}
	now := store.NowUnix()
	res, err := store.DB.Exec(`INSERT INTO users
		(username,username_norm,student_no,student_no_norm,password_hash,role,status,password_is_initial,created_at,updated_at)
		VALUES (?,?,?,?,?, 'student', 'active', 1, ?,?)`,
		name, strings.ToLower(name), display, norm, webHash(t, "Passw0rd!"), now, now)
	if err != nil {
		t.Fatalf("insert student: %v", err)
	}
	id, _ := res.LastInsertId()
	if classID != 0 {
		building, room := "", ""
		if dorm != "" {
			building, room, err = validate.SplitDorm(dorm)
			if err != nil {
				t.Fatalf("SplitDorm: %v", err)
			}
		}
		if _, err := store.DB.Exec(`INSERT INTO student_classes
			(user_id,class_id,dorm_building,dorm_room,imported_at,joined_at) VALUES (?,?,?,?,?,?)`,
			id, classID, building, room, now, now); err != nil {
			t.Fatalf("insert student class: %v", err)
		}
	}
	u, err := store.GetUserByID(context.Background(), id)
	if err != nil {
		t.Fatalf("load student: %v", err)
	}
	return u
}

func addWebClass(t *testing.T, store *app.Store, admin, teacher, counselor app.User, name string) app.Class {
	t.Helper()
	c, err := store.CreateClass(context.Background(), teacher, name, "计算机系", false)
	if err != nil {
		t.Fatalf("CreateClass: %v", err)
	}
	if counselor.ID != 0 {
		if err := store.AssignCounselor(context.Background(), admin, c.ID, counselor.ID, "127.0.0.1"); err != nil {
			t.Fatalf("AssignCounselor: %v", err)
		}
	}
	c, err = store.GetClass(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("GetClass: %v", err)
	}
	return c
}

func webSession(t *testing.T, store *app.Store, u app.User) (*http.Cookie, string) {
	t.Helper()
	raw, err := store.CreateSession(context.Background(), u.ID, "127.0.0.1", "web-test")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return &http.Cookie{Name: cookieSession, Value: raw, Path: "/"}, store.CSRFFor(secure.SHA256Hex(raw))
}

func webRequest(t *testing.T, srv *Server, method, path string, cookie *http.Cookie, form url.Values, csrf string, origin string) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if form == nil {
		form = url.Values{}
	}
	if csrf != "" {
		form.Set("csrf", csrf)
	}
	if method == http.MethodPost {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, path, body)
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestUnauthenticatedRoutesRedirectToRoleLogin(t *testing.T) {
	store, _, _ := newWebStore(t)
	srv, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	tests := []struct {
		path string
		want string
	}{
		{path: "/s", want: "/login"},
		{path: "/s/leaves", want: "/login"},
		{path: "/t", want: "/staff/login"},
		{path: "/c", want: "/staff/login"},
		{path: "/admin", want: "/admin/login"},
		{path: "/archived/leaves", want: "/staff/login"},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			rec := webRequest(t, srv, http.MethodGet, tc.path, nil, nil, "", "")
			// require 使用 SeeOther(303)。
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("状态 = %d，期望 303", rec.Code)
			}
			if got := rec.Header().Get("Location"); got != tc.want {
				t.Fatalf("Location = %q，期望 %q", got, tc.want)
			}
		})
	}
}

func TestLoginPageRendersCSRFToken(t *testing.T) {
	store, _, _ := newWebStore(t)
	srv, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rec := webRequest(t, srv, http.MethodGet, "/login", nil, nil, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /login 状态 = %d", rec.Code)
	}
	m := regexp.MustCompile(`name="csrf" value="([^"]*)"`).FindStringSubmatch(rec.Body.String())
	if len(m) != 2 {
		t.Fatalf("登录页缺少 csrf 隐藏字段")
	}
	var cookieToken string
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieCSRF {
			cookieToken = c.Value
		}
	}
	if cookieToken == "" {
		t.Fatal("登录页未下发 lp_csrf cookie")
	}
	if m[1] != cookieToken {
		t.Fatalf("登录页 csrf=%q 与 cookie=%q 不一致", m[1], cookieToken)
	}
}

func TestRolePermissionMatrixOnRoutes(t *testing.T) {
	store, _, admin := newWebStore(t)
	srv, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	teacher1 := addWebStaff(t, store, "teacher", "WT1", "13990000001")
	teacher2 := addWebStaff(t, store, "teacher", "WT2", "13990000002")
	student := addWebStudent(t, store, "网页学生", "20243001", 0, "")
	class1 := addWebClass(t, store, admin, teacher1, app.User{}, "网页权限1班")
	class2 := addWebClass(t, store, admin, teacher2, app.User{}, "网页权限2班")
	_ = class1

	t.Run("学生访问管理员页 403 且带中文原因", func(t *testing.T) {
		cookie, _ := webSession(t, store, student)
		rec := webRequest(t, srv, http.MethodGet, "/admin", cookie, nil, "", "")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("状态 = %d，期望 403", rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "无权访问") || !strings.Contains(body, "学生") {
			t.Fatalf("403 页面缺少中文原因: %s", body)
		}
	})

	t.Run("学生访问教师页 403", func(t *testing.T) {
		cookie, _ := webSession(t, store, student)
		rec := webRequest(t, srv, http.MethodGet, "/t", cookie, nil, "", "")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("状态 = %d，期望 403", rec.Code)
		}
	})

	t.Run("教师访问他人班级 403 且带原因", func(t *testing.T) {
		cookie, _ := webSession(t, store, teacher1)
		rec := webRequest(t, srv, http.MethodGet, "/t/classes/"+itoa(class2.ID), cookie, nil, "", "")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("状态 = %d，期望 403", rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "无权访问班级") || !strings.Contains(body, "班主任或辅导员") {
			t.Fatalf("403 页面缺少中文原因: %s", body)
		}
	})

	t.Run("教师访问本班详情 200", func(t *testing.T) {
		cookie, _ := webSession(t, store, teacher1)
		rec := webRequest(t, srv, http.MethodGet, "/t/classes/"+itoa(class1.ID), cookie, nil, "", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("状态 = %d，期望 200", rec.Code)
		}
	})

	t.Run("社区老师访问管理员页 403", func(t *testing.T) {
		community := addWebStaff(t, store, "community", "WC1", "13990000003")
		cookie, _ := webSession(t, store, community)
		rec := webRequest(t, srv, http.MethodGet, "/admin", cookie, nil, "", "")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("状态 = %d，期望 403", rec.Code)
		}
	})
}

func TestCSRFProtection(t *testing.T) {
	store, _, _ := newWebStore(t)
	srv, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	student := addWebStudent(t, store, "CSRF学生", "20243011", 0, "")
	cookie, csrf := webSession(t, store, student)
	su, err := store.LookupSession(context.Background(), cookie.Value)
	if err != nil {
		t.Fatalf("测试会话无效: %v", err)
	}
	if !store.CheckCSRF(su.Session.TokenHash, csrf) {
		t.Fatalf("测试 CSRF 派生不一致")
	}

	t.Run("缺少 token 403", func(t *testing.T) {
		rec := webRequest(t, srv, http.MethodPost, "/s/notifications/read", cookie, nil, "", "")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("状态 = %d，期望 403", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "表单已过期") {
			t.Fatalf("缺少原因: %s", rec.Body.String())
		}
	})

	t.Run("错误 token 403", func(t *testing.T) {
		rec := webRequest(t, srv, http.MethodPost, "/s/notifications/read", cookie, nil, "wrong-token", "")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("状态 = %d，期望 403", rec.Code)
		}
	})

	t.Run("正确 token 通过", func(t *testing.T) {
		rec := webRequest(t, srv, http.MethodPost, "/s/notifications/read", cookie, nil, csrf, "")
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("状态 = %d，期望 303", rec.Code)
		}
		if got := rec.Header().Get("Location"); got != "/s/notifications" {
			t.Fatalf("Location = %q", got)
		}
	})

	t.Run("跨站 Origin 403", func(t *testing.T) {
		rec := webRequest(t, srv, http.MethodPost, "/s/notifications/read", cookie, nil, csrf, "https://evil.example")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("状态 = %d，期望 403", rec.Code)
		}
	})

	t.Run("无会话 POST 403", func(t *testing.T) {
		// 未登录时 withCSRF 会生成随机 token；请求体不带 token 必须拒绝。
		rec := webRequest(t, srv, http.MethodPost, "/login", nil, nil, "", "")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("状态 = %d，期望 403", rec.Code)
		}
	})
}
