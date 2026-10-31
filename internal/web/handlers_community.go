package web

import (
	"net/http"
	"strings"
	"time"

	"leavepass/internal/app"
)

type communityDashData struct {
	baseData
	Buildings []string
	Upcoming  []app.Leave
}

type communityBuildingsData struct {
	baseData
	Selected []string
	Options  []string
	Custom   string
	Error    string
}

type communityStudentsData struct {
	baseData
	Query   string
	Student app.StudentRow
	Found   bool
	Error   string
	Edits   []app.DormEditRow
}

type communityLeavesData struct {
	baseData
	Leaves    []app.Leave
	Total     int
	Filter    app.LeaveFilter
	Buildings []string
	Query     string
	PageNo    int
	Pages     int
}

type communityLeaveData struct {
	baseData
	Leave app.Leave
	Edits []leaveEdit
}

func (s *Server) communityDashboard(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "community")
	if !ok {
		return
	}
	buildings, _ := s.store.CommunityBuildings(r.Context(), u.ID)
	upcoming, _ := s.store.CommunityUpcoming(r.Context(), u.ID)
	b := s.base(w, r, "community_dashboard", "社区老师首页")
	s.renderPage(w, r, "community_dashboard", communityDashData{baseData: b, Buildings: buildings, Upcoming: upcoming})
}

func (s *Server) communityBuildings(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "community")
	if !ok {
		return
	}
	selected, _ := s.store.CommunityBuildings(r.Context(), u.ID)
	b := s.base(w, r, "community_buildings", "负责楼栋")
	s.renderPage(w, r, "community_buildings", communityBuildingsData{
		baseData: b, Selected: selected, Options: buildingOptions(s.store.CampusBuildings(), selected),
	})
}

func buildingOptions(base, selected []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, b := range base {
		if !seen[b] {
			seen[b] = true
			out = append(out, b)
		}
	}
	for _, b := range selected {
		if !seen[b] {
			seen[b] = true
			out = append(out, b)
		}
	}
	return out
}

func (s *Server) communityBuildingsPost(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "community")
	if !ok {
		return
	}
	buildings := r.Form["buildings"]
	custom := strings.TrimSpace(r.FormValue("custom"))
	if custom != "" {
		buildings = append(buildings, strings.FieldsFunc(custom, func(r rune) bool {
			return r == ',' || r == '，' || r == ' ' || r == '\n' || r == '\t'
		})...)
	}
	if err := s.store.SetCommunityBuildings(r.Context(), u.ID, buildings); err != nil {
		selected, _ := s.store.CommunityBuildings(r.Context(), u.ID)
		b := s.base(w, r, "community_buildings", "负责楼栋")
		s.render(w, r, http.StatusUnprocessableEntity, "community_buildings", communityBuildingsData{
			baseData: b, Selected: selected, Options: buildingOptions(s.store.CampusBuildings(), selected),
			Custom: custom, Error: err.Error(),
		})
		return
	}
	s.redirectFlash(w, r, "/c", "ok", "负责楼栋已更新；请假可见范围立即按新楼栋生效。")
}

func (s *Server) communityStudents(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "community")
	if !ok {
		return
	}
	// PLAN 12 requires rate limiting for community dorm searches.
	key := "u:" + itoa(u.ID)
	if until, _ := s.store.LoginLocked(r.Context(), "community_search", key); until > s.store.NowUnix() {
		s.error(w, r, http.StatusTooManyRequests, "查询过于频繁",
			"宿舍查询每分钟最多 30 次；请稍后再试，或直接联系班主任/辅导员协助。")
		return
	}
	_ = s.store.RecordLoginFailure(r.Context(), "community_search", key, time.Minute, time.Minute, 30)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	d := communityStudentsData{baseData: s.base(w, r, "community_students", "学生宿舍查询"), Query: q}
	if q != "" {
		st, err := s.store.SearchStudentByNo(r.Context(), q)
		if err != nil {
			d.Error = "未找到该在读学生；请按学号精确搜索，确认学生仍在读且有班级。"
		} else {
			d.Student, d.Found = st, true
		}
	}
	s.renderPage(w, r, "community_students", d)
}

func (s *Server) communityStudentDorm(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "community")
	if !ok {
		return
	}
	if u.Status != "active" {
		s.error(w, r, http.StatusForbidden, "无法修改宿舍", "账号已停用，不能修改学生宿舍。")
		return
	}
	id, err := strconvParse(r.PathValue("student_id"))
	if err != nil {
		s.error(w, r, http.StatusNotFound, "学生不存在", "链接中的学生编号无效。")
		return
	}
	st, err := s.store.GetStudent(r.Context(), id)
	if err != nil {
		s.error(w, r, http.StatusNotFound, "学生不存在", "该学生不存在或已不在读。")
		return
	}
	if err := s.store.UpdateDorm(r.Context(), u, id, r.FormValue("old_dorm"), r.FormValue("dorm"), r.FormValue("reason"), clientIP(r)); err != nil {
		s.postFail(w, r, err)
		return
	}
	s.redirectFlash(w, r, "/c/students?q="+st.StudentNo, "ok", "宿舍已修改；系统已通知学生和班级审批人，社区可见范围已按新楼栋刷新。")
}

func (s *Server) communityLeaves(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "community")
	if !ok {
		return
	}
	f := leaveFilterFromQuery(r)
	f.Status = "approved"
	f.NoPhoneKeyword = true
	list, total, err := s.store.CommunityLeaves(r.Context(), u.ID, f)
	if err != nil {
		s.postFail(w, r, err)
		return
	}
	buildings, _ := s.store.CommunityBuildings(r.Context(), u.ID)
	b := s.base(w, r, "community_leaves", "请假查询")
	s.renderPage(w, r, "community_leaves", communityLeavesData{
		baseData: b, Leaves: list, Total: total, Filter: f, Buildings: buildings, Query: r.URL.RawQuery,
		PageNo: f.Page, Pages: pagesOf(total, 50),
	})
}

func (s *Server) communityLeaveDetail(w http.ResponseWriter, r *http.Request) {
	u, ok := s.require(w, r, "community")
	if !ok {
		return
	}
	id, err := parseID(r, "id")
	if err != nil {
		s.error(w, r, http.StatusNotFound, "假条不存在", "链接中的假条编号无效。")
		return
	}
	l, err := s.store.GetLeave(r.Context(), id)
	if err != nil || !s.store.CanViewLeave(r.Context(), u, l) {
		s.error(w, r, http.StatusForbidden, "无权查看", "无法查看：社区老师只能查看负责楼栋的已批准请假（不含原因）。")
		return
	}
	edits, _ := s.store.LeaveTimeEdits(r.Context(), l.ID)
	var list []leaveEdit
	for _, e := range edits {
		list = append(list, convertLeaveEdit(e))
	}
	b := s.base(w, r, "community_leave", "请假详情")
	s.renderPage(w, r, "community_leave", communityLeaveData{baseData: b, Leave: l, Edits: list})
}
