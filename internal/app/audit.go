package app

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"leavepass/internal/validate"
)

const auditCols = `id, COALESCE(actor_user_id,0), actor_name, action, target_type, COALESCE(target_id,0),
	COALESCE(target_class_id,0), target_class_name, target_department, target_building,
	detail, ip, created_at`

func scanAuditLog(r rowScanner) (AuditLog, error) {
	var a AuditLog
	err := r.Scan(&a.ID, &a.ActorUserID, &a.ActorName, &a.Action, &a.TargetType, &a.TargetID,
		&a.TargetClassID, &a.TargetClassName, &a.TargetDepartment, &a.TargetBuilding,
		&a.Detail, &a.IP, &a.CreatedAt)
	return a, err
}

// AuditFilter selects administrator audit rows. Empty fields are ignored.
type AuditFilter struct {
	Actor       string // actor_name OR actor username
	Target      string // target user's username/student_no for target_type user/student
	Action      string
	TargetType  string
	ClassID     int64
	Department  string
	Building    string
	Keyword     string // detail OR ip OR action
	CreatedFrom string // CST date, inclusive
	CreatedTo   string // CST date, inclusive
	Page        int
	PageSize    int
}

// Paging returns normalized paging values.
func (f AuditFilter) Paging() (limit, offset int) {
	size := f.PageSize
	if size <= 0 {
		size = 50
	}
	page := f.Page
	if page < 1 {
		page = 1
	}
	return size, (page - 1) * size
}

