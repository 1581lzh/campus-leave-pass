package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"leavepass/internal/secure"
	"leavepass/internal/validate"
)

// CreateAdmin creates another administrator with the default password AA.
func (s *Store) CreateAdmin(ctx context.Context, actor User, username, phone, password, ip string) (User, error) {
	if actor.Role != "admin" {
		return User{}, errors.New("只有管理员可以创建管理员")
	}
	display, norm, err := validate.NormalizeUsername(username)
	if err != nil {
		return User{}, err
	}
	ph, err := validate.NormalizePhone(phone)
	if err != nil {
		return User{}, err
	}
	if strings.TrimSpace(password) == "" {
		password = "AA"
	}
	if err := validate.ValidatePassword(password, true); err != nil {
		return User{}, err
	}
	if _, err := s.GetUserByUsername(ctx, norm); err == nil {
		return User{}, fmt.Errorf("用户名已被使用，可用：%s", strings.Join(validate.UsernameSuggestions(display), "、"))
	}
	var tmp int64
	if err := s.DB.QueryRowContext(ctx, `SELECT id FROM users WHERE phone=?`, ph).Scan(&tmp); err == nil {
		return User{}, errors.New("该手机号已被使用")
	}
	hash, err := secure.HashPassword(password)
	if err != nil {
		return User{}, err
	}
	now := s.NowUnix()
	initial := 0
	if password == "AA" {
		initial = 1
	}
	res, err := s.DB.ExecContext(ctx, `INSERT INTO users (phone,username,username_norm,password_hash,role,status,
		password_is_initial,created_by,created_at,updated_at) VALUES (?,?,?,?, 'admin', 'active', ?, ?,?,?)`,
		ph, display, norm, hash, initial, actor.ID, now, now)
	if err != nil {
		return User{}, err
	}
	id, _ := res.LastInsertId()
	_ = s.Audit(ctx, nil, actor, "admin.create", "user", id, "创建管理员 "+display, ip)
	return s.GetUserByID(ctx, id)
}

// CountActiveAdmins counts usable administrator accounts.
func (s *Store) CountActiveAdmins(ctx context.Context) int {
	var n int
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role='admin' AND status='active'`).Scan(&n)
	return n
}

// SoftDeleteUser starts the 7-day read-only window and handles role-specific
// class consequences.
func (s *Store) SoftDeleteUser(ctx context.Context, actor User, userID int64, reason, ip string) error {
	if actor.Role != "admin" {
		return ErrForbidden
	}
	if userID == actor.ID {
		return errors.New("不能软删除自己的账号")
	}
	var err error
	reason, err = validate.ValidateRejectReason(reason)
	if err != nil {
		return err
	}
	now := s.NowUnix()
	return s.withTx(ctx, func(tx *sql.Tx) error {
		u, err := scanUser(tx.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id=?`, userID))
		if err != nil {
			return ErrNotFound
		}
		if u.Status != "active" {
			return errors.New("该账号已是停用状态")
		}
		if u.Role == "admin" && s.CountActiveAdmins(ctx) <= 1 {
			return errors.New("不能停用最后一个可用管理员")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE users SET status='soft_deleted', deleted_at=?, purge_after=?,
			delete_reason=?, updated_at=? WHERE id=?`, now, now+SoftDeleteTTL, reason, now, userID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=?`, userID); err != nil {
			return err
		}
		if u.Role == "student" {
			if _, err := tx.ExecContext(ctx, `UPDATE leave_requests SET retain_until=? WHERE student_user_id=?`,
				now+LeaveRetainTTL, userID); err != nil {
				return err
			}
		}
		if u.Role == "teacher" {
			var classID, counselorID int64
			var className string
			err := tx.QueryRowContext(ctx, `SELECT id, COALESCE(counselor_user_id,0), name FROM classes WHERE teacher_user_id=?`, userID).
				Scan(&classID, &counselorID, &className)
			if err == nil {
				if err := s.markUnclaimedTx(ctx, tx, classID, userID, now); err != nil {
					return err
				}
				_ = s.Notify(ctx, tx, counselorID, "class_unclaimed", "班级进入待认领", className+" 的班主任账号已停用，班级已开始 7 天清理倒计时。", "/t/classes/"+itoa(classID), 0)
				_ = s.NotifyAdmins(ctx, tx, "class_unclaimed", "班级进入待认领", className+" 的班主任账号已停用，将在 7 天后清理。", "/admin/classes")
			} else if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		if u.Role == "counselor" {
			rows, err := tx.QueryContext(ctx, `SELECT id, COALESCE(teacher_user_id,0), name FROM classes WHERE counselor_user_id=?`, userID)
			if err != nil {
				return err
			}
			type cl struct {
				id, teacher int64
				name        string
			}
			var list []cl
			for rows.Next() {
				var c cl
				if err := rows.Scan(&c.id, &c.teacher, &c.name); err != nil {
					rows.Close()
					return err
				}
				list = append(list, c)
			}
			rows.Close()
			for _, c := range list {
				_, _ = tx.ExecContext(ctx, `UPDATE classes SET counselor_user_id=NULL, version=version+1, updated_at=? WHERE id=?`, now, c.id)
				_ = s.Notify(ctx, tx, c.teacher, "counselor_left", "辅导员账号已停用", classNameOr(c.name)+" 的辅导员账号已停用，班级暂无辅导员。", "/t/classes/"+itoa(c.id), 0)
			}
			_, _ = tx.ExecContext(ctx, `UPDATE class_counselor_requests SET status='cancelled', responded_at=?
				WHERE counselor_user_id=? AND status='pending'`, now, userID)
		}
		return s.Audit(ctx, tx, actor, "user.soft_delete", "user", userID,
			fmt.Sprintf("停用 %s（%s），原因：%s", u.Username, u.Role, reason), ip)
	})
}

