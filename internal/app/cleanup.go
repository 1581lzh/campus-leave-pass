package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// UnclaimedTick scans unclaimed classes: starts fallback countdowns, sends
// day-3/day-6 reminders, and cleans classes whose 7-day window elapsed.
func (s *Store) UnclaimedTick(ctx context.Context) error {
	now := s.NowUnix()
	rows, err := s.DB.QueryContext(ctx, `SELECT id, COALESCE(unclaimed_at,0), COALESCE(delete_due_at,0),
		COALESCE(reminded_3d_at,0), COALESCE(reminded_1d_at,0) FROM classes WHERE teacher_user_id IS NULL`)
	if err != nil {
		return err
	}
	type row struct{ id, unclaimed, due, r3, r1 int64 }
	var list []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.unclaimed, &r.due, &r.r3, &r.r1); err != nil {
			rows.Close()
			return err
		}
		list = append(list, r)
	}
	rows.Close()
	for _, r := range list {
		if r.due == 0 {
			if _, err := s.DB.ExecContext(ctx, `UPDATE classes SET unclaimed_at=?, delete_due_at=?, updated_at=?
				WHERE id=? AND teacher_user_id IS NULL AND delete_due_at IS NULL`, now, now+UnclaimedTTL, now, r.id); err != nil {
				return err
			}
			c, err := s.GetClass(ctx, r.id)
			if err == nil {
				s.notifyUnclaimed(ctx, c, "班级进入待认领", c.Name+" 暂无班主任，已开始 7 天清理倒计时。")
			}
			continue
		}
		if r.due <= now {
			if err := s.CleanupClass(ctx, r.id); err != nil {
				return err
			}
			continue
		}
		if r.unclaimed > 0 && now >= r.unclaimed+3*86400 && r.r3 == 0 {
			c, err := s.GetClass(ctx, r.id)
			if err == nil {
				s.notifyUnclaimed(ctx, c, "待认领班级提醒（第 3 天）",
					fmt.Sprintf("%s 已进入待认领第 3 天，将在 %s 清理班级和全部学生账号。", c.Name, cstDate(c.DeleteDueAt)))
			}
			_, _ = s.DB.ExecContext(ctx, `UPDATE classes SET reminded_3d_at=? WHERE id=? AND reminded_3d_at IS NULL`, now, r.id)
		}
		if r.due > 0 && now >= r.due-86400 && r.r1 == 0 {
			c, err := s.GetClass(ctx, r.id)
			if err == nil {
				body := fmt.Sprintf("%s 将在 24 小时内清理班级和全部学生账号，假条保留 40 天。", c.Name)
				s.notifyUnclaimed(ctx, c, "待认领班级提醒（最后 24 小时）", body)
				_ = s.NotifyClassStudents(ctx, nil, c.ID, "class_cleanup_warning", "班级即将清理",
					"本班将在 24 小时内被清理，学生账号会被删除；假条将保留 40 天供教职工查看。", "/s")
			}
			_, _ = s.DB.ExecContext(ctx, `UPDATE classes SET reminded_1d_at=? WHERE id=? AND reminded_1d_at IS NULL`, now, r.id)
		}
	}
	return nil
}

func (s *Store) notifyUnclaimed(ctx context.Context, c Class, title, body string) {
	link := "/t/classes/" + itoa(c.ID)
	_ = s.Notify(ctx, nil, c.CounselorUserID, "class_unclaimed", title, body, link, 0)
	_ = s.Notify(ctx, nil, c.FormerTeacherID, "class_unclaimed", title, body, link, 0)
	_ = s.NotifyAdmins(ctx, nil, "class_unclaimed", title, body, "/admin/classes")
}

func cstDate(ts int64) string {
	return time.Unix(ts, 0).In(time.FixedZone("CST", 8*3600)).Format("2006-01-02")
}

