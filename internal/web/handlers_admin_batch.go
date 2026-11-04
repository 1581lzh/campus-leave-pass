package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"leavepass/internal/app"
)

// maxBatchSelection keeps one request bounded. Administrators can repeat the
// action in batches; this is a transport guard, not a permission rule.
const maxBatchSelection = 100

// parseBatchIDs accepts the checkbox values common to every admin batch form.
// Duplicate IDs collapse so a duplicated form value cannot double-audit an
// item; invalid values fail before any item is changed.
func parseBatchIDs(r *http.Request) ([]int64, error) {
	raw := r.Form["ids"]
	if len(raw) == 0 {
		return nil, errors.New("请先勾选至少一项再提交；没有 JavaScript 时可以逐项勾选。")
	}
	if len(raw) > maxBatchSelection {
		return nil, fmt.Errorf("单次最多处理 %d 项，请分批操作。", maxBatchSelection)
	}
	seen := make(map[int64]bool, len(raw))
	ids := make([]int64, 0, len(raw))
	for _, value := range raw {
		id, err := strconvParse(value)
		if err != nil || id <= 0 {
			return nil, errors.New("所选项目编号无效，请刷新页面后重试。")
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, errors.New("请先勾选至少一项再提交。")
	}
	return ids, nil
}

func adminUsersListURL(role, query string) string {
	v := url.Values{}
	if role != "" {
		v.Set("role", role)
	}
	if query != "" {
		v.Set("q", query)
	}
	if len(v) == 0 {
		return "/admin/users"
	}
	return "/admin/users?" + v.Encode()
}

func adminClassesListURL(query string) string {
	if query == "" {
		return "/admin/classes"
	}
	return "/admin/classes?" + url.Values{"q": {query}}.Encode()
}

func normalizeAdminUserRole(raw string) string {
	switch role := strings.TrimSpace(raw); role {
	case "student", "teacher", "counselor", "community", "admin":
		return role
	default:
		return ""
	}
}

// batchResultFlash turns partial success into a concise flash message. Only
// the first few failures are expanded to keep the cookie and page readable;
// every failed item is still reported by count.
func batchResultFlash(verb string, res app.BatchResult) (kind, message string) {
	var parts []string
	if n := res.SucceededCount(); n > 0 {
		parts = append(parts, fmt.Sprintf("已%s %d 项", verb, n))
	}
	if n := res.SkippedCount(); n > 0 {
		parts = append(parts, fmt.Sprintf("跳过 %d 项（状态已符合）", n))
	}
	if n := res.FailedCount(); n > 0 {
		parts = append(parts, fmt.Sprintf("失败 %d 项", n))
	}
	message = strings.Join(parts, "，")
	if message == "" {
		message = "没有需要处理的项。"
	}
	if res.FailedCount() > 0 {
		details := make([]string, 0, 4)
		for i, failure := range res.Failures {
			if i == 3 {
				details = append(details, "其余失败项不再展开")
				break
			}
			details = append(details, fmt.Sprintf("%s：%s", failure.Label, failure.Err))
		}
		message += "。" + strings.Join(details, "；")
	}
	switch {
	case res.FailedCount() > 0 && res.SucceededCount() == 0:
		return "error", message
	case res.FailedCount() > 0:
		return "warn", message
	default:
		return "ok", message
	}
}

func (s *Server) adminUsersBatch(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.require(w, r, "admin")
	if !ok {
		return
	}
	role := normalizeAdminUserRole(r.FormValue("role"))
	query := strings.TrimSpace(r.FormValue("q"))
	back := adminUsersListURL(role, query)
	ids, err := parseBatchIDs(r)
	if err != nil {
		s.error(w, r, http.StatusUnprocessableEntity, "批量停用未执行", err.Error())
		return
	}
	if action := strings.TrimSpace(r.FormValue("action")); action != "soft_delete" {
		s.error(w, r, http.StatusUnprocessableEntity, "批量停用未执行", "请选择“批量停用所选账号”后重试。")
		return
	}
	if r.FormValue("confirm") != "1" {
		s.error(w, r, http.StatusUnprocessableEntity, "批量停用未执行",
			"停用账号会立即让账号退出登录并进入 7 天只读保留期；请勾选“我已核对所选账号并确认停用”后重试。")
		return
	}
	res, err := s.store.BatchSoftDeleteUsers(r.Context(), actor, ids, r.FormValue("reason"), clientIP(r))
	if err != nil {
		s.postFail(w, r, err)
		return
	}
	kind, message := batchResultFlash("停用", res)
	s.redirectFlash(w, r, back, kind, message)
}

func (s *Server) adminClassesBatch(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.require(w, r, "admin")
	if !ok {
		return
	}
	query := strings.TrimSpace(r.FormValue("q"))
	back := adminClassesListURL(query)
	ids, err := parseBatchIDs(r)
	if err != nil {
		s.error(w, r, http.StatusUnprocessableEntity, "批量解散未执行", err.Error())
		return
	}
	if r.FormValue("confirm") != "1" {
		s.error(w, r, http.StatusUnprocessableEntity, "批量解散未执行",
			"解散班级不可撤销，请勾选“我已核对所选班级”后重试。")
		return
	}
	if strings.TrimSpace(r.FormValue("confirm_text")) != "确认解散" {
		s.error(w, r, http.StatusUnprocessableEntity, "批量解散未执行",
			"请在确认框中输入“确认解散”后重试。学生账号会保留并转为暂无班级，待审批假条会取消。")
		return
	}
	res, err := s.store.BatchDisbandClasses(r.Context(), actor, ids, clientIP(r))
	if err != nil {
		s.postFail(w, r, err)
		return
	}
	kind, message := batchResultFlash("解散", res)
	s.redirectFlash(w, r, back, kind, message)
}

func (s *Server) adminRegistrationCodesBatch(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.require(w, r, "admin")
	if !ok {
		return
	}
	const back = "/admin/settings/registration-codes"
	ids, err := parseBatchIDs(r)
	if err != nil {
		s.error(w, r, http.StatusUnprocessableEntity, "批量操作未执行", err.Error())
		return
	}
	action := strings.TrimSpace(r.FormValue("action"))
	var res app.BatchResult
	switch action {
	case "disable", "enable":
		res, err = s.store.BatchSetRegistrationCodeActive(r.Context(), actor, ids, action == "enable", clientIP(r))
	case "delete":
		if r.FormValue("confirm") != "1" {
			s.error(w, r, http.StatusUnprocessableEntity, "批量删除未执行",
				"删除注册码后无法恢复；已注册账号会保留。请勾选“我确认永久删除所选注册码”后重试。")
			return
		}
		res, err = s.store.BatchDeleteRegistrationCodes(r.Context(), actor, ids, clientIP(r))
	default:
		s.error(w, r, http.StatusUnprocessableEntity, "批量操作未执行", "请选择停用、启用或删除后重试。")
		return
	}
	if err != nil {
		s.postFail(w, r, err)
		return
	}
	verb := map[string]string{"disable": "停用", "enable": "启用", "delete": "删除"}[action]
	kind, message := batchResultFlash(verb, res)
	s.redirectFlash(w, r, back, kind, message)
}
