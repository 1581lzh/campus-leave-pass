package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"leavepass/internal/validate"
)

// Lifecycle durations from PLAN.md.
const (
	UnclaimedTTL       = 7 * 24 * 3600
	ArchiveRetainTTL   = 40 * 24 * 3600
	SoftDeleteTTL      = 7 * 24 * 3600
	LeaveRetainTTL     = 40 * 24 * 3600
	LeaveDeadlineTTL   = 24 * 3600
	LeaveMaxDuration   = 30 * 24 * 3600
	LeaveBackdateGrace = 5 * 60
)

// Sentinel errors surfaced with reasons in the UI.
var (
	ErrClassSwitchNeeded       = errors.New("你已有一个班级，换班后原班将立即进入待认领并开始 7 天清理倒计时")
	ErrTakeoverNeeded          = errors.New("该班已有班主任，确认后将顶替现任班主任")
	ErrCounselorTakeoverNeeded = errors.New("该班已有辅导员，确认后将顶替现任辅导员")
	ErrVersionConflict         = errors.New("班级信息已被他人修改，请刷新后重试")
	ErrDormConflict            = errors.New("宿舍信息已被他人修改，请刷新后重试")
	ErrForbidden               = errors.New("无权执行该操作")
)

const classCols = `c.id, c.name, c.name_norm, c.department,
	COALESCE(c.teacher_user_id,0), COALESCE(c.counselor_user_id,0), COALESCE(c.former_teacher_user_id,0),
	COALESCE(c.unclaimed_at,0), COALESCE(c.delete_due_at,0), COALESCE(c.reminded_3d_at,0), COALESCE(c.reminded_1d_at,0),
	c.version, COALESCE(c.created_by,0), c.created_at, c.updated_at,
	COALESCE(t.username,''), COALESCE(co.username,''), COALESCE(f.username,'')`

const classJoins = ` FROM classes c
	LEFT JOIN users t ON t.id=c.teacher_user_id
	LEFT JOIN users co ON co.id=c.counselor_user_id
	LEFT JOIN users f ON f.id=c.former_teacher_user_id`

func scanClass(r rowScanner) (Class, error) {
	var c Class
	err := r.Scan(&c.ID, &c.Name, &c.NameNorm, &c.Department, &c.TeacherUserID, &c.CounselorUserID,
		&c.FormerTeacherID, &c.UnclaimedAt, &c.DeleteDueAt, &c.Reminded3dAt, &c.Reminded1dAt,
		&c.Version, &c.CreatedBy, &c.CreatedAt, &c.UpdatedAt, &c.TeacherName, &c.CounselorName, &c.FormerTeacherName)
	return c, err
}

