package web

import (
	"bytes"
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"leavepass/internal/app"
	"leavepass/internal/config"
)

type webExportFixture struct {
	store     *app.Store
	srv       *Server
	admin     app.User
	teacher   app.User
	counselor app.User
	community app.User
	class     app.Class
	stuA      app.User
	stuB      app.User
	leaveA1   int64
	leaveA2   int64
	leaveOld  int64
	leaveB    int64
}

func newWebExportFixture(t *testing.T) webExportFixture {
	t.Helper()
	store, clock, admin := newWebStore(t)
	srv, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	teacher := addWebStaff(t, store, "teacher", "XET1", "13971000001")
	counselor := addWebStaff(t, store, "counselor", "XEC1", "13971000002")
	community := addWebStaff(t, store, "community", "XECO1", "13971000003")
	if err := store.SetCommunityBuildings(context.Background(), community.ID, []string{"21"}); err != nil {
		t.Fatalf("SetCommunityBuildings: %v", err)
	}
	class := addWebClass(t, store, admin, teacher, counselor, "Web导出班")
	stuA := addWebStudent(t, store, "张三", "20246001", class.ID, "21-601")
	stuB := addWebStudent(t, store, "李四", "20246002", class.ID, "22-602")

	at := func(y int, m time.Month, d, hour int) int64 {
		return time.Date(y, m, d, hour, 0, 0, 0, config.Location).Unix()
	}
	submit := func(stu app.User, phone, reason string, start int64) int64 {
		id, err := store.SubmitLeave(context.Background(), stu, app.SubmitLeaveInput{
			ContactPhone: phone, Reason: reason, StartAt: start, EndAt: start + 30*60,
		})
		if err != nil {
			t.Fatalf("SubmitLeave: %v", err)
		}
		return id
	}
	clock.Set(time.Date(2024, 5, 30, 10, 0, 0, 0, config.Location))
	leaveOld := submit(stuA, "13900000101", "历史病假记录测试", at(2024, 6, 1, 8))
	clock.Set(time.Date(2026, 3, 15, 10, 0, 0, 0, config.Location))
	leaveA1 := submit(stuA, "13900000101", "因病需要请假休息", at(2026, 3, 15, 10))
	leaveA2 := submit(stuA, "13900000101", "家中有事需要请假", at(2026, 3, 15, 11))
	leaveB := submit(stuB, "13900000102", "参加学校公派活动", at(2026, 3, 15, 12))

	if err := store.DecideLeave(context.Background(), teacher, leaveA2, app.DecideInput{
		Decision: "approved", ApproverName: teacher.Username, IP: "127.0.0.1",
	}); err != nil {
		t.Fatalf("approve A2: %v", err)
	}
	if err := store.DecideLeave(context.Background(), teacher, leaveB, app.DecideInput{
		Decision: "approved", ApproverName: teacher.Username, IP: "127.0.0.1",
	}); err != nil {
		t.Fatalf("approve B: %v", err)
	}
	return webExportFixture{
		store: store, srv: srv, admin: admin, teacher: teacher, counselor: counselor,
		community: community, class: class, stuA: stuA, stuB: stuB,
		leaveA1: leaveA1, leaveA2: leaveA2, leaveOld: leaveOld, leaveB: leaveB,
	}
}

func exportRows(t *testing.T, body []byte) [][]string {
	t.Helper()
	f, err := excelize.OpenReader(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer f.Close()
	rows, err := f.GetRows(exportSheet)
	if err != nil {
		t.Fatalf("GetRows: %v", err)
	}
	return rows
}

func exportHeaderIndex(header []string, want string) int {
	for i, v := range header {
		if v == want {
			return i
		}
	}
	return -1
}

func TestExportPagesRenderForEveryRole(t *testing.T) {
	f := newWebExportFixture(t)
	cases := []struct {
		name string
		user app.User
		path string
		want string
	}{
		{"admin", f.admin, "/admin/export", "全部请假记录"},
		{"teacher", f.teacher, "/t/export", "班主任只能导出本班记录"},
		{"counselor", f.counselor, "/t/export", "名下全部班级"},
		{"community", f.community, "/c/export", "不包含联系电话"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cookie, _ := webSession(t, f.store, tc.user)
			rec := webRequest(t, f.srv, http.MethodGet, tc.path, cookie, nil, "", "")
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s = %d: %s", tc.path, rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.want) || !strings.Contains(rec.Body.String(), "导出 Excel") {
				t.Fatalf("页面缺少预期内容: %s", rec.Body.String())
			}
		})
	}
}

