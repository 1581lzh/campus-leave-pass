package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"leavepass/internal/app"
	"leavepass/internal/config"
)

func TestTeacherProxyLeaveFlow(t *testing.T) {
	store, clock, admin := newWebStore(t)
	srv, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	teacher := addWebStaff(t, store, "teacher", "PW1", "13993000011")
	counselor := addWebStaff(t, store, "counselor", "PW2", "13993000012")
	otherTeacher := addWebStaff(t, store, "teacher", "PW3", "13993000013")
	class := addWebClass(t, store, admin, teacher, counselor, "网页代提交班")
	addWebClass(t, store, admin, otherTeacher, app.User{}, "网页代提交其他班")
	student := addWebStudent(t, store, "网页代提交学生", "20247101", class.ID, "21-1801")

	teacherCookie, teacherCSRF := webSession(t, store, teacher)
	otherCookie, otherCSRF := webSession(t, store, otherTeacher)
	studentCookie, _ := webSession(t, store, student)
	path := "/t/classes/" + itoa(class.ID) + "/students/" + itoa(student.ID) + "/leave/new"

	rec := webRequest(t, srv, http.MethodGet, path, teacherCookie, nil, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("代提交页面状态 = %d；%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, student.Username) || !strings.Contains(body, "代提交") {
		t.Fatalf("代提交页面缺少学生姓名或“代提交”: %s", body)
	}

	start := clock.Now().Add(time.Hour)
	end := start.Add(2 * time.Hour)
	const (
		reason       = "网页班主任代提交请假"
		contactPhone = "13800000022"
	)
	form := url.Values{
		"contact_phone": {contactPhone},
		"reason":        {reason},
		"start_at":      {start.In(config.Location).Format("2006-01-02T15:04")},
		"end_at":        {end.In(config.Location).Format("2006-01-02T15:04")},
	}
	rec = webRequest(t, srv, http.MethodPost, path, teacherCookie, form, teacherCSRF, "")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("代提交 POST 状态 = %d，期望 303；%s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/t/leaves/") {
		t.Fatalf("代提交后 Location = %q", loc)
	}
	leaveID, err := strconv.ParseInt(strings.TrimPrefix(loc, "/t/leaves/"), 10, 64)
	if err != nil {
		t.Fatalf("解析假条 ID: %v", err)
	}
	leave, err := store.GetLeave(t.Context(), leaveID)
	if err != nil {
		t.Fatalf("GetLeave: %v", err)
	}
	if leave.StudentUserID != student.ID || leave.CreatedBy != teacher.ID || leave.Status != "pending" {
		t.Fatalf("代提交假条 = %+v", leave)
	}
	if leave.Reason != reason || leave.ContactPhone != contactPhone {
		t.Fatalf("代提交原因/电话 = %q/%q", leave.Reason, leave.ContactPhone)
	}
	if leave.StartAt != start.Unix() || leave.EndAt != end.Unix() {
		t.Fatalf("代提交时间 = %d-%d，期望 %d-%d", leave.StartAt, leave.EndAt, start.Unix(), end.Unix())
	}

	// 班主任可查看并直接审批自己代提交的假条。
	detailPath := "/t/leaves/" + itoa(leaveID)
	rec = webRequest(t, srv, http.MethodGet, detailPath, teacherCookie, nil, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("代提交详情状态 = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "老师代提交") {
		t.Fatalf("详情页缺少“老师代提交”: %s", rec.Body.String())
	}
	rec = webRequest(t, srv, http.MethodPost, detailPath+"/decide", teacherCookie, url.Values{
		"decision":      {"approved"},
		"approver_name": {"代提交班主任"},
		"major_opinion": {"同意"},
	}, teacherCSRF, "")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("代提交审批状态 = %d，期望 303；%s", rec.Code, rec.Body.String())
	}
	approved, err := store.GetLeave(t.Context(), leaveID)
	if err != nil {
		t.Fatalf("审批后 GetLeave: %v", err)
	}
	if approved.Status != "approved" || approved.Decision != "approved" ||
		approved.ApproverName != "代提交班主任" || approved.DecidedBy != teacher.ID {
		t.Fatalf("审批后假条 = %+v", approved)
	}

	// 其他班班主任不能打开或提交该页面。
	rec = webRequest(t, srv, http.MethodGet, path, otherCookie, nil, "", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("其他班 GET 状态 = %d，期望 403", rec.Code)
	}
	rec = webRequest(t, srv, http.MethodPost, path, otherCookie, url.Values{
		"reason":   {"其他班不应提交"},
		"start_at": {clock.Now().Add(3 * time.Hour).In(config.Location).Format("2006-01-02T15:04")},
		"end_at":   {clock.Now().Add(4 * time.Hour).In(config.Location).Format("2006-01-02T15:04")},
	}, otherCSRF, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("其他班 POST 状态 = %d，期望 403", rec.Code)
	}

	// 学生能在自己的列表和详情中看到这条老师代提交的已批准假条。
	rec = webRequest(t, srv, http.MethodGet, "/s/leaves", studentCookie, nil, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("学生请假列表状态 = %d", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, class.Name) ||
		!strings.Contains(body, "已批准") || !strings.Contains(body, "/s/leaves/"+itoa(leaveID)) {
		t.Fatalf("学生列表缺少代提交假条: %s", body)
	}
	rec = webRequest(t, srv, http.MethodGet, "/s/leaves/"+itoa(leaveID), studentCookie, nil, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("学生假条详情状态 = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "老师代提交") {
		t.Fatalf("学生详情缺少“老师代提交”: %s", rec.Body.String())
	}
}
