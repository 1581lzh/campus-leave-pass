package web

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"leavepass/internal/app"
)

// renderCase is one GET request in the render matrix.
//
// kind selects the body assertion because a few GET routes return binaries
// (QR PNG, XLSX template, Excel export) or JSON instead of a rendered layout.
type renderCase struct {
	name   string
	path   string
	kind   string // html (default), png, xlsx, csv, json
	cookie *http.Cookie
	status int // 0 means 200
}

// TestRenderMatrix exercises every GET page route with a realistically populated
// store and inspects the slog output for template runtime errors. Handlers log
// template execution failures but still return 200, so status-code assertions
// alone cannot catch broken templates.
func TestRenderMatrix(t *testing.T) {
	store, _, admin := newWebStore(t)
	srv, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Capture every render/panic log. The writers used by the handlers pick up
	// the new default logger through slog.Default at call time.
	var logBuf bytes.Buffer
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(oldLogger)

	ctx := context.Background()
	now := store.NowUnix()
	const ip = "127.0.0.1"

	// --- Staff and students -------------------------------------------------
	teacher := addWebStaff(t, store, "teacher", "RT1", "13991000001")
	counselor := addWebStaff(t, store, "counselor", "RC1", "13991000002")
	community := addWebStaff(t, store, "community", "RM1", "13991000003")

	class := addWebClass(t, store, admin, teacher, counselor, "渲染测试1班")
	student1 := addWebStudent(t, store, "渲染学生甲", "20268001", class.ID, "21-601")
	student2 := addWebStudent(t, store, "渲染学生乙", "20268002", class.ID, "21-602")
	studentNoClass := addWebStudent(t, store, "渲染无班学生", "20268003", 0, "")

	// A class without a teacher, created by a counselor, renders the
	// "待认领 / 倒计时" branches on the admin pages.
	unclaimed, err := store.CreateClass(ctx, counselor, "渲染待认领班", "计算机系", false)
	if err != nil {
		t.Fatalf("CreateClass(unclaimed): %v", err)
	}

	// --- Community buildings ------------------------------------------------
	if err := store.SetCommunityBuildings(ctx, community.ID, []string{"21", "22"}); err != nil {
		t.Fatalf("SetCommunityBuildings: %v", err)
	}

	// --- Leaves in every status --------------------------------------------
	pendingID, err := store.SubmitLeave(ctx, student1, app.SubmitLeaveInput{
		ContactPhone: "13800000001", Reason: "渲染测试：待审批请假",
		StartAt: now + 3600, EndAt: now + 7200,
	})
	if err != nil {
		t.Fatalf("SubmitLeave(pending): %v", err)
	}
	approvedID, err := store.SubmitLeave(ctx, student1, app.SubmitLeaveInput{
		ContactPhone: "13800000001", Reason: "渲染测试：已批准请假",
		StartAt: now + 10000, EndAt: now + 12000,
	})
	if err != nil {
		t.Fatalf("SubmitLeave(approved): %v", err)
	}
	if err := store.DecideLeave(ctx, teacher, approvedID, app.DecideInput{
		Decision: "approved", ApproverName: "渲染班主任", MajorOpinion: "同意",
	}); err != nil {
		t.Fatalf("DecideLeave(approved): %v", err)
	}
	rejectedID, err := store.SubmitLeave(ctx, student2, app.SubmitLeaveInput{
		ContactPhone: "13800000002", Reason: "渲染测试：已驳回请假",
		StartAt: now + 14000, EndAt: now + 16000,
	})
	if err != nil {
		t.Fatalf("SubmitLeave(rejected): %v", err)
	}
	if err := store.DecideLeave(ctx, teacher, rejectedID, app.DecideInput{
		Decision: "rejected", ApproverName: "渲染班主任", RejectReason: "渲染测试驳回",
	}); err != nil {
		t.Fatalf("DecideLeave(rejected): %v", err)
	}
	cancelledID, err := store.SubmitLeave(ctx, student2, app.SubmitLeaveInput{
		ContactPhone: "13800000002", Reason: "渲染测试：已取消请假",
		StartAt: now + 18000, EndAt: now + 20000,
	})
	if err != nil {
		t.Fatalf("SubmitLeave(cancelled): %v", err)
	}
	if err := store.CancelLeaveByStudent(ctx, student2, cancelledID); err != nil {
		t.Fatalf("CancelLeaveByStudent: %v", err)
	}

	// Active proxy link for the pending leave and a valid QR token for the
	// approved leave.
	proxyRaw, err := store.GenerateProxyLink(ctx, teacher, pendingID, true)
	if err != nil {
		t.Fatalf("GenerateProxyLink: %v", err)
	}
	gateRaw := store.QRToken(approvedID)

	// --- Archived class snapshot + archived leave --------------------------
	res, err := store.DB.Exec(`INSERT INTO archived_classes
		(source_class_id,class_name,department,last_teacher_user_id,last_teacher_name,
		 last_counselor_user_id,last_counselor_name,cleaned_at,retain_until,student_count,
		 leave_count,reason,created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		990001, "渲染已清理班", "计算机系", teacher.ID, teacher.Username,
		counselor.ID, counselor.Username, now-86400, now+40*86400, 2, 1, "unclaimed_7d", now-86400)
	if err != nil {
		t.Fatalf("insert archived class: %v", err)
	}
	archivedClassID, _ := res.LastInsertId()
	res, err = store.DB.Exec(`INSERT INTO leave_requests
		(student_user_id,student_name,student_no,archived_class_id,class_name,department,
		 teacher_user_id,teacher_name,counselor_user_id,counselor_name,
		 dorm_building,dorm_room,reason,start_at,end_at,status,decision,approver_name,
		 deadline_at,decided_at,created_at,archived_at,retain_until)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?, 'approved','approved',?,?,?,?,?,?)`,
		student1.ID, student1.Username, student1.StudentNo, archivedClassID, "渲染已清理班", "计算机系",
		teacher.ID, teacher.Username, counselor.ID, counselor.Username,
		"21", "601", "渲染测试：归档请假", now-7200, now-3600, "渲染班主任",
		now-3600, now-3600, now-86400, now-86400, now+40*86400)
	if err != nil {
		t.Fatalf("insert archived leave: %v", err)
	}
	archivedLeaveID, _ := res.LastInsertId()

	// --- Notifications, dorm edits, imports, codes, audit -------------------
	for _, uid := range []int64{admin.ID, teacher.ID, counselor.ID, community.ID, student1.ID} {
		if err := store.Notify(ctx, nil, uid, "render_test", "渲染测试通知", "用于渲染通知列表", "/", 0); err != nil {
			t.Fatalf("Notify(%d): %v", uid, err)
		}
	}
	if err := store.UpdateDorm(ctx, admin, student2.ID, "21-602", "22-702", "渲染测试宿舍调整", ip); err != nil {
		t.Fatalf("UpdateDorm: %v", err)
	}
	if _, err := store.DB.Exec(`INSERT INTO student_import_batches
		(class_id,imported_by,file_name,total_rows,created_count,error_count,status,ip,created_at)
		VALUES (?,?,?,?,?,?, 'success', ?, ?)`,
		class.ID, teacher.ID, "渲染测试名单.xlsx", 2, 2, 0, ip, now); err != nil {
		t.Fatalf("insert import batch: %v", err)
	}
	if _, _, err := store.CreateRegistrationCode(ctx, admin, "teacher", 5, now, now+7*86400, "渲染测试注册码", ip); err != nil {
		t.Fatalf("CreateRegistrationCode: %v", err)
	}
	if err := store.Audit(ctx, nil, admin, "render.test", "system", 0, "渲染矩阵测试审计记录", ip); err != nil {
		t.Fatalf("Audit: %v", err)
	}

	// --- Security records ---------------------------------------------------
	if _, err := store.DB.Exec(`INSERT INTO registration_attempts (ip,role,success,reason,created_at)
		VALUES (?, 'teacher', 0, '渲染测试：注册码无效', ?)`, "203.0.113.10", now); err != nil {
		t.Fatalf("insert registration attempt: %v", err)
	}
	if _, err := store.DB.Exec(`INSERT INTO login_attempts
		(key,scope,window_start,failed_count,locked_until,updated_at) VALUES (?,?,?,?,?,?)`,
		"203.0.113.10", "login_ip", now, 3, now+600, now); err != nil {
		t.Fatalf("insert locked ip: %v", err)
	}

	// --- Reset requests / tokens -------------------------------------------
	_, _, recoverBrowser, err := store.CreateStudentResetRequest(ctx, student1.StudentNo, student1.Username, ip)
	if err != nil {
		t.Fatalf("CreateStudentResetRequest(student1): %v", err)
	}
	// A class-less student's request is visible on the admin assisted-reset page.
	if _, _, _, err := store.CreateStudentResetRequest(ctx, studentNoClass.StudentNo, studentNoClass.Username, ip); err != nil {
		t.Fatalf("CreateStudentResetRequest(studentNoClass): %v", err)
	}
	resetToken, err := store.CreatePasswordResetToken(ctx, student1.ID, admin.ID, ip)
	if err != nil {
		t.Fatalf("CreatePasswordResetToken: %v", err)
	}

	// --- Sessions -----------------------------------------------------------
	adminCookie, _ := webSession(t, store, admin)
	teacherCookie, _ := webSession(t, store, teacher)
	counselorCookie, _ := webSession(t, store, counselor)
	communityCookie, _ := webSession(t, store, community)
	studentCookie, _ := webSession(t, store, student1)
	studentNoClassCookie, _ := webSession(t, store, studentNoClass)

	recoverCookie := &http.Cookie{Name: cookieRecover, Value: recoverBrowser, Path: "/"}

	// --- Matrix -------------------------------------------------------------
	cases := []renderCase{
		// Public pages.
		{name: "public home", path: "/"},
		{name: "student login", path: "/login"},
		{name: "staff login", path: "/staff/login"},
		{name: "admin login", path: "/admin/login"},
		{name: "register info", path: "/register"},
		{name: "staff register", path: "/staff/register"},
		{name: "forgot password", path: "/forgot-password"},
		{name: "admin forgot password", path: "/admin/forgot-password"},
		{name: "recover", path: "/recover"},
		{name: "recover status without cookie", path: "/recover/status", status: http.StatusBadRequest},
		{name: "recover status pending", path: "/recover/status", cookie: recoverCookie},
		{name: "reset password without token", path: "/reset-password", status: http.StatusBadRequest},
		{name: "reset password with token", path: "/reset-password?token=" + resetToken},
		{name: "gate valid", path: "/p/" + gateRaw},
		{name: "gate invalid", path: "/p/not-a-token", status: http.StatusNotFound},
		{name: "proxy valid", path: "/a/" + proxyRaw},
		{name: "proxy invalid", path: "/a/not-a-token", status: http.StatusNotFound},
		{name: "healthz", path: "/healthz", kind: "json"},
		{name: "fallback not found", path: "/this-page-does-not-exist", status: http.StatusNotFound},

		// Student.
		{name: "student dashboard", path: "/s", cookie: studentCookie},
		{name: "student leave new", path: "/s/leave/new", cookie: studentCookie},
		{name: "student leaves", path: "/s/leaves", cookie: studentCookie},
		{name: "student leave pending detail", path: "/s/leaves/" + itoa(pendingID), cookie: studentCookie},
		{name: "student leave approved detail", path: "/s/leaves/" + itoa(approvedID), cookie: studentCookie},
		{name: "student leave qr", path: "/s/leaves/" + itoa(approvedID) + "/qr.png", cookie: studentCookie, kind: "png"},
		{name: "student profile", path: "/s/profile", cookie: studentCookie},
		{name: "student notifications", path: "/s/notifications", cookie: studentCookie},
		{name: "student recover", path: "/s/recover", cookie: studentCookie},
		{name: "classless student dashboard", path: "/s", cookie: studentNoClassCookie},
		{name: "classless student leave new", path: "/s/leave/new", cookie: studentNoClassCookie},

		// Teacher.
		{name: "teacher dashboard", path: "/t", cookie: teacherCookie},
		{name: "teacher classes", path: "/t/classes", cookie: teacherCookie},
		{name: "teacher class new", path: "/t/classes/new", cookie: teacherCookie},
		{name: "teacher claim", path: "/t/classes/claim", cookie: teacherCookie},
		{name: "teacher class detail", path: "/t/classes/" + itoa(class.ID), cookie: teacherCookie},
		{name: "teacher class students", path: "/t/classes/" + itoa(class.ID) + "/students", cookie: teacherCookie},
		{name: "teacher proxy leave new", path: "/t/classes/" + itoa(class.ID) + "/students/" + itoa(student1.ID) + "/leave/new", cookie: teacherCookie},
		{name: "teacher class imports", path: "/t/classes/" + itoa(class.ID) + "/imports", cookie: teacherCookie},
		{name: "teacher class counselor", path: "/t/classes/" + itoa(class.ID) + "/counselor", cookie: teacherCookie},
		{name: "teacher import template", path: "/t/classes/" + itoa(class.ID) + "/students/import/template", cookie: teacherCookie, kind: "xlsx"},
		{name: "teacher recover", path: "/t/recover", cookie: teacherCookie},
		{name: "teacher leave pending", path: "/t/leaves/" + itoa(pendingID), cookie: teacherCookie},
		{name: "teacher leave approved", path: "/t/leaves/" + itoa(approvedID), cookie: teacherCookie},
		{name: "teacher notifications", path: "/t/notifications", cookie: teacherCookie},
		{name: "teacher export", path: "/t/export", cookie: teacherCookie},
		{name: "teacher profile", path: "/t/profile", cookie: teacherCookie},
		{name: "teacher archived leaves", path: "/archived/leaves", cookie: teacherCookie},
		{name: "teacher archived leave detail", path: "/archived/leaves/" + itoa(archivedLeaveID), cookie: teacherCookie},

		// Counselor.
		{name: "counselor dashboard", path: "/t", cookie: counselorCookie},
		{name: "counselor classes", path: "/t/classes", cookie: counselorCookie},
		{name: "counselor class new", path: "/t/classes/new", cookie: counselorCookie},
		{name: "counselor class detail", path: "/t/classes/" + itoa(class.ID), cookie: counselorCookie},
		{name: "counselor class students", path: "/t/classes/" + itoa(class.ID) + "/students", cookie: counselorCookie},
		{name: "counselor class imports", path: "/t/classes/" + itoa(class.ID) + "/imports", cookie: counselorCookie},
		{name: "counselor class counselor", path: "/t/classes/" + itoa(class.ID) + "/counselor", cookie: counselorCookie},
		{name: "counselor invitations", path: "/t/class-invitations", cookie: counselorCookie},
		{name: "counselor recover", path: "/t/recover", cookie: counselorCookie},
		{name: "counselor leave pending", path: "/t/leaves/" + itoa(pendingID), cookie: counselorCookie},
		{name: "counselor notifications", path: "/t/notifications", cookie: counselorCookie},
		{name: "counselor export", path: "/t/export", cookie: counselorCookie},
		{name: "counselor profile", path: "/t/profile", cookie: counselorCookie},
		{name: "counselor archived leaves", path: "/archived/leaves", cookie: counselorCookie},
		{name: "counselor archived leave detail", path: "/archived/leaves/" + itoa(archivedLeaveID), cookie: counselorCookie},

		// Community teacher.
		{name: "community dashboard", path: "/c", cookie: communityCookie},
		{name: "community buildings", path: "/c/buildings", cookie: communityCookie},
		{name: "community students", path: "/c/students", cookie: communityCookie},
		{name: "community student search", path: "/c/students?q=" + student2.StudentNo, cookie: communityCookie},
		{name: "community leaves", path: "/c/leaves", cookie: communityCookie},
		{name: "community leave detail", path: "/c/leaves/" + itoa(approvedID), cookie: communityCookie},
		{name: "community recover", path: "/c/recover", cookie: communityCookie},
		{name: "community notifications", path: "/c/notifications", cookie: communityCookie},
		{name: "community export", path: "/c/export", cookie: communityCookie},
		{name: "community profile", path: "/c/profile", cookie: communityCookie},
		{name: "community archived leaves", path: "/archived/leaves", cookie: communityCookie},
		{name: "community archived leave detail", path: "/archived/leaves/" + itoa(archivedLeaveID), cookie: communityCookie},

		// Administrator.
		{name: "admin dashboard", path: "/admin", cookie: adminCookie},
		{name: "admin leaves", path: "/admin/leaves", cookie: adminCookie},
		{name: "admin leave detail", path: "/admin/leaves/" + itoa(pendingID), cookie: adminCookie},
		{name: "admin export page", path: "/admin/export", cookie: adminCookie},
		{name: "admin export xlsx", path: "/admin/export.xlsx?range=all", cookie: adminCookie, kind: "xlsx"},
		{name: "admin report building", path: "/admin/reports?dimension=building", cookie: adminCookie},
		{name: "admin report class", path: "/admin/reports?dimension=class", cookie: adminCookie},
		{name: "admin report teacher", path: "/admin/reports?dimension=teacher", cookie: adminCookie},
		{name: "admin report counselor", path: "/admin/reports?dimension=counselor", cookie: adminCookie},
		{name: "admin classes", path: "/admin/classes", cookie: adminCookie},
		{name: "admin class detail", path: "/admin/classes/" + itoa(class.ID), cookie: adminCookie},
		{name: "admin unclaimed class detail", path: "/admin/classes/" + itoa(unclaimed.ID), cookie: adminCookie},
		{name: "admin users", path: "/admin/users", cookie: adminCookie},
		{name: "admin user student", path: "/admin/users/" + itoa(student1.ID), cookie: adminCookie},
		{name: "admin user teacher", path: "/admin/users/" + itoa(teacher.ID), cookie: adminCookie},
		{name: "admin user admin", path: "/admin/users/" + itoa(admin.ID), cookie: adminCookie},
		{name: "admin dorm edits", path: "/admin/students/" + itoa(student2.ID) + "/dorm-edits", cookie: adminCookie},
		{name: "admin imports", path: "/admin/imports", cookie: adminCookie},
		{name: "admin registration codes", path: "/admin/settings/registration-codes", cookie: adminCookie},
		{name: "admin recover", path: "/admin/recover", cookie: adminCookie},
		{name: "admin community", path: "/admin/community", cookie: adminCookie},
		{name: "admin security", path: "/admin/security", cookie: adminCookie},
		{name: "admin audit", path: "/admin/audit", cookie: adminCookie},
		{name: "admin notifications", path: "/admin/notifications", cookie: adminCookie},
		{name: "admin profile", path: "/admin/profile", cookie: adminCookie},
		{name: "admin archived leaves", path: "/archived/leaves", cookie: adminCookie},
		{name: "admin archived leave detail", path: "/archived/leaves/" + itoa(archivedLeaveID), cookie: adminCookie},
	}

	forbidden := []string{
		"msg=render",
		"unknown page",
		"panic",
		"incompatible types",
		"can't evaluate field",
	}

	sawHTTPLog := false
	t.Logf("渲染矩阵共 %d 个 GET 请求", len(cases))
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			rec := webRequest(t, srv, http.MethodGet, tc.path, tc.cookie, nil, "", "")

			// Attribute logs to this request, then clear the buffer so the next
			// subtest starts clean.
			logs := logBuf.String()
			logBuf.Reset()
			if strings.Contains(logs, "msg=http") {
				sawHTTPLog = true
			}
			for _, needle := range forbidden {
				if !strings.Contains(logs, needle) {
					continue
				}
				for _, line := range strings.Split(logs, "\n") {
					if strings.Contains(line, needle) {
						t.Errorf("页面 %s 渲染错误: %s", tc.path, line)
					}
				}
			}

			wantStatus := tc.status
			if wantStatus == 0 {
				wantStatus = http.StatusOK
			}
			if rec.Code != wantStatus {
				t.Errorf("页面 %s 状态 = %d，期望 %d", tc.path, rec.Code, wantStatus)
			}

			body := rec.Body.String()
			switch tc.kind {
			case "json":
				if !strings.Contains(body, `"ok":true`) {
					t.Errorf("页面 %s 响应 JSON 异常: %s", tc.path, body)
				}
			case "png":
				if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "image/png") {
					t.Errorf("页面 %s Content-Type = %q，期望 image/png", tc.path, ct)
				}
				if !strings.HasPrefix(body, "\x89PNG") {
					t.Errorf("页面 %s 不是有效 PNG", tc.path)
				}
			case "xlsx":
				if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "spreadsheetml") {
					t.Errorf("页面 %s Content-Type = %q，期望 XLSX", tc.path, ct)
				}
				if len(body) == 0 {
					t.Errorf("页面 %s 模板内容为空", tc.path)
				}
			default:
				// Every HTML response, including edge-case error pages, must
				// close the layout and carry a title.
				if !strings.Contains(body, "</html>") {
					t.Errorf("页面 %s 响应体缺少 </html>（layout 未完整执行）", tc.path)
				}
				if !strings.Contains(body, "<title>") {
					t.Errorf("页面 %s 响应体缺少 <title>", tc.path)
				}
			}
		})
	}
	if !sawHTTPLog {
		t.Fatal("日志捕获未生效：没有观察到任何 msg=http 日志，渲染错误检查可能是空转")
	}
}