// CleanupClass archives a class and deletes its students, keeping leaves for
// 40 days. It re-checks the precondition inside BEGIN IMMEDIATE.
func (s *Store) CleanupClass(ctx context.Context, classID int64) error {
	now := s.NowUnix()
	return s.withTx(ctx, func(tx *sql.Tx) error {
		c, err := scanClass(tx.QueryRowContext(ctx, `SELECT `+classCols+classJoins+` WHERE c.id=?`, classID))
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return nil
			}
			return err
		}
		if c.TeacherUserID != 0 || c.DeleteDueAt == 0 || c.DeleteDueAt > now {
			return nil
		}
		var studentCount, leaveCount int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM student_classes WHERE class_id=?`, classID).Scan(&studentCount); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM leave_requests WHERE class_id=?`, classID).Scan(&leaveCount); err != nil {
			return err
		}
		lastTeacherID, lastTeacherName := c.TeacherUserID, c.TeacherName
		if lastTeacherID == 0 {
			lastTeacherID, lastTeacherName = c.FormerTeacherID, c.FormerTeacherName
		}
		if lastTeacherID != 0 && lastTeacherName == "" {
			_ = tx.QueryRowContext(ctx, `SELECT username FROM users WHERE id=?`, lastTeacherID).Scan(&lastTeacherName)
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO archived_classes
			(source_class_id,class_name,department,last_teacher_user_id,last_teacher_name,last_counselor_user_id,last_counselor_name,
			 cleaned_at,retain_until,student_count,leave_count,reason,created_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?, 'unclaimed_7d', ?)`,
			classID, c.Name, c.Department, nullableID(lastTeacherID), lastTeacherName,
			nullableID(c.CounselorUserID), c.CounselorName, now, now+ArchiveRetainTTL,
			studentCount, leaveCount, now)
		if err != nil {
			return err
		}
		archiveID, _ := res.LastInsertId()
		if _, err := tx.ExecContext(ctx, `UPDATE leave_requests SET archived_class_id=?, archived_at=?, retain_until=?,
			status=CASE WHEN status='pending' THEN 'cancelled' ELSE status END,
			decision=CASE WHEN status='pending' THEN 'class_cleaned' ELSE decision END,
			cancel_reason=CASE WHEN status='pending' THEN '班级满 7 天未认领已清理' ELSE cancel_reason END,
			decided_at=CASE WHEN status='pending' THEN ? ELSE decided_at END,
			decided_via=CASE WHEN status='pending' THEN 'system' ELSE decided_via END
			WHERE class_id=?`, archiveID, now, now+ArchiveRetainTTL, now, classID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM proxy_links WHERE leave_id IN (SELECT id FROM leave_requests WHERE class_id=?)`, classID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM qr_tokens WHERE leave_id IN (SELECT id FROM leave_requests WHERE class_id=?)`, classID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE student_import_batches SET class_id=NULL WHERE class_id=?`, classID); err != nil {
			return err
		}
		// Students cascade to sessions, notifications, reset requests,
		// student_classes; leaves keep snapshots via ON DELETE SET NULL.
		if _, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id IN (SELECT user_id FROM student_classes WHERE class_id=?)`, classID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE leave_requests SET class_id=NULL WHERE class_id=?`, classID); err != nil {
			return err
		}
		// Audit before the class row disappears so the snapshot lookup sees it.
		if err := s.Audit(ctx, tx, User{Username: "系统"}, "class.cleanup", "class", classID,
			fmt.Sprintf("待认领满 7 天清理班级 %s：学生 %d 人，假条 %d 条，保留至 %s", c.Name, studentCount, leaveCount, cstDate(now+ArchiveRetainTTL)), ""); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM classes WHERE id=?`, classID); err != nil {
			return err
		}
		return s.NotifyAdmins(ctx, tx, "class_cleaned", "班级已清理",
			fmt.Sprintf("%s 满 7 天无人认领，已删除班级和 %d 名学生账号；%d 条假条保留 40 天。", c.Name, studentCount, leaveCount),
			"/archived/leaves")
	})
}

// RunTTL purges expired rows in bounded batches.
func (s *Store) RunTTL(ctx context.Context) int {
	now := s.NowUnix()
	n := 0
	exec := func(q string, args ...any) {
		if res, err := s.DB.ExecContext(ctx, q, args...); err == nil {
			if c, _ := res.RowsAffected(); c > 0 {
				n += int(c)
			}
		}
	}
	exec(`DELETE FROM leave_requests WHERE retain_until IS NOT NULL AND retain_until<=?`, now)
	exec(`DELETE FROM archived_classes WHERE retain_until<=? AND id NOT IN (SELECT archived_class_id FROM leave_requests WHERE archived_class_id IS NOT NULL)`, now)
	exec(`DELETE FROM sessions WHERE expires_at<=?`, now)
	exec(`DELETE FROM login_attempts WHERE updated_at<?`, now-7*86400)
	exec(`DELETE FROM password_reset_tokens WHERE expires_at<=? OR (used_at IS NOT NULL AND used_at<?)`, now, now-86400)
	exec(`DELETE FROM password_reset_requests WHERE status IN ('expired','rejected','used') AND requested_at<?`, now-30*86400)
	exec(`DELETE FROM password_change_events WHERE changed_at<?`, now-30*86400)
	exec(`DELETE FROM password_reset_attempts WHERE updated_at<?`, now-30*86400)
	exec(`DELETE FROM registration_attempts WHERE created_at<?`, now-90*86400)
	exec(`DELETE FROM notifications WHERE created_at<?`, now-30*86400)
	exec(`DELETE FROM popup_receipts WHERE day<?`, time.Unix(now-30*86400, 0).In(time.FixedZone("CST", 8*3600)).Format("2006-01-02"))
	if purged, err := s.PurgeExpiredUsers(ctx); err == nil {
		n += purged
	}
	return n
}

// CheckpointWAL keeps the write-ahead log small.
func (s *Store) CheckpointWAL(ctx context.Context) {
	_, _ = s.DB.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
}
