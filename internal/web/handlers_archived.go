package web

import (
	"net/http"

	"leavepass/internal/app"
)

type archivedLeavesData struct {
	baseData
	Leaves    []app.Leave
	Total     int
	Filter    app.LeaveFilter
	Buildings []string
	Classes   []app.Class
	PageNo    int
	Pages     int
	Query     string
}

type archivedLeaveData struct {
	baseData
	Leave       app.Leave
	Edits       []leaveEdit
	Archived    app.ArchivedClass
	HasArchived bool
	ShowPhone   bool
}

func (s *Server) archivedLeaves(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "admin", "teacher", "counselor", "community")
	if !ok {
		return
	}
	f := leaveFilterFromQuery(r)
	if u.Role == "community" {
		f.NoPhoneKeyword = true
	}
	list, total, err := s.store.ArchivedLeaves(r.Context(), u, f)
	if err != nil {
		s.error(w, r, http.StatusUnprocessableEntity, "筛选条件有误", err.Error()+"；请检查日期后重试。")
		return
	}
	classes, _ := s.store.AllClasses(r.Context())
	b := s.base(w, r, "archived_leaves", "保留假条")
	s.renderPage(w, r, "archived_leaves", archivedLeavesData{
		baseData: b, Leaves: list, Total: total, Filter: f,
		Buildings: s.store.CampusBuildings(), Classes: classes,
		PageNo: f.Page, Pages: pagesOf(total, 50), Query: r.URL.RawQuery,
	})
}

func (s *Server) archivedLeaveDetail(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "admin", "teacher", "counselor", "community")
	if !ok {
		return
	}
	id, err := parseID(r, "id")
	if err != nil {
		s.error(w, r, http.StatusNotFound, "假条不存在", "链接中的假条编号无效。")
		return
	}
	l, err := s.store.GetLeave(r.Context(), id)
	if err != nil || !s.store.CanViewArchived(r.Context(), u, l) {
		s.error(w, r, http.StatusForbidden, "无权查看",
			"无权查看：保留假条只对管理员、原班主任、辅导员和负责该楼栋的社区老师开放。")
		return
	}
	edits, _ := s.store.LeaveTimeEdits(r.Context(), id)
	var list []leaveEdit
	for _, e := range edits {
		list = append(list, convertLeaveEdit(e))
	}
	d := archivedLeaveData{baseData: s.base(w, r, "archived_leave", "保留假条详情"),
		Leave: l, Edits: list, ShowPhone: u.Role != "community"}
	if l.ArchivedClassID != 0 {
		if ac, err := s.store.GetArchivedClass(r.Context(), l.ArchivedClassID); err == nil {
			d.Archived, d.HasArchived = ac, true
		}
	}
	s.renderPage(w, r, "archived_leave", d)
}

func (s *Server) archivedLeaveDelete(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "admin")
	if !ok {
		return
	}
	id, err := parseID(r, "id")
	if err != nil {
		s.error(w, r, http.StatusNotFound, "假条不存在", "链接中的假条编号无效。")
		return
	}
	if r.FormValue("confirm") != "1" {
		s.error(w, r, http.StatusUnprocessableEntity, "删除未执行",
			"物理删除不可恢复；请勾选“我已确认删除”后重试。")
		return
	}
	l, err := s.store.GetLeave(r.Context(), id)
	if err != nil || l.ArchivedAt == 0 {
		s.error(w, r, http.StatusNotFound, "假条不存在", "该保留假条不存在或已被删除。")
		return
	}
	if err := s.store.DeleteLeaveByAdmin(r.Context(), u, id); err != nil {
		s.postFail(w, r, err)
		return
	}
	s.redirectFlash(w, r, "/archived/leaves", "ok", "保留假条已物理删除，且不可恢复。")
}
