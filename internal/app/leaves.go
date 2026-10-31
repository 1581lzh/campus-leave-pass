package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"leavepass/internal/secure"
	"leavepass/internal/validate"
)

const leaveCols = `id, COALESCE(student_user_id,0), student_name, student_no, contact_phone,
	COALESCE(class_id,0), COALESCE(archived_class_id,0), class_name, department,
	COALESCE(teacher_user_id,0), teacher_name, COALESCE(counselor_user_id,0), counselor_name,
	dorm_building, dorm_room, reason, start_at, end_at, status, decision,
	COALESCE(decided_by,0), decided_via, approver_name, major_opinion, dept_opinion,
	reject_reason, cancel_reason, deadline_at, COALESCE(decided_at,0), created_at,
	COALESCE(archived_at,0), COALESCE(retain_until,0), COALESCE(created_by,0)`

func scanLeave(r rowScanner) (Leave, error) {
	var l Leave
	err := r.Scan(&l.ID, &l.StudentUserID, &l.StudentName, &l.StudentNo, &l.ContactPhone,
		&l.ClassID, &l.ArchivedClassID, &l.ClassName, &l.Department,
		&l.TeacherUserID, &l.TeacherName, &l.CounselorUserID, &l.CounselorName,
		&l.DormBuilding, &l.DormRoom, &l.Reason, &l.StartAt, &l.EndAt, &l.Status, &l.Decision,
		&l.DecidedBy, &l.DecidedVia, &l.ApproverName, &l.MajorOpinion, &l.DeptOpinion,
		&l.RejectReason, &l.CancelReason, &l.DeadlineAt, &l.DecidedAt, &l.CreatedAt,
		&l.ArchivedAt, &l.RetainUntil, &l.CreatedBy)
	return l, err
}