func classNameOr(s string) string { return s }

// PurgeExpiredUsers physically removes accounts whose 7-day window elapsed.
func (s *Store) PurgeExpiredUsers(ctx context.Context) (int, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id FROM users WHERE status='soft_deleted' AND purge_after<=? LIMIT 200`, s.NowUnix())
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	n := 0
	for _, id := range ids {
		if _, err := s.DB.ExecContext(ctx, `DELETE FROM users WHERE id=? AND status='soft_deleted' AND purge_after<=?`,
			id, s.NowUnix()); err == nil {
			n++
		}
	}
	return n, nil
}

// FixStudentNo changes a student's globally unique number and releases the old
// one, revoking all sessions.
func (s *Store) FixStudentNo(ctx context.Context, actor User, studentID int64, newNo, ip string) error {
	if actor.Role != "admin" {
		return ErrForbidden
	}
	display, norm, err := validate.NormalizeStudentNo(newNo)
	if err != nil {
		return err
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		u, err := scanUser(tx.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id=? AND role='student'`, studentID))
		if err != nil {
			return ErrNotFound
		}
		var exists int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE student_no_norm=? AND id<>?`, norm, studentID).Scan(&exists); err == nil {
			return errors.New("该学号已被占用（含已停用账号），请先清理")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE users SET student_no=?, student_no_norm=?, updated_at=? WHERE id=?`,
			display, norm, s.NowUnix(), studentID); err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return errors.New("该学号已被占用")
			}
			return err
		}
		_, _ = tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=?`, studentID)
		return s.Audit(ctx, tx, actor, "student.fix_no", "student", studentID,
			fmt.Sprintf("%s -> %s（原姓名 %s）", u.StudentNo, display, u.Username), ip)
	})
}

// AdjustStudentClass moves a student to another class, keeping dorm data.
func (s *Store) AdjustStudentClass(ctx context.Context, actor User, studentID, classID int64, ip string) error {
	if actor.Role != "admin" {
		return ErrForbidden
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		u, err := scanUser(tx.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id=? AND role='student'`, studentID))
		if err != nil {
			return ErrNotFound
		}
		var className string
		if err := tx.QueryRowContext(ctx, `SELECT name FROM classes WHERE id=?`, classID).Scan(&className); err != nil {
			return errors.New("目标班级不存在")
		}
		var oldClass int64
		var oldName, dormB, dormR string
		err = tx.QueryRowContext(ctx, `SELECT COALESCE(sc.class_id,0), COALESCE(c.name,''), sc.dorm_building, sc.dorm_room
			FROM student_classes sc LEFT JOIN classes c ON c.id=sc.class_id WHERE sc.user_id=?`, studentID).
			Scan(&oldClass, &oldName, &dormB, &dormR)
		if errors.Is(err, sql.ErrNoRows) {
			// The student was left without a class by disbanding their class;
			// the relationship row was deleted with the class. Recreate it so
			// the administrator can transfer the student into a new class.
			now := s.NowUnix()
			if _, err := tx.ExecContext(ctx, `INSERT INTO student_classes
				(user_id,class_id,dorm_building,dorm_room,imported_at,joined_at)
				VALUES (?,?,'','',?,?)`, studentID, classID, now, now); err != nil {
				return err
			}
			_ = s.Notify(ctx, tx, studentID, "class_changed", "班级已调整", "管理员已将你转入 "+className+"。", "/s/profile", 0)
			return s.Audit(ctx, tx, actor, "student.adjust_class", "student", studentID,
				fmt.Sprintf("%s（%s）：暂无班级 -> %s", u.Username, u.StudentNo, className), ip)
		}
		if err != nil {
			return err
		}
		if oldClass == classID {
			return errors.New("学生已在该班级")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE student_classes SET class_id=? WHERE user_id=?`, classID, studentID); err != nil {
			return err
		}
		_ = s.Notify(ctx, tx, studentID, "class_changed", "班级已调整", "管理员已将你转入 "+className+"，原班级请假记录保持不变。", "/s/profile", 0)
		auditOld := oldName
		if oldClass == 0 {
			auditOld = "暂无班级"
		}
		return s.Audit(ctx, tx, actor, "student.adjust_class", "student", studentID,
			fmt.Sprintf("%s（%s）：%s -> %s", u.Username, u.StudentNo, auditOld, className), ip)
	})
}

// ChangeStaffPhone replaces a staff/admin phone after verification.
func (s *Store) ChangeStaffPhone(ctx context.Context, actor User, userID int64, newPhone, ip string) error {
	if actor.Role != "admin" {
		return ErrForbidden
	}
	phone, err := validate.NormalizePhone(newPhone)
	if err != nil {
		return err
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		u, err := scanUser(tx.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id=?`, userID))
		if err != nil || u.Role == "student" {
			return errors.New("只能修改教职工或管理员手机号")
		}
		var exists int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE phone=? AND id<>?`, phone, userID).Scan(&exists); err == nil {
			return errors.New("该手机号已被使用")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE users SET phone=?, updated_at=? WHERE id=?`, phone, s.NowUnix(), userID); err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return errors.New("该手机号已被使用")
			}
			return err
		}
		return s.Audit(ctx, tx, actor, "user.change_phone", "user", userID,
			fmt.Sprintf("%s：%s -> %s", u.Username, validate.MaskPhone(u.Phone), validate.MaskPhone(phone)), ip)
	})
}

