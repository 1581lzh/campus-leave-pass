package web

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xuri/excelize/v2"

	"leavepass/internal/app"
	"leavepass/internal/config"
)

const exportSheet = "请假记录"

// exportSlots limits how many Excel files are generated at once. A full
// export can hold 100k rows; allowing unbounded concurrent generation would
// let a handful of clicks exhaust memory.
var exportSlots = make(chan struct{}, 2)

type exportWindow struct {
	Start int64
	End   int64
	Label string
}

type exportPageData struct {
	baseData
	Action        string
	BackURL       string
	Kind          string
	IsAdmin       bool
	IsCommunity   bool
	Classes       []app.Class
	Buildings     []string
	Departments   []string
	ClassID       int64
	Building      string
	Department    string
	Student       string
	Range         string
	From          string
	To            string
	Scope         string
	FixedClass    app.Class
	HasFixedClass bool
	MaxRows       int
}

type exportMeta struct {
	ActorName   string
	ActorRole   string
	ScopeLabel  string
	WindowLabel string
	Student     string
	Count       int
	GeneratedAt int64
}

func exportRoles(kind string) []string {
	switch kind {
	case "admin":
		return []string{"admin"}
	case "community":
		return []string{"community"}
	default:
		return []string{"teacher", "counselor"}
	}
}

func exportKindBase(kind string) (action, back, page, title string) {
	switch kind {
	case "admin":
		return "/admin/export.xlsx", "/admin/leaves", "admin_export", "导出请假记录"
	case "community":
		return "/c/export.xlsx", "/c/leaves", "community_export", "导出请假记录"
	default:
		return "/t/export.xlsx", "/t/classes", "staff_export", "导出请假记录"
	}
}

func (s *Server) adminExportPage(w http.ResponseWriter, r *http.Request) {
	s.exportPage(w, r, "admin")
}

func (s *Server) communityExportPage(w http.ResponseWriter, r *http.Request) {
	s.exportPage(w, r, "community")
}

func (s *Server) staffExportPage(w http.ResponseWriter, r *http.Request) {
	s.exportPage(w, r, "staff")
}

func (s *Server) exportPage(w http.ResponseWriter, r *http.Request, kind string) {
	u, ok := s.require(w, r, exportRoles(kind)...)
	if !ok {
		return
	}
	action, back, page, title := exportKindBase(kind)
	d := exportPageData{
		baseData: s.base(w, r, page, title),
		Action:   action, BackURL: back, Kind: kind, Range: "6m", Scope: "all",
		MaxRows: app.MaxExportRows,
	}
	switch kind {
	case "admin":
		d.IsAdmin = true
		d.Classes, _ = s.store.AllClasses(r.Context())
		d.Buildings, d.Departments, _ = s.store.LeaveExportFacets(r.Context())
	case "community":
		d.IsCommunity = true
		d.Buildings, _ = s.store.CommunityBuildings(r.Context(), u.ID)
	default:
		d.Classes, _ = s.store.ClassesFor(r.Context(), u)
		if u.Role == "teacher" && len(d.Classes) == 1 {
			d.FixedClass = d.Classes[0]
			d.HasFixedClass = true
		}
	}

	q := r.URL.Query()
	if v := strings.TrimSpace(q.Get("range")); v != "" {
		d.Range = v
	}
	d.From = strings.TrimSpace(q.Get("from"))
	d.To = strings.TrimSpace(q.Get("to"))
	d.Student = strings.TrimSpace(q.Get("student"))
	d.Building = strings.TrimSpace(q.Get("building"))
	d.Department = strings.TrimSpace(q.Get("department"))
	switch q.Get("scope") {
	case "all", "class", "building", "department":
		d.Scope = q.Get("scope")
	}
	if v := strings.TrimSpace(q.Get("class_id")); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil && id > 0 {
			d.ClassID = id
		}
	}
	s.renderPage(w, r, "export_leaves", d)
}