// GetLeave loads one leave request.
func (s *Store) GetLeave(ctx context.Context, id int64) (Leave, error) {
	l, err := scanLeave(s.DB.QueryRowContext(ctx, `SELECT `+leaveCols+` FROM leave_requests WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return l, ErrNotFound
	}
	return l, err
}

// SubmitLeaveInput is the student's form.
type SubmitLeaveInput struct {
	ContactPhone string
	Reason       string
	StartAt      int64
	EndAt        int64
}

// SubmitLeave creates a pending leave request with a full snapshot.
func (s *Store) SubmitLeave(ctx context.Context, student User, in SubmitLeaveInput) (int64, error) {
	if student.Role != "student" || student.Status != "active" {
		return 0, errors.New("账号已停用，仅可查看历史；不能新建请假")
	}
	st, err := s.GetStudent(ctx, student.ID)
	if err != nil {
		return 0, ErrNotFound
	}
	if st.ClassID == 0 {
		return 0, errors.New("无法提交请假：你当前没有班级，请联系管理员转入班级")
	}
	c, err := s.GetClass(ctx, st.ClassID)
	if err != nil {
		return 0, errors.New("无法提交请假：班级不存在，请联系管理员")
	}
	if c.TeacherUserID == 0 && c.CounselorUserID == 0 {
		return 0, errors.New("无法提交请假：本班暂无班主任和辅导员，没有审批人；请联系管理员指派")
	}
	return s.insertLeave(ctx, student, st, c, in, student.ID, false)
}

// SubmitLeaveForStudent lets the current class teacher/counselor file a leave
// on behalf of a student in their class. The submitter is recorded so the UI
// can show that the request was filed by staff; the normal approval matrix
// still applies, which means the same teacher may approve it.
func (s *Store) SubmitLeaveForStudent(ctx context.Context, actor User, studentID int64, in SubmitLeaveInput) (int64, error) {
	if actor.Status != "active" || (actor.Role != "teacher" && actor.Role != "counselor") {
		return 0, errors.New("只有本班班主任或辅导员可以代学生提交请假")
	}
	st, err := s.GetStudent(ctx, studentID)
	if err != nil || st.Status != "active" {
		return 0, ErrNotFound
	}
	if st.ClassID == 0 {
		return 0, errors.New("无法代提交：该学生当前没有班级，请先由管理员转入班级")
	}
	c, err := s.GetClass(ctx, st.ClassID)
	if err != nil {
		return 0, errors.New("无法代提交：班级不存在或已解散")
	}
	if err := s.CanManageClass(ctx, actor, c.ID); err != nil {
		return 0, errors.New("无法代提交：只有该生当前班级的班主任或辅导员可以操作")
	}
	if c.TeacherUserID == 0 && c.CounselorUserID == 0 {
		return 0, errors.New("无法代提交：本班暂无班主任和辅导员，没有审批人")
	}
	return s.insertLeave(ctx, actor, st, c, in, actor.ID, true)
}

func (s *Store) insertLeave(ctx context.Context, submitter User, st StudentRow, c Class, in SubmitLeaveInput, createdBy int64, proxy bool) (int64, error) {
	contact, err := validate.NormalizeContactPhone(in.ContactPhone)
	if err != nil {
		return 0, err
	}
	reason, err := validate.ValidateReason(in.Reason)
	if err != nil {
		return 0, err
	}
	now := s.NowUnix()
	if in.StartAt < now-LeaveBackdateGrace {
		return 0, errors.New("开始时间不能早于当前时间 5 分钟以上")
	}
	if in.EndAt <= in.StartAt {
		return 0, errors.New("结束时间必须晚于开始时间")
	}
	if in.EndAt-in.StartAt > LeaveMaxDuration {
		return 0, errors.New("单次请假最长 30 天")
	}
	var id int64
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		var overlap int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM leave_requests
			WHERE student_user_id=? AND status IN ('pending','approved') AND start_at<? AND end_at>?`,
			st.UserID, in.EndAt, in.StartAt).Scan(&overlap); err != nil {
			return err
		}
		if overlap > 0 {
			return errors.New("该学生已有时间重叠的待审批或已批准请假")
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO leave_requests
			(student_user_id,student_name,student_no,contact_phone,class_id,class_name,department,
			 teacher_user_id,teacher_name,counselor_user_id,counselor_name,dorm_building,dorm_room,
			 reason,start_at,end_at,status,deadline_at,created_at,created_by)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?, 'pending', ?,?,?)`,
			st.UserID, st.Name, st.StudentNo, contact, st.ClassID, c.Name, c.Department,
			nullableID(c.TeacherUserID), c.TeacherName, nullableID(c.CounselorUserID), c.CounselorName,
			st.DormBuilding, st.DormRoom, reason, in.StartAt, in.EndAt, now+LeaveDeadlineTTL, now, nullableID(createdBy))
		if err != nil {
			return err
		}
		id, _ = res.LastInsertId()
		body := st.Name + "（" + st.StudentNo + "）提交了新的请假申请，请在 24 小时内处理。"
		if proxy {
			body = submitter.Username + " 已代 " + st.Name + "（" + st.StudentNo + "）提交请假申请，请在 24 小时内处理。"
			if err := s.Notify(ctx, tx, st.UserID, "leave_new", "老师已代你提交请假", submitter.Username+" 已代你提交请假申请，等待审批。", "/s/leaves/"+itoa(id), id); err != nil {
				return err
			}
		}
		if c.TeacherUserID != 0 && (!proxy || c.TeacherUserID != submitter.ID) {
			if err := s.Notify(ctx, tx, c.TeacherUserID, "leave_new", "新的请假申请", body, "/t/leaves/"+itoa(id), id); err != nil {
				return err
			}
		}
		if c.CounselorUserID != 0 && (!proxy || c.CounselorUserID != submitter.ID) {
			if err := s.Notify(ctx, tx, c.CounselorUserID, "leave_new", "新的请假申请", body, "/t/leaves/"+itoa(id), id); err != nil {
				return err
			}
		}
		return nil
	})
	return id, err
}

// CanApprove reports whether the actor may decide this leave.
func (s *Store) CanApprove(ctx context.Context, actor User, l Leave) bool {
	if actor.Role != "teacher" && actor.Role != "counselor" {
		return false
	}
	if l.TeacherUserID == actor.ID || l.CounselorUserID == actor.ID {
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
}

// DecideInput carries the approval form.
type DecideInput struct {
	Decision     string // approved or rejected
	ApproverName string
	MajorOpinion string
	DeptOpinion  string
	RejectReason string
	IP           string
}

// DecideLeave applies a web approval or rejection, first write wins.
func (s *Store) DecideLeave(ctx context.Context, actor User, leaveID int64, in DecideInput) error {
	if in.Decision != "approved" && in.Decision != "rejected" {
		return errors.New("无效的审批操作")
	}
	l, err := s.GetLeave(ctx, leaveID)
	if err != nil {
		return ErrNotFound
	}
	if !s.CanApprove(ctx, actor, l) {
		return errors.New("无法审批：你不是该假条所属班级的当前或提交时班主任/辅导员")
	}
	name := strings.TrimSpace(in.ApproverName)
	if name == "" {
		return errors.New("审批人姓名必填")
	}
	if len([]rune(name)) > 20 {
		return errors.New("审批人姓名不能超过 20 个字符")
	}
	major, err := validate.ValidateOpinion(in.MajorOpinion)
	if err != nil {
		return err
	}
	dept, err := validate.ValidateOpinion(in.DeptOpinion)
	if err != nil {
		return err
	}
	reject, err := validate.ValidateRejectReason(in.RejectReason)
	if err != nil {
		return err
	}
	now := s.NowUnix()
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		var status string
		var deadline int64
		if err := tx.QueryRowContext(ctx, `SELECT status, deadline_at FROM leave_requests WHERE id=?`, leaveID).Scan(&status, &deadline); err != nil {
			return ErrNotFound
		}
		if status != "pending" || deadline <= now {
			return errors.New("该假条已被处理或已超时，请刷新后重试")
		}
		res, err := tx.ExecContext(ctx, `UPDATE leave_requests SET status=?, decision=?, decided_by=?, decided_via='web',
			approver_name=?, major_opinion=?, dept_opinion=?, reject_reason=?, decided_at=?
			WHERE id=? AND status='pending' AND deadline_at>?`,
			in.Decision, in.Decision, actor.ID, name, major, dept, reject, now, leaveID, now)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return errors.New("该假条已被处理或已超时，请刷新后重试")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE proxy_links SET revoked_at=? WHERE leave_id=? AND revoked_at IS NULL`, now, leaveID); err != nil {
			return err
		}
		if in.Decision == "approved" {
			if err := s.createQRTx(ctx, tx, leaveID, now); err != nil {
				return err
			}
		}
		var notifyID int64
		switch {
		case actor.ID == l.TeacherUserID:
			notifyID = l.CounselorUserID
		case actor.ID == l.CounselorUserID:
			notifyID = l.TeacherUserID
		default:
			if actor.Role == "teacher" {
				notifyID = l.CounselorUserID
			} else {
				notifyID = l.TeacherUserID
			}
		}
		title, body := "请假已通过", "你的请假申请已通过。"
		if in.Decision == "rejected" {
			title, body = "请假已驳回", "你的请假申请已被驳回。"
			if reject != "" {
				body += "原因：" + reject
			}
		}
		if err := s.Notify(ctx, tx, l.StudentUserID, "leave_decided", title, body, "/s/leaves/"+itoa(leaveID), leaveID); err != nil {
			return err
		}
		return s.Notify(ctx, tx, notifyID, "leave_decided", "请假已处理", l.StudentName+"（"+l.StudentNo+"）的请假已由 "+actor.Username+" 处理。", "/t/leaves/"+itoa(leaveID), leaveID)
	})
	return err
}