func TestTeacherExportDefaultSixMonthsAndStudentAllTime(t *testing.T) {
	f := newWebExportFixture(t)
	cookie, _ := webSession(t, f.store, f.teacher)

	rec := webRequest(t, f.srv, http.MethodGet, "/t/export.xlsx", cookie, nil, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("默认导出 = %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "spreadsheetml") {
		t.Fatalf("Content-Type = %q", ct)
	}
	rows := exportRows(t, rec.Body.Bytes())
	if len(rows) != 4 { // header + 3 class leaves; 2024 record excluded
		t.Fatalf("默认 6 个月行数 = %d，期望 4: %v", len(rows), rows)
	}
	if exportHeaderIndex(rows[0], "联系电话") < 0 {
		t.Fatal("班主任导出应包含联系电话列")
	}

	rec = webRequest(t, f.srv, http.MethodGet, "/t/export.xlsx?range=all&student=20246001", cookie, nil, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("按学生全部时间导出 = %d: %s", rec.Code, rec.Body.String())
	}
	rows = exportRows(t, rec.Body.Bytes())
	if len(rows) != 4 {
		t.Fatalf("该生全部记录行数 = %d，期望 4: %v", len(rows), rows)
	}
}

func TestCommunityExportOnlyApprovedAndNoPhone(t *testing.T) {
	f := newWebExportFixture(t)
	cookie, _ := webSession(t, f.store, f.community)
	rec := webRequest(t, f.srv, http.MethodGet, "/c/export.xlsx?range=all", cookie, nil, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("社区导出 = %d: %s", rec.Code, rec.Body.String())
	}
	rows := exportRows(t, rec.Body.Bytes())
	if len(rows) != 2 { // header + one approved leave in building 21
		t.Fatalf("社区导出行数 = %d，期望 2: %v", len(rows), rows)
	}
	if exportHeaderIndex(rows[0], "联系电话") >= 0 {
		t.Fatal("社区老师导出不应包含联系电话列")
	}
	if rows[1][exportHeaderIndex(rows[0], "假条ID")] != strconv.FormatInt(f.leaveA2, 10) {
		t.Fatalf("社区导出包含错误记录: %v", rows[1])
	}
}

func TestAdminExportScopesAndInvalidInput(t *testing.T) {
	f := newWebExportFixture(t)
	cookie, _ := webSession(t, f.store, f.admin)

	rec := webRequest(t, f.srv, http.MethodGet, "/admin/export.xlsx?scope=building&building=22&range=all", cookie, nil, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("楼栋导出 = %d: %s", rec.Code, rec.Body.String())
	}
	if rows := exportRows(t, rec.Body.Bytes()); len(rows) != 2 {
		t.Fatalf("楼栋 22 行数 = %d，期望 2: %v", len(rows), rows)
	}

	rec = webRequest(t, f.srv, http.MethodGet, "/admin/export.xlsx?scope=class", cookie, nil, "", "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("缺少班级状态 = %d，期望 422", rec.Code)
	}

	rec = webRequest(t, f.srv, http.MethodGet, "/admin/export.xlsx?range=custom", cookie, nil, "", "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("空自定义范围状态 = %d，期望 422", rec.Code)
	}
	rec = webRequest(t, f.srv, http.MethodGet, "/admin/export.xlsx?range=custom&from=2026-03-10&to=2026-03-01", cookie, nil, "", "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("反向自定义范围状态 = %d，期望 422", rec.Code)
	}

	// The legacy CSV URL must not 404 for old bookmarks.
	rec = webRequest(t, f.srv, http.MethodGet, "/admin/leaves/export", cookie, nil, "", "")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/export" {
		t.Fatalf("旧导出入口 = %d, Location=%q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestExportWorkbookFormulaSafe(t *testing.T) {
	rows := []app.Leave{{
		ID: 1, StudentName: "测试", StudentNo: "20249999", ClassName: "测试班",
		Department: "计算机系", DormBuilding: "21", DormRoom: "601",
		Reason: "=1+1", Status: "pending", StartAt: 1700000000, EndAt: 1700003600,
		CreatedAt: 1700000000,
	}}
	data, err := buildLeaveXLSX(rows, true, exportMeta{
		ActorName: "admin", ActorRole: "管理员", ScopeLabel: "全部请假记录",
		WindowLabel: "全部时间", Count: 1, GeneratedAt: 1700000000,
	})
	if err != nil {
		t.Fatalf("buildLeaveXLSX: %v", err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer f.Close()
	cell := "I2"
	value, err := f.GetCellValue(exportSheet, cell)
	if err != nil || value != "=1+1" {
		t.Fatalf("公式样文本被改写: %q, %v", value, err)
	}
	if formula, _ := f.GetCellFormula(exportSheet, cell); formula != "" {
		t.Fatalf("用户文本被写成公式: %q", formula)
	}
	if _, err := f.GetSheetIndex("导出说明"); err != nil {
		t.Fatalf("缺少导出说明工作表: %v", err)
	}
}