// GenerateAdminResetLink creates a 10-minute one-time reset token for a user.
func (s *Store) GenerateAdminResetLink(ctx context.Context, actor User, userID int64, ip string) (string, error) {
	if actor.Role != "admin" {
		return "", ErrForbidden
	}
	u, err := s.GetUserByID(ctx, userID)
	if err != nil {
		return "", ErrNotFound
	}
	raw, err := s.CreatePasswordResetToken(ctx, userID, actor.ID, ip)
	if err != nil {
		return "", err
	}
	_ = s.Audit(ctx, nil, actor, "user.reset_link", "user", userID, "生成 10 分钟重置链接："+u.Username, ip)
	return s.BaseURL() + "/reset-password?token=" + raw, nil
}

// ListUsers lists accounts with optional role keyword filter.
func (s *Store) ListUsers(ctx context.Context, role, keyword string, limit int) ([]User, error) {
	if limit <= 0 {
		limit = 200
	}
	q := `SELECT ` + userCols + ` FROM users WHERE 1=1`
	var args []any
	if role != "" {
		q += ` AND role=?`
		args = append(args, role)
	}
	if kw := strings.TrimSpace(keyword); kw != "" {
		q += ` AND (username LIKE ? OR COALESCE(student_no,'') LIKE ? OR COALESCE(phone,'') LIKE ?)`
		like := "%" + kw + "%"
		args = append(args, like, like, like)
	}
	q += ` ORDER BY role, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// DormEditRow is one dorm change audit record.
type DormEditRow struct {
	ID          int64
	StudentName string
	StudentNo   string
	ClassName   string
	OldBuilding string
	OldRoom     string
	NewBuilding string
	NewRoom     string
	EditorName  string
	EditorRole  string
	Reason      string
	CreatedAt   int64
}

// ListDormEdits lists dorm change history for one student.
func (s *Store) ListDormEdits(ctx context.Context, studentID int64) ([]DormEditRow, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, student_name_snapshot, student_no_snapshot, class_name_snapshot,
		old_building, old_room, new_building, new_room, editor_name_snapshot, editor_role, reason, created_at
		FROM student_dorm_edits WHERE student_user_id=? ORDER BY created_at DESC`, studentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DormEditRow
	for rows.Next() {
		var r DormEditRow
		if err := rows.Scan(&r.ID, &r.StudentName, &r.StudentNo, &r.ClassName, &r.OldBuilding, &r.OldRoom,
			&r.NewBuilding, &r.NewRoom, &r.EditorName, &r.EditorRole, &r.Reason, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AdminStats is the dashboard summary.
type AdminStats struct {
	UnclaimedClasses int
	ActiveStudents   int
	PendingLeaves    int
	PendingResets    int
	AvailableCodes   int
	RecentImports    int
}

// Stats gathers dashboard counters.
func (s *Store) Stats(ctx context.Context) AdminStats {
	var st AdminStats
	now := s.NowUnix()
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM classes WHERE teacher_user_id IS NULL`).Scan(&st.UnclaimedClasses)
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role='student' AND status='active'`).Scan(&st.ActiveStudents)
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM leave_requests WHERE status='pending'`).Scan(&st.PendingLeaves)
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM password_reset_requests WHERE status='pending' AND expires_at>?`, now).Scan(&st.PendingResets)
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM registration_codes WHERE active=1 AND used_count<max_uses AND valid_from<=? AND valid_until>=?`, now, now).Scan(&st.AvailableCodes)
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM student_import_batches WHERE created_at>=?`, now-7*86400).Scan(&st.RecentImports)
	return st
}

// ListAuditLogs lists recent admin actions.
func (s *Store) ListAuditLogs(ctx context.Context, limit int) ([]AuditLog, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT `+auditCols+` FROM admin_audit_logs ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditLog
	for rows.Next() {
		a, err := scanAuditLog(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
