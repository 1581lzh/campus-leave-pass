package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"leavepass/internal/app"
	"leavepass/internal/config"
)

// auditFilterFromQuery parses the audit page filters. The time preset is
// resolved once so the store only has to deal with inclusive CST dates.
func (s *Server) auditFilterFromQuery(r *http.Request) (f app.AuditFilter, rangeValue, from, to string, err error) {
	q := r.URL.Query()
	f = app.AuditFilter{
		Actor:      strings.TrimSpace(q.Get("actor")),
		Target:     strings.TrimSpace(q.Get("target")),
		Action:     strings.TrimSpace(q.Get("action")),
		TargetType: strings.TrimSpace(q.Get("target_type")),
		Department: strings.TrimSpace(q.Get("department")),
		Building:   strings.TrimSpace(q.Get("building")),
		Keyword:    strings.TrimSpace(q.Get("q")),
		Page:       pageParam(r),
		PageSize:   50,
	}
	if v := strings.TrimSpace(q.Get("class_id")); v != "" {
		id, parseErr := strconv.ParseInt(v, 10, 64)
		if parseErr != nil || id <= 0 {
			return f, "", "", "", errors.New("班级筛选无效")
		}
		f.ClassID = id
	}
	rangeValue = strings.TrimSpace(q.Get("range"))
	if rangeValue == "" {
		rangeValue = "6m"
	}
	from = strings.TrimSpace(q.Get("from"))
	to = strings.TrimSpace(q.Get("to"))
	win, err := s.resolveExportWindow(rangeValue, from, to)
	if err != nil {
		return f, rangeValue, from, to, err
	}
	if win.Start > 0 {
		f.CreatedFrom = webCSTDate(win.Start)
	}
	if win.End > 0 {
		f.CreatedTo = webCSTDate(win.End - 1)
	}
	return f, rangeValue, from, to, nil
}

func webCSTDate(ts int64) string {
	if ts <= 0 {
		return ""
	}
	return time.Unix(ts, 0).In(config.Location).Format("2006-01-02")
}

var auditActionLabels = map[string]string{
	"admin.create":                 "创建管理员",
	"user.soft_delete":             "停用账号",
	"user.change_phone":            "修改手机号",
	"user.reset_link":              "生成重置链接",
	"student.fix_no":               "修正学号",
	"student.adjust_class":         "调整班级",
	"student.dorm":                 "修改宿舍",
	"class.assign_teacher":         "指派班主任",
	"class.assign_counselor":       "指派辅导员",
	"class.disband":                "解散班级",
	"class.cleanup":                "清理待认领班级",
	"regcode.create":               "生成注册码",
	"regcode.update":               "更新注册码",
	"regcode.delete":               "删除注册码",
	"leaves.export":                "导出请假记录",
	"security.revoke_own_sessions": "退出其他设备",
	"security.unlock":              "解除 IP 限制",
	"community.buildings":          "调整负责楼栋",
}

var auditTargetTypeLabels = map[string]string{
	"class":             "班级",
	"student":           "学生",
	"user":              "用户",
	"registration_code": "注册码",
	"leave":             "请假记录",
	"session":           "会话",
	"ip":                "IP",
	"system":            "系统",
}

func auditActionLabel(v string) string {
	if label, ok := auditActionLabels[v]; ok {
		return label
	}
	return v
}

func auditTargetTypeLabel(v string) string {
	if label, ok := auditTargetTypeLabels[v]; ok {
		return label
	}
	return v
}