func (s *Server) adminExportFile(w http.ResponseWriter, r *http.Request) {
	s.exportFile(w, r, "admin")
}

func (s *Server) communityExportFile(w http.ResponseWriter, r *http.Request) {
	s.exportFile(w, r, "community")
}

func (s *Server) staffExportFile(w http.ResponseWriter, r *http.Request) {
	s.exportFile(w, r, "staff")
}

func (s *Server) exportFile(w http.ResponseWriter, r *http.Request, kind string) {
	u, ok := s.require(w, r, exportRoles(kind)...)
	if !ok {
		return
	}
	select {
	case exportSlots <- struct{}{}:
		defer func() { <-exportSlots }()
	default:
		s.error(w, r, http.StatusTooManyRequests, "导出任务繁忙", "当前已有导出任务在生成，请稍等几秒后重试。")
		return
	}

	req, win, scopeLabel, err := s.buildExportRequest(r, kind)
	if err != nil {
		s.error(w, r, http.StatusUnprocessableEntity, "导出条件有误", err.Error())
		return
	}
	rows, err := s.store.ExportLeaves(r.Context(), u, req)
	if err != nil {
		s.error(w, r, http.StatusUnprocessableEntity, "导出失败", err.Error()+"；请检查筛选条件后重试。")
		return
	}
	if len(rows) == 0 {
		s.error(w, r, http.StatusNotFound, "没有可导出的记录",
			"当前筛选条件下没有请假记录。可以放宽时间范围（例如选择“全部时间”）、清空学生筛选，或更换班级/楼栋/专业系后重试。")
		return
	}
	if len(rows) > app.MaxExportRows {
		s.error(w, r, http.StatusUnprocessableEntity, "导出记录过多",
			fmt.Sprintf("超过 %d 条，请缩短时间范围或增加班级、楼栋、专业系、学生筛选后重试。", app.MaxExportRows))
		return
	}

	data, err := buildLeaveXLSX(rows, u.Role != "community", exportMeta{
		ActorName: u.Username, ActorRole: roleName(u.Role),
		ScopeLabel: scopeLabel, WindowLabel: win.Label, Student: req.Student,
		Count: len(rows), GeneratedAt: s.store.NowUnix(),
	})
	if err != nil {
		s.postFail(w, r, err)
		return
	}

	detail := fmt.Sprintf("导出请假 Excel：%s；时间范围 %s；学生 %s；共 %d 条",
		scopeLabel, win.Label, orAll(req.Student), len(rows))
	_ = s.store.Audit(r.Context(), nil, u, "leaves.export", "leave", 0, detail, clientIP(r))

	filename := "leave-records-" + exportTime(s.store.NowUnix())[:10] + ".xlsx"
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

func (s *Server) buildExportRequest(r *http.Request, kind string) (app.ExportQuery, exportWindow, string, error) {
	q := r.URL.Query()
	win, err := s.resolveExportWindow(q.Get("range"), q.Get("from"), q.Get("to"))
	if err != nil {
		return app.ExportQuery{}, win, "", err
	}
	student := strings.TrimSpace(q.Get("student"))
	if utf8.RuneCountInString(student) > 32 {
		return app.ExportQuery{}, win, "", errors.New("学生筛选条件最多 32 个字符")
	}
	req := app.ExportQuery{
		Student: student, StartFrom: win.Start, StartTo: win.End,
		Limit: app.MaxExportRows + 1,
	}
	scopeLabel := "全部请假记录"

	switch kind {
	case "admin":
		req.Scope = q.Get("scope")
		if req.Scope == "" {
			req.Scope = "all"
		}
		switch req.Scope {
		case "all":
		case "class":
			id, err := strconv.ParseInt(strings.TrimSpace(q.Get("class_id")), 10, 64)
			if err != nil || id <= 0 {
				return req, win, "", errors.New("请选择要导出的班级")
			}
			req.ClassID = id
			scopeLabel = "指定班级（ID " + strconv.FormatInt(id, 10) + "）"
		case "building":
			req.Building = strings.TrimSpace(q.Get("building"))
			scopeLabel = "指定楼栋：" + orAll(req.Building)
		case "department":
			req.Department = strings.TrimSpace(q.Get("department"))
			scopeLabel = "专业系：" + orAll(req.Department)
		default:
			return req, win, "", errors.New("无效的导出范围")
		}
	case "community":
		req.Building = strings.TrimSpace(q.Get("building"))
		scopeLabel = "负责楼栋的已批准请假"
		if req.Building != "" {
			scopeLabel += "（" + req.Building + "）"
		}
	default:
		if v := strings.TrimSpace(q.Get("class_id")); v != "" {
			id, err := strconv.ParseInt(v, 10, 64)
			if err != nil || id <= 0 {
				return req, win, "", errors.New("班级筛选无效")
			}
			req.ClassID = id
			scopeLabel = "指定班级（ID " + strconv.FormatInt(id, 10) + "）"
		} else {
			scopeLabel = "本人负责的班级（含历史快照记录）"
		}
	}
	return req, win, scopeLabel, nil
}

func (s *Server) resolveExportWindow(preset, from, to string) (exportWindow, error) {
	now := s.store.Now()
	end := app.NextMidnightCST(now).Unix()
	start := func(years, months, days int) int64 {
		return app.StartOfDayCST(now.AddDate(years, months, days)).Unix()
	}
	switch strings.TrimSpace(preset) {
	case "", "6m":
		return exportWindow{Start: start(0, -6, 0), End: end, Label: "最近 6 个月"}, nil
	case "1w":
		return exportWindow{Start: start(0, 0, -7), End: end, Label: "最近 1 周"}, nil
	case "1m":
		return exportWindow{Start: start(0, -1, 0), End: end, Label: "最近 1 个月"}, nil
	case "3m":
		return exportWindow{Start: start(0, -3, 0), End: end, Label: "最近 3 个月"}, nil
	case "1y":
		return exportWindow{Start: start(-1, 0, 0), End: end, Label: "最近 1 年"}, nil
	case "all":
		return exportWindow{Label: "全部时间"}, nil
	case "custom":
		startAt, endAt, err := app.DateBounds(strings.TrimSpace(from), strings.TrimSpace(to))
		if err != nil {
			return exportWindow{}, err
		}
		if startAt == 0 && endAt == 0 {
			return exportWindow{}, errors.New("选择“自定义范围”后，请至少填写开始日期或结束日期")
		}
		if startAt > 0 && endAt > 0 && startAt >= endAt {
			return exportWindow{}, errors.New("自定义开始日期不能晚于结束日期")
		}
		label := "自定义范围"
		if from != "" || to != "" {
			label += "（" + orAll(from) + " 至 " + orAll(to) + "）"
		}
		return exportWindow{Start: startAt, End: endAt, Label: label}, nil
	default:
		return exportWindow{}, errors.New("无效的时间范围")
	}
}

func orAll(v string) string {
	if strings.TrimSpace(v) == "" {
		return "全部"
	}
	return v
}

func exportTime(ts int64) string {
	if ts <= 0 {
		return ""
	}
	return time.Unix(ts, 0).In(config.Location).Format("2006-01-02 15:04")
}

func exportOpinion(l app.Leave) string {
	var parts []string
	if v := strings.TrimSpace(l.MajorOpinion); v != "" {
		parts = append(parts, "班主任："+v)
	}
	if v := strings.TrimSpace(l.DeptOpinion); v != "" {
		parts = append(parts, "辅导员："+v)
	}
	return strings.Join(parts, "；")
}

func buildLeaveXLSX(rows []app.Leave, includePhone bool, meta exportMeta) ([]byte, error) {
	f := excelize.NewFile()
	defer f.Close()
	if err := f.SetSheetName("Sheet1", exportSheet); err != nil {
		return nil, err
	}

	headers := []string{"假条ID", "学生姓名", "学号"}
	if includePhone {
		headers = append(headers, "联系电话")
	}
	headers = append(headers,
		"班级", "专业系", "楼栋", "房间", "请假原因",
		"开始时间", "结束时间", "状态", "提交时间",
		"审批结果", "审批人", "审批意见", "驳回原因", "取消原因",
		"班主任", "辅导员")

	widths := []float64{10, 12, 15, 15, 20, 16, 10, 8, 40,
		18, 18, 10, 18, 12, 12, 30, 24, 24, 12, 12}
	if !includePhone {
		widths = append(widths[:3], widths[4:]...)
	}
	col := func(i int) string {
		name, _ := excelize.ColumnNumberToName(i + 1)
		return name
	}
	for i, w := range widths {
		_ = f.SetColWidth(exportSheet, col(i), col(i), w)
	}

	headerStyle, err := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "FFFFFF"},
		Fill:      excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"1D4ED8"}},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center", WrapText: true},
		Border:    []excelize.Border{{Type: "bottom", Color: "1E3A8A", Style: 1}},
	})
	if err != nil {
		return nil, err
	}
	bodyStyle, err := f.NewStyle(&excelize.Style{
		Alignment: &excelize.Alignment{Vertical: "top"},
	})
	if err != nil {
		return nil, err
	}

	sw, err := f.NewStreamWriter(exportSheet)
	if err != nil {
		return nil, err
	}
	hdr := make([]any, len(headers))
	for i, h := range headers {
		hdr[i] = h
	}
	if err := sw.SetRow("A1", hdr, excelize.RowOpts{StyleID: headerStyle, Height: 26}); err != nil {
		return nil, err
	}
	for i, l := range rows {
		vals := make([]any, 0, len(headers))
		vals = append(vals, strconv.FormatInt(l.ID, 10), l.StudentName, l.StudentNo)
		if includePhone {
			vals = append(vals, l.ContactPhone)
		}
		vals = append(vals, l.ClassName, l.Department, l.DormBuilding, l.DormRoom, l.Reason,
			exportTime(l.StartAt), exportTime(l.EndAt), statusName(l.Status), exportTime(l.CreatedAt),
			decisionText(l), l.ApproverName, exportOpinion(l), l.RejectReason, l.CancelReason,
			l.TeacherName, l.CounselorName)
		cell, _ := excelize.CoordinatesToCellName(1, i+2)
		if err := sw.SetRow(cell, vals, excelize.RowOpts{StyleID: bodyStyle}); err != nil {
			return nil, err
		}
	}
	if err := sw.Flush(); err != nil {
		return nil, err
	}
	_ = f.SetPanes(exportSheet, &excelize.Panes{
		Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft",
	})
	_ = f.AutoFilter(exportSheet, "A1:"+col(len(headers)-1)+"1", nil)

	if _, err := f.NewSheet("导出说明"); err != nil {
		return nil, err
	}
	info := [][2]string{
		{"导出时间", exportTime(meta.GeneratedAt)},
		{"导出人", meta.ActorName + "（" + meta.ActorRole + "）"},
		{"导出范围", meta.ScopeLabel},
		{"时间范围", meta.WindowLabel},
		{"学生筛选", orAll(meta.Student)},
		{"记录数", strconv.Itoa(meta.Count)},
		{"说明", "社区老师导出不包含联系电话；本文件由校园请假通行系统生成。"},
	}
	for i, kv := range info {
		cell, _ := excelize.CoordinatesToCellName(1, i+1)
		if err := f.SetCellValue("导出说明", cell, kv[0]); err != nil {
			return nil, err
		}
		cell, _ = excelize.CoordinatesToCellName(2, i+1)
		if err := f.SetCellValue("导出说明", cell, kv[1]); err != nil {
			return nil, err
		}
	}
	_ = f.SetColWidth("导出说明", "A", "A", 14)
	_ = f.SetColWidth("导出说明", "B", "B", 60)
	f.SetActiveSheet(0)

	buf, err := f.WriteToBuffer()
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