// createQRTx creates the deterministic QR token row for an approved leave.
func (s *Store) createQRTx(ctx context.Context, tx *sql.Tx, leaveID, now int64) error {
	raw := s.QRToken(leaveID)
	_, err := tx.ExecContext(ctx, `INSERT INTO qr_tokens (leave_id,token_hash,created_at,revoked_at)
		VALUES (?,?,?,NULL) ON CONFLICT(leave_id) DO UPDATE SET token_hash=excluded.token_hash, created_at=excluded.created_at, revoked_at=NULL`,
		leaveID, secure.SHA256Hex(raw), now)
	return err
}

// QRToken derives the stable QR capability for a leave. Only its SHA-256 is
// stored; the derivation keeps the QR re-renderable for the student.
func (s *Store) QRToken(leaveID int64) string {
	return secure.HMACHex(s.SessionSecret(), "qr:"+itoa(leaveID))
}

// EditLeaveTime changes start/end of a pending leave with CAS.
func (s *Store) EditLeaveTime(ctx context.Context, actor User, leaveID, start, end int64) error {
	l, err := s.GetLeave(ctx, leaveID)
	if err != nil {
		return ErrNotFound
	}
	if !s.CanApprove(ctx, actor, l) {
		return errors.New("无法修改时间：你不是该假条所属班级的当前或提交时班主任/辅导员")
	}
	if l.Status != "pending" {
		return errors.New("只能修改待审批请假的时间")
	}
	now := s.NowUnix()
	if l.DeadlineAt <= now {
		return errors.New("该假条已超过 24 小时审批期限")
	}
	if end <= start {
		return errors.New("结束时间必须晚于开始时间")
	}
	if end-start > LeaveMaxDuration {
		return errors.New("单次请假最长 30 天")
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var overlap int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM leave_requests WHERE student_user_id=? AND id<>?
			AND status IN ('pending','approved') AND start_at<? AND end_at>?`, l.StudentUserID, leaveID, end, start).Scan(&overlap); err != nil {
			return err
		}
		if overlap > 0 {
			return errors.New("修改后的时间与该学生的其他请假重叠")
		}
		res, err := tx.ExecContext(ctx, `UPDATE leave_requests SET start_at=?, end_at=? WHERE id=? AND status='pending' AND start_at=? AND end_at=?`,
			start, end, leaveID, l.StartAt, l.EndAt)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return errors.New("请假时间已被他人修改，请刷新后重试")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO leave_time_edits (leave_id,editor_user_id,editor_name,old_start,old_end,new_start,new_end,created_at)
			VALUES (?,?,?,?,?,?,?,?)`, leaveID, actor.ID, actor.Username, l.StartAt, l.EndAt, start, end, now); err != nil {
			return err
		}
		body := fmt.Sprintf("你的请假时间已由 %s 调整为 %s 至 %s。", actor.Username,
			formatCST(start), formatCST(end))
		return s.Notify(ctx, tx, l.StudentUserID, "leave_time_edited", "请假时间已修改", body, "/s/leaves/"+itoa(leaveID), leaveID)
	})
}