// buildAuditFilterBar keeps the audit page declarative: the shared component
// owns layout and escaping, while this function only supplies choices.
func buildAuditFilterBar(f app.AuditFilter, facets app.AuditFacets, rangeValue, from, to string) FilterBar {
	classValue := ""
	if f.ClassID > 0 {
		classValue = strconv.FormatInt(f.ClassID, 10)
	}
	classOptions := []FilterOption{OptionIf("", "全部班级", f.ClassID == 0)}
	classFound := false
	for _, c := range facets.Classes {
		selected := c.ID == f.ClassID
		if selected {
			classFound = true
		}
		classOptions = append(classOptions, OptionIf(strconv.FormatInt(c.ID, 10), c.Name+"（"+c.Department+"）", selected))
	}
	if f.ClassID > 0 && !classFound {
		classOptions = append(classOptions, OptionIf(classValue, "班级 #"+classValue, true))
	}

	departmentOptions := []FilterOption{OptionIf("", "全部专业系", f.Department == "")}
	departmentFound := false
	for _, d := range facets.Departments {
		selected := d == f.Department
		if selected {
			departmentFound = true
		}
		departmentOptions = append(departmentOptions, OptionIf(d, d, selected))
	}
	if f.Department != "" && !departmentFound {
		departmentOptions = append(departmentOptions, OptionIf(f.Department, f.Department, true))
	}

	buildingOptions := []FilterOption{OptionIf("", "全部楼栋", f.Building == "")}
	buildingFound := false
	for _, b := range facets.Buildings {
		selected := b == f.Building
		if selected {
			buildingFound = true
		}
		buildingOptions = append(buildingOptions, OptionIf(b, b, selected))
	}
	if f.Building != "" && !buildingFound {
		buildingOptions = append(buildingOptions, OptionIf(f.Building, f.Building, true))
	}

	actionOptions := []FilterOption{OptionIf("", "全部动作", f.Action == "")}
	actionFound := false
	for _, a := range facets.Actions {
		selected := a == f.Action
		if selected {
			actionFound = true
		}
		actionOptions = append(actionOptions, OptionIf(a, auditActionLabel(a), selected))
	}
	if f.Action != "" && !actionFound {
		actionOptions = append(actionOptions, OptionIf(f.Action, auditActionLabel(f.Action), true))
	}

	typeOptions := []FilterOption{OptionIf("", "全部对象", f.TargetType == "")}
	typeSeen := map[string]bool{}
	for _, name := range []string{"class", "student", "user", "registration_code", "leave", "session", "ip", "system"} {
		typeSeen[name] = true
		typeOptions = append(typeOptions, OptionIf(name, auditTargetTypeLabel(name), f.TargetType == name))
	}
	for _, name := range facets.TargetTypes {
		if typeSeen[name] {
			continue
		}
		typeSeen[name] = true
		typeOptions = append(typeOptions, OptionIf(name, auditTargetTypeLabel(name), f.TargetType == name))
	}
	if f.TargetType != "" && !typeSeen[f.TargetType] {
		typeOptions = append(typeOptions, OptionIf(f.TargetType, auditTargetTypeLabel(f.TargetType), true))
	}

	rangeOptions := []FilterOption{
		OptionIf("6m", "最近 6 个月（默认）", rangeValue == "6m"),
		OptionIf("1w", "最近 1 周", rangeValue == "1w"),
		OptionIf("1m", "最近 1 个月", rangeValue == "1m"),
		OptionIf("3m", "最近 3 个月", rangeValue == "3m"),
		OptionIf("1y", "最近 1 年", rangeValue == "1y"),
		OptionIf("all", "全部时间", rangeValue == "all"),
		OptionIf("custom", "自定义范围", rangeValue == "custom"),
	}

	bar := NewFilterBar("/admin/audit",
		TextFilter("actor", "操作者", f.Actor, "用户名或姓名").
			WithHelp("按操作者用户名或姓名匹配").
			WithSpan(2),
		TextFilter("target", "对象用户", f.Target, "用户名或学号").
			WithHelp("按被操作的用户或学生匹配").
			WithSpan(2),
		SelectFilter("action", "动作", f.Action, actionOptions...),
		SelectFilter("target_type", "对象类型", f.TargetType, typeOptions...),
		SelectFilter("class_id", "班级", classValue, classOptions...),
		SelectFilter("department", "专业系", f.Department, departmentOptions...),
		SelectFilter("building", "楼栋", f.Building, buildingOptions...),
		SelectFilter("range", "时间范围", rangeValue, rangeOptions...).
			WithHelp("按操作时间计算"),
		DateFilter("from", "自定义开始", from).
			WithHelp("仅“自定义范围”时生效"),
		DateFilter("to", "自定义结束", to).
			WithHelp("仅“自定义范围”时生效"),
		TextFilter("q", "关键词", f.Keyword, "详情 / 动作 / IP").
			WithHelp("匹配详情、动作或 IP").
			WithSpan(2),
	)
	bar.ResetURL = "/admin/audit"
	bar.SummaryLabel = "筛选条件"
	return bar.WithCollapsible(auditActiveFilterCount(f, rangeValue))
}

func auditActiveFilterCount(f app.AuditFilter, rangeValue string) int {
	n := 0
	for _, v := range []string{f.Actor, f.Target, f.Action, f.TargetType, f.Department, f.Building, f.Keyword} {
		if strings.TrimSpace(v) != "" {
			n++
		}
	}
	if f.ClassID > 0 {
		n++
	}
	if rangeValue != "" && rangeValue != "6m" {
		n++
	}
	return n
}
