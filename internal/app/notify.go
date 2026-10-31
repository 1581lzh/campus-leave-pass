package app

import (
	"context"
	"database/sql"
	"strings"
)

// Notify inserts one notification; userID<=0 is ignored.
func (s *Store) Notify(ctx context.Context, q queryer, userID int64, kind, title, body, link string, leaveID int64) error {
	if userID <= 0 {
		return nil
	}
	if q == nil {
		q = s.DB
	}
	var leave any
	if leaveID > 0 {
		leave = leaveID
	}
	_, err := q.ExecContext(ctx, `INSERT INTO notifications (user_id,kind,title,body,link,leave_id,created_at)
		VALUES (?,?,?,?,?,?,?)`, userID, kind, title, body, link, leave, s.NowUnix())
	return err
}

// NotifyAdmins sends a notification to every administrator.
func (s *Store) NotifyAdmins(ctx context.Context, q queryer, kind, title, body, link string) error {
	if q == nil {
		q = s.DB
	}
	rows, err := q.QueryContext(ctx, `SELECT id FROM users WHERE role='admin' AND status='active'`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.Notify(ctx, q, id, kind, title, body, link, 0); err != nil {
			return err
		}
	}
	return nil
}

// NotifyClassStudents notifies every student of a class.
func (s *Store) NotifyClassStudents(ctx context.Context, q queryer, classID int64, kind, title, body, link string) error {
	if q == nil {
		q = s.DB
	}
	rows, err := q.QueryContext(ctx, `SELECT user_id FROM student_classes WHERE class_id=?`, classID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.Notify(ctx, q, id, kind, title, body, link, 0); err != nil {
			return err
		}
	}
	return nil
}

// Audit writes one administrator action record.
func (s *Store) Audit(ctx context.Context, q queryer, actor User, action, targetType string, targetID int64, detail, ip string) error {
	if q == nil {
		q = s.DB
	}
	var tid any
	if targetID > 0 {
		tid = targetID
	}
	_, err := q.ExecContext(ctx, `INSERT INTO admin_audit_logs (actor_user_id,actor_name,action,target_type,target_id,detail,ip,created_at)
		VALUES (?,?,?,?,?,?,?,?)`, nullInt(actor.ID), actor.Username, action, targetType, tid, detail, ip, s.NowUnix())
	return err
}

func nullInt(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

// DisplayName renders 姓名（学号） for students and 用户名 for staff.
func DisplayName(name, studentNo string) string {
	if strings.TrimSpace(studentNo) == "" {
		return name
	}
	return name + "（" + studentNo + "）"
}

// UnreadNotificationCount counts unread notifications.
func (s *Store) UnreadNotificationCount(ctx context.Context, userID int64) int {
	var n int
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM notifications WHERE user_id=? AND read_at IS NULL`, userID).Scan(&n)
	return n
}

// ListNotifications returns the newest notifications.
func (s *Store) ListNotifications(ctx context.Context, userID int64, limit int) ([]Notification, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id,user_id,kind,title,body,link,COALESCE(leave_id,0),COALESCE(read_at,0),created_at
		FROM notifications WHERE user_id=? ORDER BY created_at DESC, id DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Notification
	for rows.Next() {
		var n Notification
		if err := rows.Scan(&n.ID, &n.UserID, &n.Kind, &n.Title, &n.Body, &n.Link, &n.LeaveID, &n.ReadAt, &n.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// MarkAllNotificationsRead marks all of a user's notifications read.
func (s *Store) MarkAllNotificationsRead(ctx context.Context, userID int64) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE notifications SET read_at=? WHERE user_id=? AND read_at IS NULL`, s.NowUnix(), userID)
	return err
}

// MarkNotificationRead marks a single notification owned by the user.
func (s *Store) MarkNotificationRead(ctx context.Context, userID, id int64) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE notifications SET read_at=? WHERE id=? AND user_id=? AND read_at IS NULL`,
		s.NowUnix(), id, userID)
	return err
}

// PopupAlreadyShown records a daily popup and reports whether it was already
// recorded for the CST day.
func (s *Store) PopupAlreadyShown(ctx context.Context, userID int64, key string) bool {
	day := s.TodayCST()
	res, err := s.DB.ExecContext(ctx, `INSERT OR IGNORE INTO popup_receipts (user_id,popup_key,day,shown_at) VALUES (?,?,?,?)`,
		userID, key, day, s.NowUnix())
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n == 0
}

// popupTx inserts a receipt inside a transaction and reports prior existence.
func popupTx(ctx context.Context, tx *sql.Tx, userID int64, key, day string, now int64) (bool, error) {
	res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO popup_receipts (user_id,popup_key,day,shown_at) VALUES (?,?,?,?)`,
		userID, key, day, now)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 0, err
}