func formatCST(ts int64) string {
	return time.Unix(ts, 0).In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04")
}

// CancelLeaveByStudent cancels a pending leave without notifications.
func (s *Store) CancelLeaveByStudent(ctx context.Context, student User, leaveID int64) error {
	now := s.NowUnix()
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var owner int64
		var status string
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(student_user_id,0), status FROM leave_requests WHERE id=?`, leaveID).Scan(&owner, &status); err != nil {
			return ErrNotFound
		}
		if owner != student.ID {
			return errors.New("无法操作：只能查看/取消自己的请假记录")
		}
		if status != "pending" {
			return errors.New("已批准或已处理的请假不能取消")
		}
		res, err := tx.ExecContext(ctx, `UPDATE leave_requests SET status='cancelled', decision='cancelled_by_student', decided_at=?
			WHERE id=? AND status='pending'`, now, leaveID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return errors.New("该假条已被处理，请刷新后重试")
		}
		_, err = tx.ExecContext(ctx, `UPDATE proxy_links SET revoked_at=? WHERE leave_id=? AND revoked_at IS NULL`, now, leaveID)
		return err
	})
}

// CancelLeaveByAdmin cancels a pending leave and notifies the student.
func (s *Store) CancelLeaveByAdmin(ctx context.Context, admin User, leaveID int64, reason, ip string) error {
	if admin.Role != "admin" {
		return ErrForbidden
	}
	var err error
	reason, err = validate.ValidateRejectReason(reason)
	if err != nil {
		return err
	}
	now := s.NowUnix()
	return s.withTx(ctx, func(tx *sql.Tx) error {
		l, err := scanLeave(tx.QueryRowContext(ctx, `SELECT `+leaveCols+` FROM leave_requests WHERE id=?`, leaveID))
		if err != nil {
			return ErrNotFound
		}
		if l.Status != "pending" {
			return errors.New("只能取消待审批的请假")
		}
		res, err := tx.ExecContext(ctx, `UPDATE leave_requests SET status='cancelled', decision='cancelled_by_admin',
			cancel_reason=?, decided_by=?, decided_via='web', decided_at=? WHERE id=? AND status='pending'`,
			reason, admin.ID, now, leaveID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return errors.New("该假条已被处理，请刷新后重试")
		}
		_, _ = tx.ExecContext(ctx, `UPDATE proxy_links SET revoked_at=? WHERE leave_id=? AND revoked_at IS NULL`, now, leaveID)
		body := "管理员已取消你的请假申请。"
		if reason != "" {
			body += "原因：" + reason
		}
		if err := s.Notify(ctx, tx, l.StudentUserID, "leave_cancelled", "请假已被管理员取消", body, "/s/leaves/"+itoa(leaveID), leaveID); err != nil {
			return err
		}
		_ = s.Notify(ctx, tx, l.TeacherUserID, "leave_cancelled", "请假已被管理员取消", l.StudentName+"（"+l.StudentNo+"）的请假已由管理员取消。", "/t/leaves/"+itoa(leaveID), leaveID)
		_ = s.Notify(ctx, tx, l.CounselorUserID, "leave_cancelled", "请假已被管理员取消", l.StudentName+"（"+l.StudentNo+"）的请假已由管理员取消。", "/t/leaves/"+itoa(leaveID), leaveID)
		return nil
	})
}

// DeleteLeaveByAdmin physically deletes a leave and its dependants.
func (s *Store) DeleteLeaveByAdmin(ctx context.Context, admin User, leaveID int64) error {
	if admin.Role != "admin" {
		return ErrForbidden
	}
	res, err := s.DB.ExecContext(ctx, `DELETE FROM leave_requests WHERE id=?`, leaveID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// TimeoutLeaves rejects pending leaves past their deadline.
func (s *Store) TimeoutLeaves(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 100
	}
	now := s.NowUnix()
	rows, err := s.DB.QueryContext(ctx, `SELECT id FROM leave_requests WHERE status='pending' AND deadline_at<=? ORDER BY id LIMIT ?`, now, limit)
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
	count := 0
	for _, id := range ids {
		err := s.withTx(ctx, func(tx *sql.Tx) error {
			l, err := scanLeave(tx.QueryRowContext(ctx, `SELECT `+leaveCols+` FROM leave_requests WHERE id=?`, id))
			if err != nil {
				return err
			}
			res, err := tx.ExecContext(ctx, `UPDATE leave_requests SET status='rejected', decision='timeout',
				reject_reason='超过 24 小时未审批，系统自动驳回', decided_via='system', decided_at=?
				WHERE id=? AND status='pending' AND deadline_at<=?`, now, id, now)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return nil
			}
			_, _ = tx.ExecContext(ctx, `UPDATE proxy_links SET revoked_at=? WHERE leave_id=? AND revoked_at IS NULL`, now, id)
			body := "你的请假申请超过 24 小时未审批，已自动驳回。"
			_ = s.Notify(ctx, tx, l.StudentUserID, "leave_timeout", "请假已超时驳回", body, "/s/leaves/"+itoa(id), id)
			_ = s.Notify(ctx, tx, l.TeacherUserID, "leave_timeout", "请假已超时驳回", l.StudentName+"（"+l.StudentNo+"）的请假超过 24 小时未审批，已自动驳回。", "/t/leaves/"+itoa(id), id)
			_ = s.Notify(ctx, tx, l.CounselorUserID, "leave_timeout", "请假已超时驳回", l.StudentName+"（"+l.StudentNo+"）的请假超过 24 小时未审批，已自动驳回。", "/t/leaves/"+itoa(id), id)
			return nil
		})
		if err == nil {
			count++
		}
	}
	return count, nil
}

// GenerateProxyLink creates a new single active proxy link and returns the raw
// token (only displayable once).
func (s *Store) GenerateProxyLink(ctx context.Context, actor User, leaveID int64, showPhone bool) (string, error) {
	l, err := s.GetLeave(ctx, leaveID)
	if err != nil {
		return "", ErrNotFound
	}
	allowed := s.CanApprove(ctx, actor, l) || actor.Role == "admin"
	if !allowed {
		return "", errors.New("无权生成代理链接：只有本班班主任/辅导员或管理员可以生成")
	}
	now := s.NowUnix()
	if l.Status != "pending" || l.DeadlineAt <= now {
		return "", errors.New("只能为审批中的请假生成代理链接")
	}
	raw, err := secure.RandomToken(32)
	if err != nil {
		return "", err
	}
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE proxy_links SET revoked_at=? WHERE leave_id=? AND revoked_at IS NULL`, now, leaveID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO proxy_links (leave_id,token_hash,created_by,show_contact_phone,expires_at,created_at)
			VALUES (?,?,?,?,?,?)`, leaveID, secure.SHA256Hex(raw), actor.ID, boolInt(showPhone), l.DeadlineAt, now)
		return err
	})
	return raw, err
}

// ProxyView is the public proxy approval page data.
type ProxyView struct {
	Leave     Leave
	ShowPhone bool
	LinkID    int64
	ExpiresAt int64
}

// GetProxyView resolves a raw proxy token.
func (s *Store) GetProxyView(ctx context.Context, token string) (ProxyView, error) {
	var v ProxyView
	var leaveID int64
	var revoked sql.NullInt64
	err := s.DB.QueryRowContext(ctx, `SELECT id, leave_id, show_contact_phone, expires_at, revoked_at
		FROM proxy_links WHERE token_hash=?`, secure.SHA256Hex(token)).
		Scan(&v.LinkID, &leaveID, &v.ShowPhone, &v.ExpiresAt, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	if err != nil {
		return v, err
	}
	if revoked.Valid || v.ExpiresAt <= s.NowUnix() {
		return v, ErrNotFound
	}
	l, err := s.GetLeave(ctx, leaveID)
	if err != nil {
		return v, ErrNotFound
	}
	if l.Status != "pending" || l.DeadlineAt <= s.NowUnix() {
		return v, ErrNotFound
	}
	v.Leave = l
	return v, nil
}

// ProxyDecide applies an anonymous proxy approval and revokes the link.
func (s *Store) ProxyDecide(ctx context.Context, token string, in DecideInput) error {
	if in.Decision != "approved" && in.Decision != "rejected" {
		return errors.New("无效的审批操作")
	}
	name := strings.TrimSpace(in.ApproverName)
	if name == "" {
		return errors.New("审批人姓名必填")
	}
	if len([]rune(name)) > 20 {
		return errors.New("审批人姓名不能超过 20 个字符")
	}
	major, err := validate.ValidateOpinion(in.MajorOpinion)
	if err != nil {
		return err
	}
	dept, err := validate.ValidateOpinion(in.DeptOpinion)
	if err != nil {
		return err
	}
	reject, err := validate.ValidateRejectReason(in.RejectReason)
	if err != nil {
		return err
	}
	now := s.NowUnix()
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var leaveID int64
		var revoked sql.NullInt64
		var expires int64
		var createdBy int64
		err := tx.QueryRowContext(ctx, `SELECT leave_id, revoked_at, expires_at, COALESCE(created_by,0)
			FROM proxy_links WHERE token_hash=?`, secure.SHA256Hex(token)).Scan(&leaveID, &revoked, &expires, &createdBy)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if revoked.Valid || expires <= now {
			return ErrNotFound
		}
		l, err := scanLeave(tx.QueryRowContext(ctx, `SELECT `+leaveCols+` FROM leave_requests WHERE id=?`, leaveID))
		if err != nil {
			return ErrNotFound
		}
		if l.Status != "pending" || l.DeadlineAt <= now {
			return errors.New("该假条已被处理或已超时")
		}
		res, err := tx.ExecContext(ctx, `UPDATE leave_requests SET status=?, decision=?, decided_via='proxy',
			approver_name=?, major_opinion=?, dept_opinion=?, reject_reason=?, decided_at=?
			WHERE id=? AND status='pending' AND deadline_at>?`,
			in.Decision, in.Decision, name, major, dept, reject, now, leaveID, now)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return errors.New("该假条已被处理或已超时")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE proxy_links SET revoked_at=? WHERE leave_id=? AND revoked_at IS NULL`, now, leaveID); err != nil {
			return err
		}
		if in.Decision == "approved" {
			if err := s.createQRTx(ctx, tx, leaveID, now); err != nil {
				return err
			}
		}
		title, body := "请假已通过（代理审批）", "你的请假申请已由代理审批人通过。"
		if in.Decision == "rejected" {
			title, body = "请假已驳回（代理审批）", "你的请假申请已由代理审批人驳回。"
			if reject != "" {
				body += "原因：" + reject
			}
		}
		_ = s.Notify(ctx, tx, l.StudentUserID, "leave_decided", title, body, "/s/leaves/"+itoa(leaveID), leaveID)
		_ = s.Notify(ctx, tx, l.TeacherUserID, "leave_decided", "请假已代理处理", l.StudentName+"（"+l.StudentNo+"）的请假已由代理审批人处理。", "/t/leaves/"+itoa(leaveID), leaveID)
		_ = s.Notify(ctx, tx, l.CounselorUserID, "leave_decided", "请假已代理处理", l.StudentName+"（"+l.StudentNo+"）的请假已由代理审批人处理。", "/t/leaves/"+itoa(leaveID), leaveID)
		return nil
	})
}

