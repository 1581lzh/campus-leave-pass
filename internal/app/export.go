package app

import (
	"context"
	"errors"
	"strings"

	"leavepass/internal/validate"
)

// MaxExportRows bounds one Excel export. The HTTP layer reports a friendly
// error when the query hits this limit so a single request cannot exhaust
// memory or block the service for minutes.
const MaxExportRows = 100000

// ExportQuery is the validated filter for a leave-record export. Scope,
// ClassID, Building and Department are administrator-only; the store enforces
// every role's visibility before applying them.
type ExportQuery struct {
	Scope      string // all, class, building, department (administrator only)
	ClassID    int64
	Building   string
	Department string
	Student    string // exact student number or exact name
	StartFrom  int64  // inclusive, 0 = open
	StartTo    int64  // exclusive, 0 = open
	Limit      int
}

// ExportLeaves returns the leave rows an actor may export.
//
// The permission boundary lives here, not in the handler: a teacher can only
// see their current class plus leaves stamped with their own teacher snapshot;
// a counselor likewise; a community teacher only approved leaves in their
// buildings and never the contact phone (the HTTP layer omits that column).
// Archived rows are included because "past records" must survive a class
// cleanup, and the same snapshot columns still carry the original ownership.
func (s *Store) ExportLeaves(ctx context.Context, actor User, q ExportQuery) ([]Leave, error) {
	where := []string{"1=1"}
	var args []any

	switch actor.Role {
	case "admin":
		// No row restriction.
	case "teacher":
		where = append(where, "(l.teacher_user_id=? OR l.class_id IN (SELECT id FROM classes WHERE teacher_user_id=?))")
		args = append(args, actor.ID, actor.ID)
	case "counselor":
		where = append(where, "(l.counselor_user_id=? OR l.class_id IN (SELECT id FROM classes WHERE counselor_user_id=?))")
		args = append(args, actor.ID, actor.ID)
	case "community":
		buildings, err := s.CommunityBuildings(ctx, actor.ID)
		if err != nil {
			return nil, err
		}
		if len(buildings) == 0 {
			return nil, nil
		}
		where = append(where, "l.status='approved'")
		in := make([]string, 0, len(buildings))
		for _, b := range buildings {
			in = append(in, "?")
			args = append(args, b)
		}
		where = append(where, "l.dorm_building IN ("+strings.Join(in, ",")+")")
	default:
		return nil, ErrForbidden
	}

	if q.StartFrom > 0 && q.StartTo > 0 && q.StartFrom >= q.StartTo {
		return nil, errors.New("开始日期不能晚于结束日期")
	}
	if q.StartFrom > 0 {
		where = append(where, "l.start_at>=?")
		args = append(args, q.StartFrom)
	}
	if q.StartTo > 0 {
		where = append(where, "l.start_at<?")
		args = append(args, q.StartTo)
	}

	if actor.Role == "admin" {
		switch q.Scope {
		case "", "all":
		case "class":
			if q.ClassID <= 0 {
				return nil, errors.New("请选择要导出的班级")
			}
			where = append(where, "l.class_id=?")
			args = append(args, q.ClassID)
		case "building":
			b, err := validate.NormalizeBuilding(q.Building)
			if err != nil {
				return nil, errors.New("请选择要导出的楼栋")
			}
			where = append(where, "l.dorm_building=?")
			args = append(args, b)
		case "department":
			d := validate.NFKC(q.Department)
			if d == "" {
				return nil, errors.New("请选择要导出的专业系")
			}
			where = append(where, "l.department=?")
			args = append(args, d)
		default:
			return nil, errors.New("无效的导出范围")
		}
	} else {
		if q.ClassID > 0 {
			// Optional narrowing; the role clause above still restricts the set.
			where = append(where, "l.class_id=?")
			args = append(args, q.ClassID)
		}
		if actor.Role == "community" && strings.TrimSpace(q.Building) != "" {
			b, err := validate.NormalizeBuilding(q.Building)
			if err != nil {
				return nil, errors.New("楼栋名无效")
			}
			where = append(where, "l.dorm_building=?")
			args = append(args, b)
		}
	}

	if kw := validate.NFKC(q.Student); kw != "" {
		if len([]rune(kw)) > 32 {
			return nil, errors.New("学生筛选条件过长")
		}
		where = append(where, "(l.student_no=? OR l.student_name=?)")
		args = append(args, strings.ToUpper(kw), kw)
	}

	limit := q.Limit
	if limit <= 0 || limit > MaxExportRows+1 {
		limit = MaxExportRows + 1
	}
	args = append(args, limit)
	query := `SELECT ` + leaveCols + ` FROM leave_requests l WHERE ` +
		strings.Join(where, " AND ") + ` ORDER BY l.start_at DESC, l.id DESC LIMIT ?`
	return s.queryLeaves(ctx, query, args...)
}

// LeaveExportFacets returns the buildings and departments that exist in leave
// snapshots and current classes, so administrators can pick a concrete scope.
func (s *Store) LeaveExportFacets(ctx context.Context) (buildings, departments []string, err error) {
	buildings, err = s.scanStrings(ctx, `SELECT b FROM (
		SELECT DISTINCT dorm_building AS b FROM leave_requests WHERE dorm_building<>''
		UNION SELECT building FROM community_buildings
	) ORDER BY b`)
	if err != nil {
		return nil, nil, err
	}
	departments, err = s.scanStrings(ctx, `SELECT d FROM (
		SELECT DISTINCT department AS d FROM leave_requests WHERE department<>''
		UNION SELECT department FROM classes WHERE department<>''
	) ORDER BY d`)
	if err != nil {
		return nil, nil, err
	}
	return buildings, departments, nil
}

func (s *Store) scanStrings(ctx context.Context, query string) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
