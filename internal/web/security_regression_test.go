package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"leavepass/internal/app"
)

var csrfInputRE = regexp.MustCompile(`name="csrf" value="([^"]*)"`)

func advanceWebClock(c *webClock, d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.unix += int64(d.Seconds())
}

func csrfCookieAndToken(t *testing.T, rec *httptest.ResponseRecorder) (*http.Cookie, string) {
	t.Helper()
	m := csrfInputRE.FindStringSubmatch(rec.Body.String())
	if len(m) != 2 || m[1] == "" {
		t.Fatalf("页面缺少非空 csrf 隐藏字段: %s", rec.Body.String())
	}
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieCSRF {
			cookie = c
		}
	}
	if cookie == nil || cookie.Value == "" {
		t.Fatal("页面未下发 lp_csrf cookie")
	}
	if cookie.Value != m[1] {
		t.Fatalf("csrf 字段 %q 与 cookie %q 不一致", m[1], cookie.Value)
	}
	return cookie, m[1]
}

func anonymousCSRF(t *testing.T, srv *Server) (*http.Cookie, string) {
	t.Helper()
	rec := webRequest(t, srv, http.MethodGet, "/login", nil, nil, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /login 状态 = %d", rec.Code)
	}
	return csrfCookieAndToken(t, rec)
}

func loginPOST(t *testing.T, srv *Server, cookie *http.Cookie, csrf, path, ident, password string) *httptest.ResponseRecorder {
	t.Helper()
	return webRequest(t, srv, http.MethodPost, path, cookie, url.Values{
		"ident":    {ident},
		"password": {password},
	}, csrf, "")
}

func TestAnonymousLoginPageHasCSRFAndNoUserBar(t *testing.T) {
	store, _, _ := newWebStore(t)
	srv, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rec := webRequest(t, srv, http.MethodGet, "/login", nil, nil, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /login 状态 = %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "退出") || strings.Contains(body, `class="userbox"`) {
		t.Fatal("匿名登录页不应渲染已登录用户栏/退出按钮")
	}
	cookie, token := csrfCookieAndToken(t, rec)
	if cookie.Value != token {
		t.Fatalf("csrf=%q cookie=%q", token, cookie.Value)
	}
}