// GetClass loads one class with joined staff names.
func (s *Store) GetClass(ctx context.Context, id int64) (Class, error) {
	c, err := scanClass(s.DB.QueryRowContext(ctx, `SELECT `+classCols+classJoins+` WHERE c.id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM student_classes sc JOIN users u ON u.id=sc.user_id
		WHERE sc.class_id=? AND u.status='active'`, id).Scan(&c.StudentCount)
	return c, nil
}

// GetClassByName resolves the unique normalized class name.
func (s *Store) GetClassByName(ctx context.Context, norm string) (Class, error) {
	c, err := scanClass(s.DB.QueryRowContext(ctx, `SELECT `+classCols+classJoins+` WHERE c.name_norm=?`, norm))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// TeacherClass returns the class currently led by a teacher.
func (s *Store) TeacherClass(ctx context.Context, teacherID int64) (Class, error) {
	c, err := scanClass(s.DB.QueryRowContext(ctx, `SELECT `+classCols+classJoins+` WHERE c.teacher_user_id=?`, teacherID))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// ClassesFor lists the classes a teacher or counselor is responsible for.
func (s *Store) ClassesFor(ctx context.Context, u User) ([]Class, error) {
	var rows *sql.Rows
	var err error
	switch u.Role {
	case "teacher":
		rows, err = s.DB.QueryContext(ctx, `SELECT `+classCols+classJoins+` WHERE c.teacher_user_id=? ORDER BY c.name`, u.ID)
	case "counselor":
		rows, err = s.DB.QueryContext(ctx, `SELECT `+classCols+classJoins+` WHERE c.counselor_user_id=? ORDER BY c.name`, u.ID)
	case "admin":
		rows, err = s.DB.QueryContext(ctx, `SELECT `+classCols+classJoins+` ORDER BY c.name`)
	default:
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Class
	for rows.Next() {
		c, err := scanClass(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ClassStudents lists students, optionally including inactive ones.
func (s *Store) ClassStudents(ctx context.Context, classID int64, includeInactive bool, keyword string) ([]StudentRow, error) {
	q := `SELECT u.id, u.username, COALESCE(u.student_no,''), u.status, sc.class_id, c.name, c.department,
		sc.dorm_building, sc.dorm_room, COALESCE(t.username,''), COALESCE(co.username,''), sc.imported_at
		FROM student_classes sc
		JOIN users u ON u.id=sc.user_id
		JOIN classes c ON c.id=sc.class_id
		LEFT JOIN users t ON t.id=c.teacher_user_id
		LEFT JOIN users co ON co.id=c.counselor_user_id
		WHERE sc.class_id=?`
	args := []any{classID}
	if !includeInactive {
		q += ` AND u.status='active'`
	}
	if kw := strings.TrimSpace(keyword); kw != "" {
		q += ` AND (u.username LIKE ? OR u.student_no LIKE ?)`
		args = append(args, "%"+kw+"%", "%"+strings.ToUpper(kw)+"%")
	}
	q += ` ORDER BY u.student_no`
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StudentRow
	for rows.Next() {
		var st StudentRow
		if err := rows.Scan(&st.UserID, &st.Name, &st.StudentNo, &st.Status, &st.ClassID, &st.ClassName,
			&st.Department, &st.DormBuilding, &st.DormRoom, &st.TeacherName, &st.CounselorName, &st.ImportedAt); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// StudentRow is a student with class and dorm data.
type StudentRow struct {
	UserID        int64
	Name          string
	StudentNo     string
	Status        string
	ClassID       int64
	ClassName     string
	Department    string
	DormBuilding  string
	DormRoom      string
	TeacherName   string
	CounselorName string
	ImportedAt    int64
}

// Dorm renders 楼栋-房间.
func (st StudentRow) Dorm() string {
	if st.DormBuilding == "" {
		return ""
	}
	return st.DormBuilding + "-" + st.DormRoom
}

// GetStudent loads one student with class details.
func (s *Store) GetStudent(ctx context.Context, userID int64) (StudentRow, error) {
	var st StudentRow
	err := s.DB.QueryRowContext(ctx, `SELECT u.id, u.username, COALESCE(u.student_no,''), u.status,
		COALESCE(sc.class_id,0), COALESCE(c.name,''), COALESCE(c.department,''),
		COALESCE(sc.dorm_building,''), COALESCE(sc.dorm_room,''),
		COALESCE(t.username,''), COALESCE(co.username,''), COALESCE(sc.imported_at,0)
		FROM users u
		LEFT JOIN student_classes sc ON sc.user_id=u.id
		LEFT JOIN classes c ON c.id=sc.class_id
		LEFT JOIN users t ON t.id=c.teacher_user_id
		LEFT JOIN users co ON co.id=c.counselor_user_id
		WHERE u.id=? AND u.role='student'`, userID).Scan(&st.UserID, &st.Name, &st.StudentNo, &st.Status,
		&st.ClassID, &st.ClassName, &st.Department, &st.DormBuilding, &st.DormRoom,
		&st.TeacherName, &st.CounselorName, &st.ImportedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return st, ErrNotFound
	}
	return st, err
}

// SearchStudentByNo finds an active student by exact normalized student number.
func (s *Store) SearchStudentByNo(ctx context.Context, no string) (StudentRow, error) {
	norm, _, err := validate.NormalizeStudentNo(no)
	if err != nil {
		return StudentRow{}, err
	}
	var id int64
	err = s.DB.QueryRowContext(ctx, `SELECT id FROM users WHERE student_no_norm=? AND role='student' AND status='active'`, norm).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return StudentRow{}, ErrNotFound
	}
	if err != nil {
		return StudentRow{}, err
	}
	return s.GetStudent(ctx, id)
}

// CreateClass creates a class. A teacher becomes its teacher; a counselor
// becomes its counselor and the class starts the 7-day unclaimed countdown.
func (s *Store) CreateClass(ctx context.Context, actor User, name, department string, confirmSwitch bool) (Class, error) {
	if actor.Role != "teacher" && actor.Role != "counselor" {
		return Class{}, errors.New("只有班主任或辅导员可以创建班级")
	}
	display, norm, err := validate.NormalizeClassName(name)
	if err != nil {
		return Class{}, err
	}
	dept, err := validate.NormalizeDepartment(department)
	if err != nil {
		return Class{}, err
	}
	now := s.NowUnix()
	var classID int64
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `SELECT 1`); err != nil {
			return err
		}
		if actor.Role == "teacher" {
			var oldID int64
			var oldName string
			err := tx.QueryRowContext(ctx, `SELECT id,name FROM classes WHERE teacher_user_id=?`, actor.ID).Scan(&oldID, &oldName)
			if err == nil {
				if !confirmSwitch {
					return fmt.Errorf("%w（当前班级：%s）", ErrClassSwitchNeeded, oldName)
				}
				var oldCounselor int64
				_ = tx.QueryRowContext(ctx, `SELECT COALESCE(counselor_user_id,0) FROM classes WHERE id=?`, oldID).Scan(&oldCounselor)
				if err := s.markUnclaimedTx(ctx, tx, oldID, actor.ID, now); err != nil {
					return err
				}
				_ = s.Notify(ctx, tx, oldCounselor, "class_unclaimed", "班级进入待认领", oldName+" 暂无班主任，已开始 7 天清理倒计时。", "/t/classes/"+itoa(oldID), 0)
				_ = s.NotifyAdmins(ctx, tx, "class_unclaimed", "班级进入待认领", oldName+" 暂无班主任，将在 7 天后清理。", "/admin/classes")
			} else if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO classes (name,name_norm,department,teacher_user_id,counselor_user_id,unclaimed_at,delete_due_at,version,created_by,created_at,updated_at)
			VALUES (?,?,?,?,?,?,?,1,?,?,?)`, display, norm, dept,
			nullableID(ifTeacher(actor)), nullableID(ifCounselor(actor)),
			nullableTime(actor.Role == "counselor", now),
			nullableTime(actor.Role == "counselor", now+UnclaimedTTL),
			actor.ID, now, now)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return errors.New("班级名称已被使用，请换一个名称")
			}
			return err
		}
		classID, _ = res.LastInsertId()
		if actor.Role == "counselor" {
			_ = s.NotifyAdmins(ctx, tx, "class_unclaimed", "新班级待认领", display+" 暂无班主任，已开始 7 天清理倒计时。", "/admin/classes")
		}
		return nil
	})
	if err != nil {
		return Class{}, err
	}
	return s.GetClass(ctx, classID)
}

func ifTeacher(u User) int64 {
	if u.Role == "teacher" {
		return u.ID
	}
	return 0
}

func ifCounselor(u User) int64 {
	if u.Role == "counselor" {
		return u.ID
	}
	return 0
}

func nullableID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func nullableTime(ok bool, v int64) any {
	if !ok {
		return nil
	}
	return v
}

// markUnclaimedTx clears the teacher and starts the 7-day countdown.
func (s *Store) markUnclaimedTx(ctx context.Context, tx *sql.Tx, classID, formerTeacherID, now int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE classes SET teacher_user_id=NULL, former_teacher_user_id=?,
		unclaimed_at=?, delete_due_at=?, reminded_3d_at=NULL, reminded_1d_at=NULL, version=version+1, updated_at=?
		WHERE id=?`, nullableID(formerTeacherID), now, now+UnclaimedTTL, now, classID)
	return err
}

func clearUnclaimedTx(ctx context.Context, tx *sql.Tx, classID, now int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE classes SET unclaimed_at=NULL, delete_due_at=NULL, former_teacher_user_id=NULL,
		reminded_3d_at=NULL, reminded_1d_at=NULL, version=version+1, updated_at=? WHERE id=?`, now, classID)
	return err
}

// ClaimableClasses lists classes without a teacher.
func (s *Store) ClaimableClasses(ctx context.Context) ([]Class, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+classCols+classJoins+` WHERE c.teacher_user_id IS NULL ORDER BY c.delete_due_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Class
	for rows.Next() {
		c, err := scanClass(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CounselorClaimableClasses lists classes without a counselor. A counselor can
// still take over an occupied class through the exact-name search flow.
func (s *Store) CounselorClaimableClasses(ctx context.Context) ([]Class, error) {
	all, err := s.AllClasses(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Class, 0, len(all))
	for _, c := range all {
		if c.CounselorUserID == 0 {
			out = append(out, c)
		}
	}
	return out, nil
}

// ClaimCounselorClass lets a counselor claim any class. A class keeps exactly
// one counselor; a confirmed claim replaces the current one and notifies the
// replaced counselor with the time, actor and class name.
func (s *Store) ClaimCounselorClass(ctx context.Context, actor User, classID int64, confirmTakeover bool) error {
	if actor.Role != "counselor" {
		return errors.New("只有辅导员可以认领班级")
	}
	now := s.NowUnix()
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var counselorID, teacherID int64
		var counselorName, className string
		err := tx.QueryRowContext(ctx, `SELECT COALESCE(c.counselor_user_id,0), COALESCE(co.username,''),
			c.name, COALESCE(c.teacher_user_id,0)
			FROM classes c LEFT JOIN users co ON co.id=c.counselor_user_id WHERE c.id=?`, classID).
			Scan(&counselorID, &counselorName, &className, &teacherID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if counselorID == actor.ID {
			return nil
		}
		if counselorID != 0 && !confirmTakeover {
			return fmt.Errorf("%w（现任：%s）", ErrCounselorTakeoverNeeded, counselorName)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE classes SET counselor_user_id=?, version=version+1, updated_at=? WHERE id=?`,
			actor.ID, now, classID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE class_counselor_requests SET status='cancelled', responded_at=?
			WHERE class_id=? AND status='pending'`, now, classID); err != nil {
			return err
		}
		if counselorID != 0 {
			_ = s.notifyCounselorReplaced(ctx, tx, counselorID, className,
				fmt.Sprintf("辅导员 %s 认领了 %s，顶替了你的辅导员岗位", actor.Username, className), now)
		}
		_ = s.Notify(ctx, tx, teacherID, "class_counselor", "班级辅导员已调整",
			actor.Username+" 已认领 "+className+" 的辅导员岗位。", "/t/classes/"+itoa(classID), 0)
		_ = s.Notify(ctx, tx, actor.ID, "class_counselor_claimed", "班级认领成功",
			"你已成为 "+className+" 的辅导员。", "/t/classes/"+itoa(classID), 0)
		return nil
	})
}

// TakeClass lets a teacher claim an unclaimed class or take over an occupied
// one. Confirmations are required for both switching and takeover.
func (s *Store) TakeClass(ctx context.Context, actor User, classID int64, confirmTakeover, confirmSwitch bool) error {
	if actor.Role != "teacher" {
		return errors.New("只有班主任可以认领或顶替班级")
	}
	now := s.NowUnix()
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var teacherID int64
		var oldTeacherName, className string
		err := tx.QueryRowContext(ctx, `SELECT COALESCE(c.teacher_user_id,0), COALESCE(t.username,''), c.name
			FROM classes c LEFT JOIN users t ON t.id=c.teacher_user_id WHERE c.id=?`, classID).
			Scan(&teacherID, &oldTeacherName, &className)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if teacherID == actor.ID {
			return nil
		}
		if teacherID != 0 && !confirmTakeover {
			return fmt.Errorf("%w（现任：%s）", ErrTakeoverNeeded, oldTeacherName)
		}
		var myClassID int64
		var myClassName string
		err = tx.QueryRowContext(ctx, `SELECT id,name FROM classes WHERE teacher_user_id=?`, actor.ID).Scan(&myClassID, &myClassName)
		if err == nil && myClassID != classID {
			if !confirmSwitch {
				return fmt.Errorf("%w（当前班级：%s）", ErrClassSwitchNeeded, myClassName)
			}
			var oldCounselor int64
			_ = tx.QueryRowContext(ctx, `SELECT COALESCE(counselor_user_id,0) FROM classes WHERE id=?`, myClassID).Scan(&oldCounselor)
			if err := s.markUnclaimedTx(ctx, tx, myClassID, actor.ID, now); err != nil {
				return err
			}
			_ = s.Notify(ctx, tx, oldCounselor, "class_unclaimed", "班级进入待认领", myClassName+" 暂无班主任，已开始 7 天清理倒计时。", "/t/classes/"+itoa(myClassID), 0)
			_ = s.NotifyAdmins(ctx, tx, "class_unclaimed", "班级进入待认领", myClassName+" 暂无班主任，将在 7 天后清理。", "/admin/classes")
		} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE classes SET teacher_user_id=?, unclaimed_at=NULL, delete_due_at=NULL,
			former_teacher_user_id=NULL, reminded_3d_at=NULL, reminded_1d_at=NULL, version=version+1, updated_at=? WHERE id=?`,
			actor.ID, now, classID); err != nil {
			return err
		}
		if teacherID != 0 && teacherID != actor.ID {
			_ = s.Notify(ctx, tx, teacherID, "class_takeover", "你已被顶替", actor.Username+" 已成为 "+className+" 的班主任，你不再是该班班主任。", "/t/classes", 0)
		}
		var counselorID int64
		_ = tx.QueryRowContext(ctx, `SELECT COALESCE(counselor_user_id,0) FROM classes WHERE id=?`, classID).Scan(&counselorID)
		_ = s.Notify(ctx, tx, counselorID, "class_claimed", "班级已有班主任", actor.Username+" 已认领 "+className+"，班级已恢复。", "/t/classes/"+itoa(classID), 0)
		_ = s.NotifyClassStudents(ctx, tx, classID, "class_claimed", "班级已恢复", className+" 已有班主任，班级功能恢复正常。", "/s")
		return nil
	})
}

// ExitTeacher makes the actor's class unclaimed.
func (s *Store) ExitTeacher(ctx context.Context, actor User) error {
	if actor.Role != "teacher" {
		return errors.New("只有班主任可以退出班级")
	}
	now := s.NowUnix()
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var classID, counselorID int64
		var name string
		err := tx.QueryRowContext(ctx, `SELECT id, COALESCE(counselor_user_id,0), name FROM classes WHERE teacher_user_id=?`, actor.ID).
			Scan(&classID, &counselorID, &name)
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("你当前没有班级")
		}
		if err != nil {
			return err
		}
		if err := s.markUnclaimedTx(ctx, tx, classID, actor.ID, now); err != nil {
			return err
		}
		_ = s.Notify(ctx, tx, counselorID, "class_unclaimed", "班级进入待认领", name+" 暂无班主任，已开始 7 天清理倒计时。", "/t/classes/"+itoa(classID), 0)
		_ = s.Notify(ctx, tx, actor.ID, "class_unclaimed", "你已退出班级", name+" 已进入待认领，将在 7 天后清理。", "/t/classes/claim", 0)
		return s.NotifyAdmins(ctx, tx, "class_unclaimed", "班级进入待认领", name+" 暂无班主任，将在 7 天后清理。", "/admin/classes")
	})
}

// ExitCounselor removes the actor from their class.
func (s *Store) ExitCounselor(ctx context.Context, actor User, classID int64) error {
	if actor.Role != "counselor" {
		return errors.New("只有辅导员可以退出班级")
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var teacherID int64
		var name string
		err := tx.QueryRowContext(ctx, `SELECT COALESCE(teacher_user_id,0), name FROM classes WHERE id=? AND counselor_user_id=?`, classID, actor.ID).
			Scan(&teacherID, &name)
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("你不是该班辅导员")
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE classes SET counselor_user_id=NULL, version=version+1, updated_at=? WHERE id=?`, s.NowUnix(), classID); err != nil {
			return err
		}
		_ = s.Notify(ctx, tx, teacherID, "counselor_left", "辅导员已退出", actor.Username+" 已退出 "+name+" 的辅导员岗位。", "/t/classes/"+itoa(classID), 0)
		_ = s.NotifyAdmins(ctx, tx, "counselor_left", "辅导员退出班级", actor.Username+" 已退出 "+name+"。", "/admin/classes")
		return nil
	})
}

// RenameClass updates name/department with an optimistic version check.
func (s *Store) RenameClass(ctx context.Context, actor User, classID, version int64, name, department string) error {
	display, norm, err := validate.NormalizeClassName(name)
	if err != nil {
		return err
	}
	dept, err := validate.NormalizeDepartment(department)
	if err != nil {
		return err
	}
	if err := s.canManageClass(ctx, actor, classID); err != nil {
		return err
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE classes SET name=?, name_norm=?, department=?, version=version+1, updated_at=?
		WHERE id=? AND version=?`, display, norm, dept, s.NowUnix(), classID, version)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return errors.New("班级名称已被使用，请换一个名称")
		}
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrVersionConflict
	}
	_ = s.NotifyClassStudents(ctx, nil, classID, "class_renamed", "班级已更名", "班级已更名为 "+display+"。", "/s")
	return nil
}

// canManageClass reports whether actor is the class's current teacher/counselor.
func (s *Store) CanManageClass(ctx context.Context, actor User, classID int64) error {
	return s.canManageClass(ctx, actor, classID)
}

func (s *Store) canManageClass(ctx context.Context, actor User, classID int64) error {
	c, err := s.GetClass(ctx, classID)
	if err != nil {
		return err
	}
	switch actor.Role {
	case "teacher":
		if c.TeacherUserID == actor.ID {
			return nil
		}
	case "counselor":
		if c.CounselorUserID == actor.ID {
			return nil
		}
	case "admin":
		return nil
	}
	return ErrForbidden
}

// InvitationRow is a counselor invitation with joined display fields.
type InvitationRow struct {
	ID              int64
	ClassID         int64
	ClassName       string
	Department      string
	CounselorUserID int64
	InvitedBy       int64
	InviterName     string
	ReplacesUserID  int64
	ReplacesName    string
	Status          string
	Note            string
	CreatedAt       int64
	RespondedAt     int64
}

// InviteCounselor sends an in-app invitation from the class teacher.
func (s *Store) InviteCounselor(ctx context.Context, actor User, classID int64, username, note string) (int64, error) {
	if actor.Role != "teacher" {
		return 0, errors.New("只有班主任可以邀请辅导员")
	}
	if err := s.canManageClass(ctx, actor, classID); err != nil {
		return 0, errors.New("只有本班班主任可以邀请辅导员")
	}
	_, norm, err := validate.NormalizeUsername(username)
	if err != nil {
		return 0, errors.New("找不到该辅导员，请按用户名精确搜索")
	}
	var target User
	target, err = s.GetUserByUsername(ctx, norm)
	if err != nil || target.Role != "counselor" || target.Status != "active" {
		return 0, errors.New("找不到该辅导员，请确认对方已用辅导员注册码注册")
	}
	if target.ID == actor.ID {
		return 0, errors.New("不能邀请自己")
	}
	note, err = validate.ValidateOpinion(note)
	if err != nil {
		return 0, err
	}
	now := s.NowUnix()
	var inviteID int64
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		c, err := scanClass(tx.QueryRowContext(ctx, `SELECT `+classCols+classJoins+` WHERE c.id=?`, classID))
		if err != nil {
			return err
		}
		if c.CounselorUserID == target.ID {
			return errors.New("该辅导员已是本班辅导员")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE class_counselor_requests SET status='cancelled', responded_at=?
			WHERE class_id=? AND status='pending'`, now, classID); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO class_counselor_requests
			(class_id,counselor_user_id,invited_by,replaces_user_id,status,note,created_at)
			VALUES (?,?,?,?, 'pending', ?, ?)`, classID, target.ID, actor.ID, nullableID(c.CounselorUserID), note, now)
		if err != nil {
			return err
		}
		inviteID, _ = res.LastInsertId()
		body := actor.Username + " 邀请你担任 " + c.Name + " 的辅导员。"
		if c.CounselorUserID != 0 {
			body += " 接受后将顶替现任辅导员 " + c.CounselorName + "。"
		}
		return s.Notify(ctx, tx, target.ID, "counselor_invite", "新的辅导员邀请", body, "/t/class-invitations", 0)
	})
	return inviteID, err
}

// CancelCounselorInvite cancels the pending invitation of a class.
func (s *Store) CancelCounselorInvite(ctx context.Context, actor User, classID int64) error {
	if err := s.canManageClass(ctx, actor, classID); err != nil {
		return errors.New("只有本班班主任或辅导员可以取消邀请")
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE class_counselor_requests SET status='cancelled', responded_at=?
		WHERE class_id=? AND status='pending'`, s.NowUnix(), classID)
	return err
}

// PendingInvite returns the pending invitation of a class.
func (s *Store) PendingInvite(ctx context.Context, classID int64) (InvitationRow, error) {
	var r InvitationRow
	err := s.DB.QueryRowContext(ctx, `SELECT r.id, r.class_id, c.name, c.department, r.counselor_user_id,
		COALESCE(r.invited_by,0), COALESCE(i.username,''), COALESCE(r.replaces_user_id,0), COALESCE(rp.username,''),
		r.status, r.note, r.created_at, COALESCE(r.responded_at,0)
		FROM class_counselor_requests r
		JOIN classes c ON c.id=r.class_id
		LEFT JOIN users i ON i.id=r.invited_by
		LEFT JOIN users rp ON rp.id=r.replaces_user_id
		WHERE r.class_id=? AND r.status='pending'`, classID).Scan(&r.ID, &r.ClassID, &r.ClassName, &r.Department,
		&r.CounselorUserID, &r.InvitedBy, &r.InviterName, &r.ReplacesUserID, &r.ReplacesName, &r.Status,
		&r.Note, &r.CreatedAt, &r.RespondedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// InvitationsFor lists a counselor's pending invitations.
func (s *Store) InvitationsFor(ctx context.Context, counselorID int64) ([]InvitationRow, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT r.id, r.class_id, c.name, c.department, r.counselor_user_id,
		COALESCE(r.invited_by,0), COALESCE(i.username,''), COALESCE(r.replaces_user_id,0), COALESCE(rp.username,''),
		r.status, r.note, r.created_at, COALESCE(r.responded_at,0)
		FROM class_counselor_requests r
		JOIN classes c ON c.id=r.class_id
		LEFT JOIN users i ON i.id=r.invited_by
		LEFT JOIN users rp ON rp.id=r.replaces_user_id
		WHERE r.counselor_user_id=? AND r.status='pending' ORDER BY r.created_at DESC`, counselorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InvitationRow
	for rows.Next() {
		var r InvitationRow
		if err := rows.Scan(&r.ID, &r.ClassID, &r.ClassName, &r.Department, &r.CounselorUserID,
			&r.InvitedBy, &r.InviterName, &r.ReplacesUserID, &r.ReplacesName, &r.Status, &r.Note,
			&r.CreatedAt, &r.RespondedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RespondInvitation accepts or declines an invitation.
func (s *Store) RespondInvitation(ctx context.Context, actor User, inviteID int64, accept bool) error {
	if actor.Role != "counselor" {
		return errors.New("只有辅导员可以处理班级邀请")
	}
	now := s.NowUnix()
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var classID, counselorID, invitedBy, replacesID int64
		var status, className string
		err := tx.QueryRowContext(ctx, `SELECT r.class_id, r.counselor_user_id, COALESCE(r.invited_by,0), COALESCE(r.replaces_user_id,0), r.status, c.name
			FROM class_counselor_requests r JOIN classes c ON c.id=r.class_id WHERE r.id=?`, inviteID).
			Scan(&classID, &counselorID, &invitedBy, &replacesID, &status, &className)
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("邀请不存在或班级已清理")
		}
		if err != nil {
			return err
		}
		if counselorID != actor.ID || status != "pending" {
			return errors.New("该邀请已失效，请刷新后重试")
		}
		if !accept {
			if _, err := tx.ExecContext(ctx, `UPDATE class_counselor_requests SET status='declined', responded_at=? WHERE id=?`, now, inviteID); err != nil {
				return err
			}
			_ = s.Notify(ctx, tx, invitedBy, "counselor_invite_result", "辅导员已拒绝邀请", actor.Username+" 拒绝了 "+className+" 的辅导员邀请。", "/t/classes/"+itoa(classID), 0)
			return nil
		}
		var currentCounselor int64
		var currentName string
		err = tx.QueryRowContext(ctx, `SELECT COALESCE(counselor_user_id,0), COALESCE(u.username,'') FROM classes c
			LEFT JOIN users u ON u.id=c.counselor_user_id WHERE c.id=?`, classID).Scan(&currentCounselor, &currentName)
		if err != nil {
			return err
		}
		if currentCounselor != 0 && currentCounselor != actor.ID {
			replacesID = currentCounselor
		}
		if _, err := tx.ExecContext(ctx, `UPDATE classes SET counselor_user_id=?, version=version+1, updated_at=? WHERE id=?`,
			actor.ID, now, classID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE class_counselor_requests SET status='accepted', replaces_user_id=?, responded_at=? WHERE id=?`,
			nullableID(replacesID), now, inviteID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE class_counselor_requests SET status='cancelled', responded_at=?
			WHERE class_id=? AND status='pending' AND id<>?`, now, classID, inviteID); err != nil {
			return err
		}
		if replacesID != 0 && replacesID != actor.ID {
			_ = s.notifyCounselorReplaced(ctx, tx, replacesID, className,
				fmt.Sprintf("辅导员 %s 接受邀请成为 %s 的辅导员，顶替了你的辅导员岗位", actor.Username, className), now)
		}
		_ = s.Notify(ctx, tx, invitedBy, "counselor_invite_result", "辅导员已接受邀请", actor.Username+" 已接受 "+className+" 的辅导员邀请。", "/t/classes/"+itoa(classID), 0)
		return nil
	})
}

// AssignTeacher lets an administrator set the class teacher.
func (s *Store) AssignTeacher(ctx context.Context, admin User, classID, teacherID int64, ip string) error {
	if admin.Role != "admin" {
		return ErrForbidden
	}
	now := s.NowUnix()
	return s.withTx(ctx, func(tx *sql.Tx) error {
		c, err := scanClass(tx.QueryRowContext(ctx, `SELECT `+classCols+classJoins+` WHERE c.id=?`, classID))
		if err != nil {
			return err
		}
		var t User
		t, err = scanUser(tx.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id=? AND role='teacher' AND status='active'`, teacherID))
		if err != nil {
			return errors.New("指定的班主任账号不存在或不可用")
		}
		var oldClassID int64
		var oldClassName string
		err = tx.QueryRowContext(ctx, `SELECT id,name FROM classes WHERE teacher_user_id=? AND id<>?`, teacherID, classID).Scan(&oldClassID, &oldClassName)
		if err == nil {
			var oldCounselor int64
			_ = tx.QueryRowContext(ctx, `SELECT COALESCE(counselor_user_id,0) FROM classes WHERE id=?`, oldClassID).Scan(&oldCounselor)
			if err := s.markUnclaimedTx(ctx, tx, oldClassID, teacherID, now); err != nil {
				return err
			}
			_ = s.Notify(ctx, tx, oldCounselor, "class_unclaimed", "班级进入待认领", oldClassName+" 暂无班主任，已开始 7 天清理倒计时。", "/t/classes/"+itoa(oldClassID), 0)
			_ = s.NotifyAdmins(ctx, tx, "class_unclaimed", "班级进入待认领", oldClassName+" 暂无班主任，将在 7 天后清理。", "/admin/classes")
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE classes SET teacher_user_id=?, unclaimed_at=NULL, delete_due_at=NULL,
			former_teacher_user_id=NULL, reminded_3d_at=NULL, reminded_1d_at=NULL, version=version+1, updated_at=? WHERE id=?`,
			teacherID, now, classID); err != nil {
			return err
		}
		if c.TeacherUserID != 0 && c.TeacherUserID != teacherID {
			_ = s.Notify(ctx, tx, c.TeacherUserID, "class_takeover", "你已被顶替", "管理员已将 "+c.Name+" 的班主任改为 "+t.Username+"。", "/t/classes", 0)
		}
		_ = s.Notify(ctx, tx, teacherID, "class_assigned", "你被指派为班主任", "管理员已指派你为 "+c.Name+" 的班主任。", "/t/classes/"+itoa(classID), 0)
		_ = s.Notify(ctx, tx, c.CounselorUserID, "class_claimed", "班级已有班主任", t.Username+" 已被指派为 "+c.Name+" 的班主任。", "/t/classes/"+itoa(classID), 0)
		_ = s.NotifyClassStudents(ctx, tx, classID, "class_claimed", "班级已恢复", c.Name+" 已有班主任，班级功能恢复正常。", "/s")
		return s.Audit(ctx, tx, admin, "class.assign_teacher", "class", classID,
			fmt.Sprintf("指派 %s 为班主任（原：%s）", t.Username, c.TeacherName), ip)
	})
}

// AssignCounselor lets an administrator set or replace the class counselor.
func (s *Store) AssignCounselor(ctx context.Context, admin User, classID, counselorID int64, ip string) error {
	if admin.Role != "admin" {
		return ErrForbidden
	}
	now := s.NowUnix()
	return s.withTx(ctx, func(tx *sql.Tx) error {
		c, err := scanClass(tx.QueryRowContext(ctx, `SELECT `+classCols+classJoins+` WHERE c.id=?`, classID))
		if err != nil {
			return err
		}
		var co User
		co, err = scanUser(tx.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id=? AND role='counselor' AND status='active'`, counselorID))
		if err != nil {
			return errors.New("指定的辅导员账号不存在或不可用")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE classes SET counselor_user_id=?, version=version+1, updated_at=? WHERE id=?`, counselorID, now, classID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE class_counselor_requests SET status='cancelled', responded_at=? WHERE class_id=? AND status='pending'`, now, classID); err != nil {
			return err
		}
		if c.CounselorUserID != 0 && c.CounselorUserID != counselorID {
			_ = s.notifyCounselorReplaced(ctx, tx, c.CounselorUserID, c.Name,
				fmt.Sprintf("管理员将 %s 的辅导员改为 %s，顶替了你的辅导员岗位", c.Name, co.Username), now)
		}
		_ = s.Notify(ctx, tx, counselorID, "class_assigned", "你被指派为辅导员", "管理员已指派你为 "+c.Name+" 的辅导员。", "/t/classes/"+itoa(classID), 0)
		_ = s.Notify(ctx, tx, c.TeacherUserID, "class_counselor", "班级辅导员已调整", co.Username+" 现为 "+c.Name+" 的辅导员。", "/t/classes/"+itoa(classID), 0)
		return s.Audit(ctx, tx, admin, "class.assign_counselor", "class", classID,
			fmt.Sprintf("指派 %s 为辅导员（原：%s）", co.Username, c.CounselorName), ip)
	})
}

// DisbandClass explicitly dissolves a class: students keep accounts and leave
// their class, pending leaves are cancelled.
func (s *Store) DisbandClass(ctx context.Context, admin User, classID int64, ip string) error {
	if admin.Role != "admin" {
		return ErrForbidden
	}
	now := s.NowUnix()
	return s.withTx(ctx, func(tx *sql.Tx) error {
		c, err := scanClass(tx.QueryRowContext(ctx, `SELECT `+classCols+classJoins+` WHERE c.id=?`, classID))
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE leave_requests SET status='cancelled', decision='class_disbanded',
			cancel_reason='班级已被管理员解散', decided_at=?, decided_via='system' WHERE class_id=? AND status='pending'`, now, classID)
		if err != nil {
			return err
		}
		cancelled, _ := res.RowsAffected()
		if _, err := tx.ExecContext(ctx, `UPDATE proxy_links SET revoked_at=? WHERE revoked_at IS NULL AND leave_id IN (SELECT id FROM leave_requests WHERE class_id=?)`, now, classID); err != nil {
			return err
		}
		// Approved QR codes stay valid until their leave ends, as required by
		// PLAN 4.5/3.5. Pending or already-finished leaves lose their token.
		if _, err := tx.ExecContext(ctx, `DELETE FROM qr_tokens WHERE leave_id IN
			(SELECT id FROM leave_requests WHERE class_id=? AND (status<>'approved' OR end_at<=?))`, classID, now); err != nil {
			return err
		}
		_ = s.Notify(ctx, tx, c.TeacherUserID, "class_disbanded", "班级已被解散", c.Name+" 已被管理员解散，学生转为暂无班级。", "/t/classes", 0)
		_ = s.Notify(ctx, tx, c.CounselorUserID, "class_disbanded", "班级已被解散", c.Name+" 已被管理员解散，学生转为暂无班级。", "/t/classes", 0)
		_ = s.NotifyClassStudents(ctx, tx, classID, "class_disbanded", "班级已被解散", c.Name+" 已被管理员解散，你当前暂无班级，请联系管理员转入新班。", "/s")
		if _, err := tx.ExecContext(ctx, `UPDATE student_import_batches SET class_id=NULL WHERE class_id=?`, classID); err != nil {
			return err
		}
		// Audit while the class row still exists so the target snapshot is
		// populated; both writes share this transaction.
		if err := s.Audit(ctx, tx, admin, "class.disband", "class", classID,
			fmt.Sprintf("解散班级 %s，取消待审批假条 %d 条，学生 %d 人转为暂无班级", c.Name, cancelled, c.StudentCount), ip); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM classes WHERE id=?`, classID); err != nil {
			return err
		}
		return nil
	})
}

// UpdateDorm changes a student's dorm with old-value CAS and writes the audit.
func (s *Store) UpdateDorm(ctx context.Context, actor User, studentID int64, oldDorm, dorm, reason, ip string) error {
	if actor.Role == "student" {
		return errors.New("学生不能自行修改宿舍")
	}
	var oldBuilding, oldRoom string
	if strings.TrimSpace(oldDorm) != "" {
		var err error
		oldBuilding, oldRoom, err = validate.SplitDorm(oldDorm)
		if err != nil {
			return errors.New("页面上的宿舍信息无效，请刷新后重试")
		}
	}
	building, room, err := validate.SplitDorm(dorm)
	if err != nil {
		return err
	}
	reason, err = validate.ValidateRejectReason(reason)
	if err != nil {
		return err
	}
	now := s.NowUnix()
	return s.withTx(ctx, func(tx *sql.Tx) error {
		st, err := scanStudent(tx.QueryRowContext(ctx, `SELECT u.id, u.username, COALESCE(u.student_no,''), u.status,
			COALESCE(sc.class_id,0), COALESCE(c.name,''), COALESCE(c.department,''),
			COALESCE(sc.dorm_building,''), COALESCE(sc.dorm_room,''),
			COALESCE(t.username,''), COALESCE(co.username,''), COALESCE(sc.imported_at,0)
			FROM users u LEFT JOIN student_classes sc ON sc.user_id=u.id
			LEFT JOIN classes c ON c.id=sc.class_id
			LEFT JOIN users t ON t.id=c.teacher_user_id
			LEFT JOIN users co ON co.id=c.counselor_user_id
			WHERE u.id=? AND u.role='student'`, studentID))
		if err != nil {
			return ErrNotFound
		}
		if actor.Role == "teacher" || actor.Role == "counselor" {
			c, err := s.GetClass(ctx, st.ClassID)
			if err != nil {
				return errors.New("学生当前没有班级")
			}
			if (actor.Role == "teacher" && c.TeacherUserID != actor.ID) ||
				(actor.Role == "counselor" && c.CounselorUserID != actor.ID) {
				return errors.New("只能修改本班学生的宿舍")
			}
		}
		if st.DormBuilding != oldBuilding || st.DormRoom != oldRoom {
			return ErrDormConflict
		}
		if st.DormBuilding == building && st.DormRoom == room {
			return errors.New("新宿舍与当前宿舍相同")
		}
		res, err := tx.ExecContext(ctx, `UPDATE student_classes SET dorm_building=?, dorm_room=?, dorm_updated_by=?, dorm_updated_at=?
			WHERE user_id=? AND dorm_building=? AND dorm_room=?`, building, room, actor.ID, now,
			studentID, oldBuilding, oldRoom)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return ErrDormConflict
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO student_dorm_edits
			(student_user_id,student_name_snapshot,student_no_snapshot,class_id,class_name_snapshot,old_building,old_room,
			 new_building,new_room,edited_by,editor_name_snapshot,editor_role,reason,created_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			studentID, st.Name, st.StudentNo, nullableID(st.ClassID), st.ClassName, st.DormBuilding, st.DormRoom,
			building, room, actor.ID, actor.Username, actor.Role, reason, now); err != nil {
			return err
		}
		_ = s.Notify(ctx, tx, studentID, "dorm_changed", "宿舍信息已修改", "你的宿舍已修改为 "+validate.DormString(building, room)+"。修改人："+actor.Username, "/s/profile", 0)
		var teacherID, counselorID int64
		if st.ClassID != 0 {
			_ = tx.QueryRowContext(ctx, `SELECT COALESCE(teacher_user_id,0), COALESCE(counselor_user_id,0) FROM classes WHERE id=?`, st.ClassID).Scan(&teacherID, &counselorID)
		}
		_ = s.Notify(ctx, tx, teacherID, "dorm_changed", "学生宿舍已修改", st.Name+"（"+st.StudentNo+"）宿舍改为 "+validate.DormString(building, room)+"，操作人："+actor.Username, "/t/classes/"+itoa(st.ClassID)+"/students", 0)
		_ = s.Notify(ctx, tx, counselorID, "dorm_changed", "学生宿舍已修改", st.Name+"（"+st.StudentNo+"）宿舍改为 "+validate.DormString(building, room)+"，操作人："+actor.Username, "/t/classes/"+itoa(st.ClassID)+"/students", 0)
		if actor.Role == "admin" {
			_ = s.Audit(ctx, tx, actor, "student.dorm", "student", studentID,
				"宿舍 "+st.Dorm()+" -> "+validate.DormString(building, room), ip)
		}
		return nil
	})
}

func scanStudent(r rowScanner) (StudentRow, error) {
	var st StudentRow
	err := r.Scan(&st.UserID, &st.Name, &st.StudentNo, &st.Status, &st.ClassID, &st.ClassName, &st.Department,
		&st.DormBuilding, &st.DormRoom, &st.TeacherName, &st.CounselorName, &st.ImportedAt)
	return st, err
}

// UpdateStudentName lets the class teacher/counselor correct a student's name.
func (s *Store) UpdateStudentName(ctx context.Context, actor User, studentID int64, name string) error {
	if actor.Role != "teacher" && actor.Role != "counselor" {
		return errors.New("只有本班班主任或辅导员可以修正学生姓名")
	}
	newName, err := validate.NormalizeName(name)
	if err != nil {
		return err
	}
	st, err := s.GetStudent(ctx, studentID)
	if err != nil {
		return ErrNotFound
	}
	if st.ClassID == 0 {
		return errors.New("学生当前没有班级")
	}
	c, err := s.GetClass(ctx, st.ClassID)
	if err != nil {
		return err
	}
	if (actor.Role == "teacher" && c.TeacherUserID != actor.ID) ||
		(actor.Role == "counselor" && c.CounselorUserID != actor.ID) {
		return errors.New("只能修改本班学生的姓名")
	}
	_, err = s.DB.ExecContext(ctx, `UPDATE users SET username=?, username_norm=?, updated_at=? WHERE id=?`,
		newName, strings.ToLower(newName), s.NowUnix(), studentID)
	return err
}

// CommunityBuildings lists a community teacher's chosen buildings.
func (s *Store) CommunityBuildings(ctx context.Context, userID int64) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT building FROM community_buildings WHERE user_id=? ORDER BY building`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var b string
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// SetCommunityBuildings replaces a community teacher's building selection.
func (s *Store) SetCommunityBuildings(ctx context.Context, userID int64, buildings []string) error {
	seen := map[string]bool{}
	var clean []string
	for _, raw := range buildings {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		b, err := validate.NormalizeBuilding(raw)
		if err != nil {
			return err
		}
		if !seen[b] {
			seen[b] = true
			clean = append(clean, b)
		}
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM community_buildings WHERE user_id=?`, userID); err != nil {
			return err
		}
		for _, b := range clean {
			if _, err := tx.ExecContext(ctx, `INSERT INTO community_buildings (user_id,building) VALUES (?,?)`, userID, b); err != nil {
				return err
			}
		}
		return nil
	})
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	neg := v < 0
	if neg {
		v = -v
	}
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
