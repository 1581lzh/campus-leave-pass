package app

import (
	"context"
	"database/sql"
	"errors"

	"leavepass/internal/validate"
)

// CanViewLeave applies the per-role leave visibility matrix.
func (s *Store) CanViewLeave(ctx context.Context, actor User, l Leave) bool {
	switch actor.Role {
	case "admin":
		return true
	case "student":
		return l.StudentUserID == actor.ID
	case "teacher", "counselor":
		if actor.Role == "teacher" && l.TeacherUserID == actor.ID {
			return true
		}
		if actor.Role == "counselor" && l.CounselorUserID == actor.ID {
			return true
		}
		if l.ClassID == 0 {
			return false
		}
		c, err := s.GetClass(ctx, l.ClassID)
		if err != nil {
			return false
		}
		if actor.Role == "teacher" {
			return c.TeacherUserID == actor.ID
		}
		return c.CounselorUserID == actor.ID
	case "community":
		if l.Status != "approved" || l.ArchivedAt != 0 {
			return false
		}
		buildings, _ := s.CommunityBuildings(ctx, actor.ID)
		for _, b := range buildings {
			if b == l.DormBuilding {
				return true
			}
		}
	}
	return false
}

// AllClasses lists every class with its active student count.
func (s *Store) AllClasses(ctx context.Context) ([]Class, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+classCols+`,
		(SELECT COUNT(*) FROM student_classes sc JOIN users u ON u.id=sc.user_id
		 WHERE sc.class_id=c.id AND u.status='active')`+classJoins+` ORDER BY c.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Class
	for rows.Next() {
		var c Class
		if err := rows.Scan(&c.ID, &c.Name, &c.NameNorm, &c.Department,
			&c.TeacherUserID, &c.CounselorUserID, &c.FormerTeacherID,
			&c.UnclaimedAt, &c.DeleteDueAt, &c.Reminded3dAt, &c.Reminded1dAt,
			&c.Version, &c.CreatedBy, &c.CreatedAt, &c.UpdatedAt,
			&c.TeacherName, &c.CounselorName, &c.FormerTeacherName, &c.StudentCount); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetArchivedClass loads one archived class snapshot.
func (s *Store) GetArchivedClass(ctx context.Context, id int64) (ArchivedClass, error) {
	var a ArchivedClass
	err := s.DB.QueryRowContext(ctx, `SELECT id, source_class_id, class_name, department,
		COALESCE(last_teacher_user_id,0), last_teacher_name, COALESCE(last_counselor_user_id,0), last_counselor_name,
		cleaned_at, retain_until, student_count, leave_count, reason, created_at
		FROM archived_classes WHERE id=?`, id).Scan(&a.ID, &a.SourceClassID, &a.ClassName, &a.Department,
		&a.LastTeacherUserID, &a.LastTeacherName, &a.LastCounselorID, &a.LastCounselorName,
		&a.CleanedAt, &a.RetainUntil, &a.StudentCount, &a.LeaveCount, &a.Reason, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

// CommunityUpcoming lists approved leaves starting today or later for the
// teacher's buildings.
func (s *Store) CommunityUpcoming(ctx context.Context, userID int64) ([]Leave, error) {
	buildings, err := s.CommunityBuildings(ctx, userID)
	if err != nil || len(buildings) == 0 {
		return nil, err
	}
	q := `SELECT ` + leaveCols + ` FROM leave_requests WHERE archived_at IS NULL AND status='approved'
		AND start_at>=? AND dorm_building IN (`
	args := []any{StartOfDayCST(s.Now()).Unix()}
	for i, b := range buildings {
		if i > 0 {
			q += ","
		}
		q += "?"
		args = append(args, b)
	}
	q += `) ORDER BY start_at LIMIT 500`
	return s.queryLeaves(ctx, q, args...)
}

// ReportRow is one grouped report line.
type ReportRow struct {
	Key       string
	Total     int
	Approved  int
	Pending   int
	Rejected  int
	Cancelled int
}

// AdminReport groups normal leaves by a whitelisted dimension.
func (s *Store) AdminReport(ctx context.Context, dimension string) ([]ReportRow, error) {
	col := ""
	switch dimension {
	case "building":
		col = "dorm_building"
	case "class":
		col = "class_name"
	case "teacher":
		col = "teacher_name"
	case "counselor":
		col = "counselor_name"
	default:
		return nil, errors.New("无效的汇总维度")
	}
	q := `SELECT COALESCE(NULLIF(` + col + `,''),'（未填写）'), COUNT(*),
		SUM(CASE WHEN status='approved' THEN 1 ELSE 0 END),
		SUM(CASE WHEN status='pending' THEN 1 ELSE 0 END),
		SUM(CASE WHEN status='rejected' THEN 1 ELSE 0 END),
		SUM(CASE WHEN status='cancelled' THEN 1 ELSE 0 END)
		FROM leave_requests WHERE archived_at IS NULL GROUP BY 1 ORDER BY 2 DESC`
	rows, err := s.DB.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReportRow
	for rows.Next() {
		var r ReportRow
		if err := rows.Scan(&r.Key, &r.Total, &r.Approved, &r.Pending, &r.Rejected, &r.Cancelled); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ArchivedClassList lists snapshots for administrators.
func (s *Store) ArchivedClassList(ctx context.Context) ([]ArchivedClass, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, source_class_id, class_name, department,
		COALESCE(last_teacher_user_id,0), last_teacher_name, COALESCE(last_counselor_user_id,0), last_counselor_name,
		cleaned_at, retain_until, student_count, leave_count, reason, created_at
		FROM archived_classes ORDER BY cleaned_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ArchivedClass
	for rows.Next() {
		var a ArchivedClass
		if err := rows.Scan(&a.ID, &a.SourceClassID, &a.ClassName, &a.Department,
			&a.LastTeacherUserID, &a.LastTeacherName, &a.LastCounselorID, &a.LastCounselorName,
			&a.CleanedAt, &a.RetainUntil, &a.StudentCount, &a.LeaveCount, &a.Reason, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AdminClassDetail returns class plus pending leave count and recent leaves.
func (s *Store) AdminClassDetail(ctx context.Context, classID int64) (Class, []Leave, error) {
	c, err := s.GetClass(ctx, classID)
	if err != nil {
		return c, nil, err
	}
	leaves, err := s.ClassLeaves(ctx, classID, "")
	return c, leaves, err
}

// StaffByRole finds an active staff member by exact username.
func (s *Store) StaffByRole(ctx context.Context, username, role string) (User, error) {
	_, norm, err := validate.NormalizeUsername(username)
	if err != nil {
		return User{}, err
	}
	u, err := s.GetUserByUsername(ctx, norm)
	if err != nil || u.Role != role || u.Status != "active" {
		return User{}, ErrNotFound
	}
	return u, nil
}