// GatePass is the public gate page data.
type GatePass struct {
	Leave      Leave
	State      string // not_started / valid / expired
	StateLabel string
}

// GetGatePass resolves a QR token without logging it.
func (s *Store) GetGatePass(ctx context.Context, token string) (GatePass, error) {
	var leaveID int64
	err := s.DB.QueryRowContext(ctx, `SELECT leave_id FROM qr_tokens WHERE token_hash=? AND revoked_at IS NULL`,
		secure.SHA256Hex(token)).Scan(&leaveID)
	if errors.Is(err, sql.ErrNoRows) {
		return GatePass{}, ErrNotFound
	}
	if err != nil {
		return GatePass{}, err
	}
	l, err := s.GetLeave(ctx, leaveID)
	if err != nil || l.Status != "approved" {
		return GatePass{}, ErrNotFound
	}
	now := s.NowUnix()
	g := GatePass{Leave: l}
	switch {
	case now < l.StartAt:
		g.State, g.StateLabel = "not_started", "未开始"
	case now > l.EndAt:
		g.State, g.StateLabel = "expired", "已过期"
	default:
		g.State, g.StateLabel = "valid", "有效"
	}
	return g, nil
}

// StudentLeaves lists a student's own leave history.
func (s *Store) StudentLeaves(ctx context.Context, studentID int64) ([]Leave, error) {
	return s.queryLeaves(ctx, `SELECT `+leaveCols+` FROM leave_requests WHERE student_user_id=? AND archived_at IS NULL ORDER BY created_at DESC, id DESC`, studentID)
}