func TestClassClaimRequiresMatchingClassName(t *testing.T) {
	store, _, _ := newWebStore(t)
	srv, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	teacher := addWebStaff(t, store, "teacher", "CT1", "13992000001")
	counselor := addWebStaff(t, store, "counselor", "CC1", "13992000002")
	class, err := store.CreateClass(context.Background(), counselor, "待认领回归班", "计算机系", false)
	if err != nil {
		t.Fatalf("CreateClass: %v", err)
	}
	if class.TeacherUserID != 0 {
		t.Fatal("测试班级应无班主任")
	}
	cookie, csrf := webSession(t, store, teacher)

	rec := webRequest(t, srv, http.MethodPost, "/t/classes/claim", cookie, url.Values{
		"class_id":   {strconv.FormatInt(class.ID, 10)},
		"class_name": {"错误班级名"},
	}, csrf, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("错误班级名状态 = %d，期望 403", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "提交的班级名") {
		t.Fatalf("403 页面缺少原因: %s", rec.Body.String())
	}
	if got, _ := store.GetClass(context.Background(), class.ID); got.TeacherUserID != 0 {
		t.Fatalf("错误班级名不应认领成功: teacher=%d", got.TeacherUserID)
	}

	rec = webRequest(t, srv, http.MethodPost, "/t/classes/claim", cookie, url.Values{
		"class_id":   {strconv.FormatInt(class.ID, 10)},
		"class_name": {class.Name},
	}, csrf, "")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("正确班级名状态 = %d，期望 303；%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/t/classes/"+itoa(class.ID) {
		t.Fatalf("Location = %q", got)
	}
	claimed, err := store.GetClass(context.Background(), class.ID)
	if err != nil {
		t.Fatalf("GetClass: %v", err)
	}
	if claimed.TeacherUserID != teacher.ID {
		t.Fatalf("认领后班主任 = %d，期望 %d", claimed.TeacherUserID, teacher.ID)
	}
	if claimed.Unclaimed() || claimed.DeleteDueAt != 0 {
		t.Fatalf("认领后待认领状态未清除: %+v", claimed)
	}
}

func TestLoginLockoutIsPerIPAndAccount(t *testing.T) {
	store, _, _ := newWebStore(t)
	srv, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	studentA := addWebStudent(t, store, "锁定学生甲", "20246001", 0, "")
	studentB := addWebStudent(t, store, "锁定学生乙", "20246002", 0, "")
	cookie, csrf := anonymousCSRF(t, srv)

	for i := 0; i < 10; i++ {
		rec := loginPOST(t, srv, cookie, csrf, "/login", studentA.StudentNo, "wrong-password")
		want := http.StatusUnauthorized
		if i == 9 {
			want = http.StatusTooManyRequests
		}
		if rec.Code != want {
			t.Fatalf("账号 A 第 %d 次失败状态 = %d，期望 %d；%s", i+1, rec.Code, want, rec.Body.String())
		}
	}

	probe := httptest.NewRequest(http.MethodPost, "/login", nil)
	keyA := loginAttemptKeyStudent(probe, studentA.StudentNo)
	keyB := loginAttemptKeyStudent(probe, studentB.StudentNo)
	untilA, err := store.LoginLocked(context.Background(), "login_ip", keyA)
	if err != nil || untilA <= store.NowUnix() {
		t.Fatalf("账号 A 应处于锁定: until=%d err=%v", untilA, err)
	}

	// 同一 IP 用账号 B 成功登录，只会清除 B 的计数，不能解锁 A。
	rec := loginPOST(t, srv, cookie, csrf, "/login", studentB.StudentNo, "Passw0rd!")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("账号 B 登录状态 = %d，期望 303；%s", rec.Code, rec.Body.String())
	}
	untilA, err = store.LoginLocked(context.Background(), "login_ip", keyA)
	if err != nil || untilA <= store.NowUnix() {
		t.Fatalf("账号 B 成功登录后 A 的锁定被清除: until=%d err=%v", untilA, err)
	}
	if untilB, _ := store.LoginLocked(context.Background(), "login_ip", keyB); untilB != 0 {
		t.Fatalf("账号 B 成功后仍有锁定: %d", untilB)
	}
	if n := countLoginRows(t, store, "login_ip", keyA); n != 1 {
		t.Fatalf("账号 A 的 login_attempts 行 = %d", n)
	}
}

func countLoginRows(t *testing.T, store *app.Store, scope, key string) int {
	t.Helper()
	var n int
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM login_attempts WHERE scope=? AND key=? AND COALESCE(locked_until,0)>?`,
		scope, key, store.NowUnix()).Scan(&n); err != nil {
		t.Fatalf("count login_attempts: %v", err)
	}
	return n
}

func TestAdminLoginStillLocksIPAfterFourFailures(t *testing.T) {
	store, _, _ := newWebStore(t)
	srv, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cookie, csrf := anonymousCSRF(t, srv)
	for i := 0; i < 4; i++ {
		rec := loginPOST(t, srv, cookie, csrf, "/admin/login", "admin", "wrong-password")
		want := http.StatusUnauthorized
		if i == 3 {
			want = http.StatusTooManyRequests
		}
		if rec.Code != want {
			t.Fatalf("管理员第 %d 次失败状态 = %d，期望 %d", i+1, rec.Code, want)
		}
	}
	probe := httptest.NewRequest(http.MethodPost, "/admin/login", nil)
	ip := clientIP(probe)
	until, _ := store.LoginLocked(context.Background(), "admin_login_ip", ip)
	if until <= store.NowUnix() {
		t.Fatalf("管理员登录成功后应锁定 IP %s，until=%d", ip, until)
	}
}

func TestArchivedLeaveDetailCommunitySeesReasonButNotPhone(t *testing.T) {
	store, clock, admin := newWebStore(t)
	srv, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	teacher := addWebStaff(t, store, "teacher", "PT1", "13992000011")
	counselor := addWebStaff(t, store, "counselor", "PC1", "13992000012")
	community := addWebStaff(t, store, "community", "PM1", "13992000013")
	class := addWebClass(t, store, admin, teacher, counselor, "隐私保留班")
	student := addWebStudent(t, store, "隐私学生", "20246101", class.ID, "21-1601")
	now := clock.Now().Unix()

	const (
		reason       = "隐私测试原因文本"
		majorOpinion = "隐私班主任意见"
		contactPhone = "13800000009"
	)
	leaveID, err := store.SubmitLeave(context.Background(), student, app.SubmitLeaveInput{
		ContactPhone: contactPhone, Reason: reason, StartAt: now + 3600, EndAt: now + 7200,
	})
	if err != nil {
		t.Fatalf("SubmitLeave: %v", err)
	}
	if err := store.DecideLeave(context.Background(), teacher, leaveID, app.DecideInput{
		Decision: "approved", ApproverName: "隐私审批人", MajorOpinion: majorOpinion,
	}); err != nil {
		t.Fatalf("DecideLeave: %v", err)
	}
	if err := store.ExitTeacher(context.Background(), teacher); err != nil {
		t.Fatalf("ExitTeacher: %v", err)
	}
	advanceWebClock(clock, time.Duration(app.UnclaimedTTL)*time.Second)
	if err := store.UnclaimedTick(context.Background()); err != nil {
		t.Fatalf("UnclaimedTick: %v", err)
	}
	if err := store.SetCommunityBuildings(context.Background(), community.ID, []string{"21"}); err != nil {
		t.Fatalf("SetCommunityBuildings: %v", err)
	}
	// Active approved leave: community sees reason/opinions but not phone.
	activeTeacher := addWebStaff(t, store, "teacher", "PT2", "13992000014")
	activeCounselor := addWebStaff(t, store, "counselor", "PC2", "13992000015")
	activeClass := addWebClass(t, store, admin, activeTeacher, activeCounselor, "隐私活动班")
	activeStudent := addWebStudent(t, store, "活动学生", "20246102", activeClass.ID, "22-1602")
	if err := store.SetCommunityBuildings(context.Background(), community.ID, []string{"21", "22"}); err != nil {
		t.Fatalf("SetCommunityBuildings: %v", err)
	}
	communityCookie, _ := webSession(t, store, community)
	activeNow := clock.Now().Unix()
	activeID, err := store.SubmitLeave(context.Background(), activeStudent, app.SubmitLeaveInput{
		ContactPhone: contactPhone, Reason: reason, StartAt: activeNow + 3600, EndAt: activeNow + 7200,
	})
	if err != nil {
		t.Fatalf("SubmitLeave active: %v", err)
	}
	if err := store.DecideLeave(context.Background(), activeTeacher, activeID, app.DecideInput{
		Decision: "approved", ApproverName: "活动审批人", MajorOpinion: majorOpinion,
	}); err != nil {
		t.Fatalf("DecideLeave active: %v", err)
	}
	recActive := webRequest(t, srv, http.MethodGet, "/c/leaves/"+itoa(activeID), communityCookie, nil, "", "")
	if recActive.Code != http.StatusOK {
		t.Fatalf("社区老师活动假条状态 = %d；%s", recActive.Code, recActive.Body.String())
	}
	activeBody := recActive.Body.String()
	for _, want := range []string{reason, majorOpinion} {
		if !strings.Contains(activeBody, want) {
			t.Fatalf("社区老师活动假条缺少字段 %q", want)
		}
	}
	if strings.Contains(activeBody, contactPhone) {
		t.Fatalf("社区老师活动假条泄露联系电话 %q", contactPhone)
	}

	archived, err := store.GetLeave(context.Background(), leaveID)
	if err != nil || archived.ArchivedAt == 0 {
		t.Fatalf("假条未归档: %+v err=%v", archived, err)
	}

	path := "/archived/leaves/" + itoa(leaveID)
	rec := webRequest(t, srv, http.MethodGet, path, communityCookie, nil, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("社区老师保留假条状态 = %d；%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{reason, majorOpinion} {
		if !strings.Contains(body, want) {
			t.Fatalf("社区老师保留假条缺少字段 %q", want)
		}
	}
	if strings.Contains(body, contactPhone) {
		t.Fatalf("社区老师页面泄露联系电话 %q", contactPhone)
	}

	adminCookie, _ := webSession(t, store, admin)
	rec = webRequest(t, srv, http.MethodGet, path, adminCookie, nil, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("管理员保留假条状态 = %d", rec.Code)
	}
	adminBody := rec.Body.String()
	for _, want := range []string{reason, majorOpinion, contactPhone} {
		if !strings.Contains(adminBody, want) {
			t.Fatalf("管理员页面缺少字段 %q", want)
		}
	}
}

func TestAdminAssignTeacherRequiresConfirmMove(t *testing.T) {
	store, _, admin := newWebStore(t)
	srv, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	teacher := addWebStaff(t, store, "teacher", "AT1", "13992000021")
	counselor := addWebStaff(t, store, "counselor", "AC1", "13992000022")
	oldClass := addWebClass(t, store, admin, teacher, app.User{}, "指派原班")
	target, err := store.CreateClass(context.Background(), counselor, "指派目标班", "计算机系", false)
	if err != nil {
		t.Fatalf("CreateClass(target): %v", err)
	}
	adminCookie, csrf := webSession(t, store, admin)
	path := "/admin/classes/" + itoa(target.ID) + "/teacher"

	rec := webRequest(t, srv, http.MethodPost, path, adminCookie, url.Values{
		"teacher_id": {strconv.FormatInt(teacher.ID, 10)},
	}, csrf, "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("未确认移出原班状态 = %d，期望 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "确认") {
		t.Fatalf("409 页面缺少确认提示: %s", rec.Body.String())
	}
	if got, _ := store.GetClass(context.Background(), oldClass.ID); got.TeacherUserID != teacher.ID {
		t.Fatalf("未确认时原班班主任已变化: %d", got.TeacherUserID)
	}

	rec = webRequest(t, srv, http.MethodPost, path, adminCookie, url.Values{
		"teacher_id":   {strconv.FormatInt(teacher.ID, 10)},
		"confirm_move": {"1"},
	}, csrf, "")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("确认后状态 = %d，期望 303；%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/admin/classes/"+itoa(target.ID) {
		t.Fatalf("Location = %q", got)
	}
	gotTarget, err := store.GetClass(context.Background(), target.ID)
	if err != nil {
		t.Fatalf("GetClass(target): %v", err)
	}
	if gotTarget.TeacherUserID != teacher.ID {
		t.Fatalf("目标班班主任 = %d，期望 %d", gotTarget.TeacherUserID, teacher.ID)
	}
	gotOld, err := store.GetClass(context.Background(), oldClass.ID)
	if err != nil {
		t.Fatalf("GetClass(old): %v", err)
	}
	if gotOld.TeacherUserID != 0 || gotOld.DeleteDueAt <= store.NowUnix() {
		t.Fatalf("原班未进入待认领: %+v", gotOld)
	}
}

func TestSameOriginAllowsNullOriginButRejectsCrossOrigin(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "https://leave.example.com/admin/login", nil)
	r.Host = "leave.example.com"

	r.Header.Set("Origin", "null")
	if !sameOrigin(r) {
		t.Fatal("Origin: null 应被接受（CSRF token 仍会校验）")
	}

	r.Header.Set("Origin", "https://leave.example.com")
	if !sameOrigin(r) {
		t.Fatal("同源 Origin 应被接受")
	}

	r.Header.Set("Origin", "https://evil.example")
	if sameOrigin(r) {
		t.Fatal("跨源 Origin 不应被接受")
	}
}

func TestLoginAttemptKeysNormalizeVariants(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/login", nil)
	req.RemoteAddr = "198.51.100.91:12345"
	if got, want := loginAttemptKeyStudent(req, "２０２４６００１"), loginAttemptKeyStudent(req, "20246001"); got != want {
		t.Fatalf("student key 未归一化: %q != %q", got, want)
	}
	if got, want := loginAttemptKeyStaff(req, "139-8000-0001"), loginAttemptKeyStaff(req, "13980000001"); got != want {
		t.Fatalf("staff key 未归一化: %q != %q", got, want)
	}
}

func TestLoginGlobalIPLimitBlocksBeforePasswordCheck(t *testing.T) {
	store, clock, _ := newWebStore(t)
	srv, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cookie, csrf := anonymousCSRF(t, srv)
	now := clock.Now().Unix()
	if _, err := store.DB.Exec(`INSERT INTO login_attempts
		(key,scope,window_start,failed_count,locked_until,updated_at) VALUES (?,?,?,?,?,?)`,
		"192.0.2.1", "login_global_ip", now, 0, now+600, now); err != nil {
		t.Fatalf("seed global lock: %v", err)
	}
	rec := loginPOST(t, srv, cookie, csrf, "/login", "20246001", "wrong-password")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("全局限流状态 = %d，期望 429；%s", rec.Code, rec.Body.String())
	}
}

func TestProfileRevokeOtherSessions(t *testing.T) {
	store, _, _ := newWebStore(t)
	srv, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	student := addWebStudent(t, store, "设备学生", "20246101", 0, "")
	anonCookie, anonCSRF := anonymousCSRF(t, srv)
	rec := loginPOST(t, srv, anonCookie, anonCSRF, "/login", student.StudentNo, "Passw0rd!")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("登录状态 = %d；%s", rec.Code, rec.Body.String())
	}
	var sess *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieSession {
			sess = c
		}
	}
	if sess == nil {
		t.Fatal("登录未下发会话 cookie")
	}
	other, err := store.CreateSession(context.Background(), student.ID, "198.51.100.99", "other-device")
	if err != nil {
		t.Fatalf("CreateSession other: %v", err)
	}
	rec = webRequest(t, srv, http.MethodGet, "/s/profile", sess, nil, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /s/profile = %d", rec.Code)
	}
	m := csrfInputRE.FindStringSubmatch(rec.Body.String())
	if len(m) != 2 || m[1] == "" {
		t.Fatalf("个人资料页缺少 csrf: %s", rec.Body.String())
	}
	rec = webRequest(t, srv, http.MethodPost, "/s/profile/sessions/revoke", sess, url.Values{}, m[1], "")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("撤销其他设备状态 = %d；%s", rec.Code, rec.Body.String())
	}
	if _, err := store.LookupSession(context.Background(), other); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("其他设备会话仍有效: %v", err)
	}
	rec = webRequest(t, srv, http.MethodGet, "/s/profile", sess, nil, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("当前会话被误杀: %d", rec.Code)
	}
}
