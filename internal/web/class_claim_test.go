package web

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestCounselorCanClaimClassAndReplaceCounselor(t *testing.T) {
	store, _, admin := newWebStore(t)
	srv, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	teacher := addWebStaff(t, store, "teacher", "WCT1", "13992000021")
	freeTeacher := addWebStaff(t, store, "teacher", "WCT2", "13992000024")
	current := addWebStaff(t, store, "counselor", "WCC1", "13992000022")
	claimant := addWebStaff(t, store, "counselor", "WCC2", "13992000023")
	class := addWebClass(t, store, admin, teacher, current, "辅导员网页认领班")
	free, err := store.CreateClass(context.Background(), freeTeacher, "辅导员网页空位班", "计算机系", false)
	if err != nil {
		t.Fatalf("CreateClass: %v", err)
	}
	cookie, csrf := webSession(t, store, claimant)

	rec := webRequest(t, srv, http.MethodGet, "/t/classes/claim", cookie, nil, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("辅导员认领页状态 = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, free.Name) {
		t.Fatalf("认领页缺少无辅导员班级 %q: %s", free.Name, body)
	}
	if strings.Contains(body, class.Name) {
		t.Fatalf("认领页默认列表不应包含已有辅导员的班级 %q: %s", class.Name, body)
	}

	rec = webRequest(t, srv, http.MethodGet, "/t/classes/claim?q="+url.QueryEscape(class.Name), cookie, nil, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("按班级名搜索状态 = %d", rec.Code)
	}
	body = rec.Body.String()
	for _, want := range []string{class.Name, current.Username, "我确认顶替现任辅导员", "认领为该班辅导员"} {
		if !strings.Contains(body, want) {
			t.Fatalf("搜索结果缺少 %q: %s", want, body)
		}
	}

	form := url.Values{
		"class_id":   {strconv.FormatInt(class.ID, 10)},
		"class_name": {class.Name},
	}
	rec = webRequest(t, srv, http.MethodPost, "/t/classes/claim", cookie, form, csrf, "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("未确认顶替状态 = %d，期望 409: %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "该班已有辅导员") || !strings.Contains(body, "确认后将顶替") {
		t.Fatalf("未确认顶替页面缺少原因: %s", body)
	}
	if got, err := store.GetClass(context.Background(), class.ID); err != nil || got.CounselorUserID != current.ID {
		t.Fatalf("未确认顶替后辅导员 = %d, err=%v，期望 %d", got.CounselorUserID, err, current.ID)
	}

	form.Set("confirm_takeover", "1")
	rec = webRequest(t, srv, http.MethodPost, "/t/classes/claim", cookie, form, csrf, "")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("确认顶替状态 = %d，期望 303: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/t/classes/"+strconv.FormatInt(class.ID, 10) {
		t.Fatalf("确认顶替 Location = %q", got)
	}
	got, err := store.GetClass(context.Background(), class.ID)
	if err != nil || got.CounselorUserID != claimant.ID {
		t.Fatalf("确认顶替后辅导员 = %d, err=%v，期望 %d", got.CounselorUserID, err, claimant.ID)
	}

	notes, err := store.ListNotifications(context.Background(), current.ID, 50)
	if err != nil {
		t.Fatalf("ListNotifications: %v", err)
	}
	var takeover string
	for _, n := range notes {
		if n.Kind == "counselor_takeover" {
			takeover = n.Body
			break
		}
	}
	if takeover == "" {
		t.Fatalf("被顶替辅导员未收到通知: %+v", notes)
	}
	for _, want := range []string{"2026-03-15 10:00", claimant.Username, class.Name, "顶替了你的辅导员岗位"} {
		if !strings.Contains(takeover, want) {
			t.Fatalf("顶替通知缺少 %q: %s", want, takeover)
		}
	}
}