// ClassLeaves lists a class's leaves, optionally by status.
func (s *Store) ClassLeaves(ctx context.Context, classID int64, status string) ([]Leave, error) {
	q := `SELECT ` + leaveCols + ` FROM leave_requests WHERE class_id=? AND archived_at IS NULL`
	args := []any{classID}
	if status != "" {
		q += ` AND status=?`
		args = append(args, status)
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT 200`
	return s.queryLeaves(ctx, q, args...)
}

// PendingLeavesForClasses lists pending leaves for a set of classes.
func (s *Store) PendingLeavesForClasses(ctx context.Context, classIDs []int64) ([]Leave, error) {
	if len(classIDs) == 0 {
		return nil, nil
	}
	q := `SELECT ` + leaveCols + ` FROM leave_requests WHERE status='pending' AND archived_at IS NULL AND class_id IN (`
	args := make([]any, 0, len(classIDs))
	for i, id := range classIDs {
		if i > 0 {
			q += ","
		}
		q += "?"
		args = append(args, id)
	}
	q += `) ORDER BY deadline_at`
	return s.queryLeaves(ctx, q, args...)
}

// PendingCountForClass counts pending leaves.
func (s *Store) PendingCountForClass(ctx context.Context, classID int64) int {
	var n int
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM leave_requests WHERE class_id=? AND status='pending'`, classID).Scan(&n)
	return n
}