func (f AuditFilter) where() (string, []any, error) {
	var clauses []string
	var args []any
	add := func(cond string, vals ...any) {
		clauses = append(clauses, cond)
		args = append(args, vals...)
	}
	if actor := validate.NFKC(f.Actor); actor != "" {
		like := "%" + actor + "%"
		add(`(a.actor_name LIKE ? OR a.actor_user_id IN
			(SELECT id FROM users WHERE username LIKE ?))`, like, like)
	}
	if target := validate.NFKC(f.Target); target != "" {
		like := "%" + target + "%"
		studentNoLike := "%" + strings.ToUpper(target) + "%"
		add(`(a.target_type IN ('user','student') AND a.target_id IN
			(SELECT id FROM users WHERE username LIKE ? OR COALESCE(student_no,'') LIKE ?))`,
			like, studentNoLike)
	}
	if action := strings.TrimSpace(f.Action); action != "" {
		add(`a.action=?`, action)
	}
	if targetType := strings.TrimSpace(f.TargetType); targetType != "" {
		add(`a.target_type=?`, targetType)
	}
	if f.ClassID > 0 {
		add(`a.target_class_id=?`, f.ClassID)
	}
	if department := validate.NFKC(f.Department); department != "" {
		add(`a.target_department=?`, department)
	}
	if building := validate.NFKC(f.Building); building != "" {
		add(`a.target_building=?`, building)
	}
	if keyword := validate.NFKC(f.Keyword); keyword != "" {
		like := "%" + keyword + "%"
		add(`(a.detail LIKE ? OR a.ip LIKE ? OR a.action LIKE ?)`, like, like, like)
	}
	if f.CreatedFrom != "" || f.CreatedTo != "" {
		start, end, err := DateBounds(f.CreatedFrom, f.CreatedTo)
		if err != nil {
			return "", nil, err
		}
		if start > 0 {
			add(`a.created_at>=?`, start)
		}
		if end > 0 {
			add(`a.created_at<?`, end)
		}
	}
	if len(clauses) == 0 {
		return "", nil, nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args, nil
}

// SearchAuditLogs returns filtered audit rows and the unpaginated total.
func (s *Store) SearchAuditLogs(ctx context.Context, f AuditFilter) ([]AuditLog, int, error) {
	where, args, err := f.where()
	if err != nil {
		return nil, 0, err
	}
	var total int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM admin_audit_logs a`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit, offset := f.Paging()
	rows, err := s.DB.QueryContext(ctx, `SELECT `+auditCols+` FROM admin_audit_logs a`+where+
		` ORDER BY a.created_at DESC, a.id DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []AuditLog
	for rows.Next() {
		a, err := scanAuditLog(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, a)
	}
	return out, total, rows.Err()
}

// AuditFacets lists filter choices, including historical class snapshots.
type AuditFacets struct {
	Classes     []Class
	Departments []string
	Buildings   []string
	Actions     []string
	TargetTypes []string
}

// AuditFacets gathers stable filter values for the admin audit page.
func (s *Store) AuditFacets(ctx context.Context) (AuditFacets, error) {
	var out AuditFacets
	current := map[int64]bool{}
	rows, err := s.DB.QueryContext(ctx, `SELECT id,name,department FROM classes ORDER BY name, id`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var c Class
		if err := rows.Scan(&c.ID, &c.Name, &c.Department); err != nil {
			rows.Close()
			return out, err
		}
		current[c.ID] = true
		out.Classes = append(out.Classes, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return out, err
	}
	rows.Close()

	// Newest snapshot first per class; names from still-existing classes win.
	firstHistorical := len(out.Classes)
	historical := map[int64]int{}
	rows, err = s.DB.QueryContext(ctx, `SELECT target_class_id,target_class_name,target_department
		FROM admin_audit_logs
		WHERE target_class_id IS NOT NULL AND target_class_id>0
		ORDER BY target_class_id, id DESC`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var id int64
		var name, department string
		if err := rows.Scan(&id, &name, &department); err != nil {
			rows.Close()
			return out, err
		}
		if current[id] {
			continue
		}
		if idx, ok := historical[id]; ok {
			if out.Classes[idx].Name == "" && strings.TrimSpace(name) != "" {
				out.Classes[idx].Name = name
			}
			continue
		}
		historical[id] = len(out.Classes)
		out.Classes = append(out.Classes, Class{ID: id, Name: name, Department: department})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return out, err
	}
	rows.Close()
	for i := firstHistorical; i < len(out.Classes); i++ {
		if strings.TrimSpace(out.Classes[i].Name) == "" {
			out.Classes[i].Name = "班级 #" + strconv.FormatInt(out.Classes[i].ID, 10)
		}
	}
	sort.Slice(out.Classes, func(i, j int) bool {
		if out.Classes[i].Name != out.Classes[j].Name {
			return out.Classes[i].Name < out.Classes[j].Name
		}
		return out.Classes[i].ID < out.Classes[j].ID
	})

	departments := map[string]bool{}
	for _, c := range out.Classes {
		if c.Department != "" {
			departments[c.Department] = true
		}
	}
	historicalDepartments, err := s.auditStringColumn(ctx, `SELECT DISTINCT target_department
		FROM admin_audit_logs WHERE target_department<>'' ORDER BY target_department`)
	if err != nil {
		return out, err
	}
	for _, d := range historicalDepartments {
		departments[d] = true
	}
	out.Departments = sortedKeys(departments)

	buildings := map[string]bool{}
	for _, b := range s.CampusBuildings() {
		if b = strings.TrimSpace(b); b != "" {
			buildings[b] = true
		}
	}
	historicalBuildings, err := s.auditStringColumn(ctx, `SELECT DISTINCT target_building
		FROM admin_audit_logs WHERE target_building<>'' ORDER BY target_building`)
	if err != nil {
		return out, err
	}
	for _, b := range historicalBuildings {
		buildings[b] = true
	}
	out.Buildings = sortedKeys(buildings)

	out.Actions, err = s.auditStringColumn(ctx, `SELECT DISTINCT action
		FROM admin_audit_logs WHERE action<>'' ORDER BY action`)
	if err != nil {
		return out, err
	}
	out.TargetTypes, err = s.auditStringColumn(ctx, `SELECT DISTINCT target_type
		FROM admin_audit_logs WHERE target_type<>'' ORDER BY target_type`)
	if err != nil {
		return out, err
	}
	return out, nil
}

func (s *Store) auditStringColumn(ctx context.Context, query string) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