func (s *Store) queryLeaves(ctx context.Context, q string, args ...any) ([]Leave, error) {
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Leave
	for rows.Next() {
		l, err := scanLeave(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// LeaveFilter is shared by admin/community/archive queries.
type LeaveFilter struct {
	CreatedFrom    string
	CreatedTo      string
	StartFrom      string
	StartTo        string
	Building       string
	ClassName      string
	TeacherName    string
	CounselorName  string
	Status         string
	Keyword        string
	CleanedFrom    string
	CleanedTo      string
	NoPhoneKeyword bool
	Page           int
	PageSize       int
}

// Page returns normalized paging values.
func (f LeaveFilter) Paging() (limit, offset int) {
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

func (f LeaveFilter) where(alias string) (string, []any, error) {
	var clauses []string
	var args []any
	p := alias + "."
	add := func(cond string, vals ...any) {
		clauses = append(clauses, cond)
		args = append(args, vals...)
	}
	if f.CreatedFrom != "" || f.CreatedTo != "" {
		start, end, err := DateBounds(f.CreatedFrom, f.CreatedTo)
		if err != nil {
			return "", nil, err
		}
		if start > 0 {
			add(p+"created_at>=?", start)
		}
		if end > 0 {
			add(p+"created_at<?", end)
		}
	}
	if f.StartFrom != "" || f.StartTo != "" {
		start, end, err := DateBounds(f.StartFrom, f.StartTo)
		if err != nil {
			return "", nil, err
		}
		if start > 0 {
			add(p+"start_at>=?", start)
		}
		if end > 0 {
			add(p+"start_at<?", end)
		}
	}
	if f.Building != "" {
		add(p+"dorm_building=?", validate.NFKC(f.Building))
	}
	if f.ClassName != "" {
		add(p+"class_name LIKE ?", "%"+validate.NFKC(f.ClassName)+"%")
	}
	if f.TeacherName != "" {
		add(p+"teacher_name LIKE ?", "%"+validate.NFKC(f.TeacherName)+"%")
	}
	if f.CounselorName != "" {
		add(p+"counselor_name LIKE ?", "%"+validate.NFKC(f.CounselorName)+"%")
	}
	if f.Status != "" {
		add(p+"status=?", f.Status)
	}
	if kw := strings.TrimSpace(f.Keyword); kw != "" {
		like := "%" + kw + "%"
		if f.NoPhoneKeyword {
			add("("+p+"student_name LIKE ? OR "+p+"student_no LIKE ?)", like, like)
		} else {
			add("("+p+"student_name LIKE ? OR "+p+"student_no LIKE ? OR "+p+"contact_phone LIKE ?)", like, like, like)
		}
	}
	if len(clauses) == 0 {
		return "", nil, nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args, nil
}

// AdminLeaves filters normal (non-archived) leaves.
func (s *Store) AdminLeaves(ctx context.Context, f LeaveFilter) ([]Leave, int, error) {
	where, args, err := f.where("l")
	if err != nil {
		return nil, 0, err
	}
	arch := " AND l.archived_at IS NULL"
	if where == "" {
		where = " WHERE l.archived_at IS NULL"
	} else {
		where += arch
	}
	var total int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM leave_requests l`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit, offset := f.Paging()
	q := `SELECT ` + leaveCols + ` FROM leave_requests l` + where + ` ORDER BY l.created_at DESC, l.id DESC LIMIT ? OFFSET ?`
	list, err := s.queryLeaves(ctx, q, append(args, limit, offset)...)
	return list, total, err
}

// CommunityLeaves lists approved leaves for the buildings a community teacher
// is responsible for.
func (s *Store) CommunityLeaves(ctx context.Context, userID int64, f LeaveFilter) ([]Leave, int, error) {
	buildings, err := s.CommunityBuildings(ctx, userID)
	if err != nil {
		return nil, 0, err
	}
	if len(buildings) == 0 {
		return nil, 0, nil
	}
	where, filterArgs, err := f.where("l")
	if err != nil {
		return nil, 0, err
	}
	in := ""
	var args []any
	for i, b := range buildings {
		if i > 0 {
			in += ","
		}
		in += "?"
		args = append(args, b)
	}
	cond := " WHERE l.archived_at IS NULL AND l.status='approved' AND l.dorm_building IN (" + in + ")"
	if where != "" {
		cond += strings.Replace(where, " WHERE ", " AND ", 1)
		args = append(args, filterArgs...)
	}
	var total int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM leave_requests l`+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit, offset := f.Paging()
	list, err := s.queryLeaves(ctx, `SELECT `+leaveCols+` FROM leave_requests l`+cond+` ORDER BY l.start_at DESC LIMIT ? OFFSET ?`,
		append(args, limit, offset)...)
	return list, total, err
}

// ArchivedLeaves lists retained leaves with row-level permission filtering.
func (s *Store) ArchivedLeaves(ctx context.Context, actor User, f LeaveFilter) ([]Leave, int, error) {
	where, filterArgs, err := f.where("l")
	if err != nil {
		return nil, 0, err
	}
	cond := " WHERE l.archived_at IS NOT NULL"
	var args []any
	switch actor.Role {
	case "admin":
	case "teacher":
		cond += " AND (l.teacher_user_id=? OR ac.last_teacher=?)"
		args = append(args, actor.ID, actor.ID)
	case "counselor":
		cond += " AND (l.counselor_user_id=? OR ac.last_counselor=?)"
		args = append(args, actor.ID, actor.ID)
	case "community":
		buildings, err := s.CommunityBuildings(ctx, actor.ID)
		if err != nil {
			return nil, 0, err
		}
		if len(buildings) == 0 {
			return nil, 0, nil
		}
		cond += " AND l.status='approved' AND l.dorm_building IN ("
		for i, b := range buildings {
			if i > 0 {
				cond += ","
			}
			cond += "?"
			args = append(args, b)
		}
		cond += ")"
	default:
		return nil, 0, ErrForbidden
	}
	if where != "" {
		cond += strings.Replace(where, " WHERE ", " AND ", 1)
		args = append(args, filterArgs...)
	}
	if f.CleanedFrom != "" || f.CleanedTo != "" {
		start, end, err := DateBounds(f.CleanedFrom, f.CleanedTo)
		if err != nil {
			return nil, 0, err
		}
		if start > 0 {
			cond += " AND ac.cleaned_at>=?"
			args = append(args, start)
		}
		if end > 0 {
			cond += " AND ac.cleaned_at<?"
			args = append(args, end)
		}
	}
	from := ` FROM leave_requests l JOIN (SELECT id AS acid, last_teacher_user_id AS last_teacher, last_counselor_user_id AS last_counselor, cleaned_at FROM archived_classes) ac ON ac.acid=l.archived_class_id`
	var total int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*)`+from+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit, offset := f.Paging()
	q := `SELECT ` + leaveCols + from + cond + ` ORDER BY ac.cleaned_at DESC, l.id DESC LIMIT ? OFFSET ?`
	list, err := s.queryLeaves(ctx, q, append(args, limit, offset)...)
	return list, total, err
}

// CanViewArchived checks one archived leave against the actor's permissions.
func (s *Store) CanViewArchived(ctx context.Context, actor User, l Leave) bool {
	if l.ArchivedAt == 0 {
		return false
	}
	switch actor.Role {
	case "admin":
		return true
	case "teacher":
		if l.TeacherUserID == actor.ID {
			return true
		}
		if l.ArchivedClassID == 0 {
			return false
		}
		var last int64
		_ = s.DB.QueryRowContext(ctx, `SELECT COALESCE(last_teacher_user_id,0) FROM archived_classes WHERE id=?`, l.ArchivedClassID).Scan(&last)
		return last == actor.ID
	case "counselor":
		if l.CounselorUserID == actor.ID {
			return true
		}
		if l.ArchivedClassID == 0 {
			return false
		}
		var last int64
		_ = s.DB.QueryRowContext(ctx, `SELECT COALESCE(last_counselor_user_id,0) FROM archived_classes WHERE id=?`, l.ArchivedClassID).Scan(&last)
		return last == actor.ID
	case "community":
		if l.Status != "approved" {
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

// LeaveTimeEdits lists change history.
func (s *Store) LeaveTimeEdits(ctx context.Context, leaveID int64) ([]struct {
	EditorName string
	OldStart   int64
	OldEnd     int64
	NewStart   int64
	NewEnd     int64
	CreatedAt  int64
}, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT editor_name, old_start, old_end, new_start, new_end, created_at
		FROM leave_time_edits WHERE leave_id=? ORDER BY created_at`, leaveID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []struct {
		EditorName string
		OldStart   int64
		OldEnd     int64
		NewStart   int64
		NewEnd     int64
		CreatedAt  int64
	}
	for rows.Next() {
		var r struct {
			EditorName string
			OldStart   int64
			OldEnd     int64
			NewStart   int64
			NewEnd     int64
			CreatedAt  int64
		}
		if err := rows.Scan(&r.EditorName, &r.OldStart, &r.OldEnd, &r.NewStart, &r.NewEnd, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
