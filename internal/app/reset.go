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

// ResetRequest is a student/staff assisted reset application.
type ResetRequest struct {
	ID          int64
	UserID      int64
	ClassID     int64
	Status      string
	RequestedIP string
	RequestedAt int64
	ApprovedBy  int64
	ApprovedAt  int64
	ExpiresAt   int64
	UsedAt      int64
	UserName    string
	StudentNo   string
	Role        string
	ClassName   string
	Department  string
	TeacherID   int64
	CounselorID int64
}

// PendingResetForUser returns the user's pending request.
func (s *Store) PendingResetForUser(ctx context.Context, userID int64) (ResetRequest, error) {
	return s.scanResetRequest(s.DB.QueryRowContext(ctx, `SELECT r.id, r.user_id, COALESCE(r.class_id,0), r.status,
		r.requested_ip, r.requested_at, COALESCE(r.approved_by,0), COALESCE(r.approved_at,0), r.expires_at, COALESCE(r.used_at,0),
		u.username, COALESCE(u.student_no,''), u.role, COALESCE(c.name,''), COALESCE(c.department,''),
		COALESCE(c.teacher_user_id,0), COALESCE(c.counselor_user_id,0)
		FROM password_reset_requests r JOIN users u ON u.id=r.user_id
		LEFT JOIN classes c ON c.id=r.class_id
		WHERE r.user_id=? AND r.status='pending'`, userID))
}

func (s *Store) scanResetRequest(r rowScanner) (ResetRequest, error) {
	var q ResetRequest
	err := r.Scan(&q.ID, &q.UserID, &q.ClassID, &q.Status, &q.RequestedIP, &q.RequestedAt,
		&q.ApprovedBy, &q.ApprovedAt, &q.ExpiresAt, &q.UsedAt, &q.UserName, &q.StudentNo, &q.Role,
		&q.ClassName, &q.Department, &q.TeacherID, &q.CounselorID)
	if errors.Is(err, sql.ErrNoRows) {
		return q, ErrNotFound
	}
	return q, err
}

// resetKey derives the rate-limit key for a reset attempt.
func (s *Store) resetKey(userID int64, ident string, ip string) string {
	if userID > 0 {
		return secure.HMACHex(s.SessionSecret(), "u:"+itoa(userID)+":"+ip)
	}
	return secure.HMACHex(s.SessionSecret(), "x:"+ident+":"+ip)
}

// resetLockedMessage checks both IP-global and per-key locks.
func (s *Store) resetLockedMessage(ctx context.Context, key, ip string) error {
	now := s.NowUnix()
	if until, _ := s.LoginLocked(ctx, "reset_ip", ip); until > now {
		return fmt.Errorf("校验失败次数过多，请 %d 分钟后再试", (until-now+59)/60)
	}
	if until, _ := s.ResetLocked(ctx, key); until > now {
		return fmt.Errorf("校验失败次数过多，请 %d 分钟后再试", (until-now+59)/60)
	}
	return nil
}

// failReset records both the ladder and IP-global counters.
func (s *Store) failReset(ctx context.Context, key string, userID int64, ip string) error {
	until, _ := s.RecordResetFailure(ctx, key, userID, ip)
	_ = s.RecordLoginFailure(ctx, "reset_ip", ip, time.Hour, 30*time.Minute, 20)
	if until > s.NowUnix() {
		return fmt.Errorf("校验失败次数过多，请 %d 分钟后再试", (until-s.NowUnix()+59)/60)
	}
	return nil
}

// FindStaffForReset looks up a staff/admin account by username or phone.
func (s *Store) FindStaffForReset(ctx context.Context, ident string, adminOnly bool) (User, error) {
	if adminOnly {
		return s.FindAdminLogin(ctx, ident)
	}
	u, err := s.FindStaffLogin(ctx, ident)
	if err == nil {
		return u, nil
	}
	return s.FindAdminLogin(ctx, ident)
}

// VerifyStaffPhone checks a staff phone with the staged lockout.
func (s *Store) VerifyStaffPhone(ctx context.Context, u User, phone, ip string) error {
	key := s.resetKey(u.ID, u.UsernameNorm, ip)
	if err := s.resetLockedMessage(ctx, key, ip); err != nil {
		return err
	}
	norm, err := validate.NormalizePhone(phone)
	if err != nil || norm != u.Phone {
		if err := s.failReset(ctx, key, u.ID, ip); err != nil {
			return err
		}
		return errors.New("信息不匹配，请重试")
	}
	_ = s.ClearResetFailures(ctx, key)
	return nil
}

// CreatePasswordResetToken issues a 10-minute one-time reset token.
func (s *Store) CreatePasswordResetToken(ctx context.Context, userID, issuedBy int64, ip string) (string, error) {
	raw, err := secure.RandomToken(32)
	if err != nil {
		return "", err
	}
	now := s.NowUnix()
	_, err = s.DB.ExecContext(ctx, `INSERT INTO password_reset_tokens (user_id,token_hash,expires_at,created_at,ip,issued_by)
		VALUES (?,?,?,?,?,?)`, userID, secure.SHA256Hex(raw), now+600, now, ip, nullableID(issuedBy))
	if err != nil {
		return "", err
	}
	return raw, nil
}

// ResetTokenUser resolves a reset cookie to its user without consuming it.
func (s *Store) ResetTokenUser(ctx context.Context, raw string) (User, int64, error) {
	var userID, expires int64
	var used sql.NullInt64
	err := s.DB.QueryRowContext(ctx, `SELECT user_id, expires_at, used_at FROM password_reset_tokens WHERE token_hash=?`,
		secure.SHA256Hex(raw)).Scan(&userID, &expires, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, 0, ErrNotFound
	}
	if err != nil {
		return User{}, 0, err
	}
	if used.Valid || expires <= s.NowUnix() {
		return User{}, 0, ErrNotFound
	}
	u, err := s.GetUserByID(ctx, userID)
	return u, expires, err
}

// ResetPasswordWithToken consumes a reset token and sets a new password.
func (s *Store) ResetPasswordWithToken(ctx context.Context, raw, newPassword, ip string) (User, error) {
	u, _, err := s.ResetTokenUser(ctx, raw)
	if err != nil {
		return User{}, errors.New("重置链接已过期或已使用，请重新发起")
	}
	if err := validate.ValidatePassword(newPassword, u.Role == "admin"); err != nil {
		return User{}, err
	}
	hash, err := secure.HashPassword(newPassword)
	if err != nil {
		return User{}, err
	}
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		var expires int64
		var used sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT expires_at, used_at FROM password_reset_tokens WHERE token_hash=?`,
			secure.SHA256Hex(raw)).Scan(&expires, &used); err != nil {
			return errors.New("重置链接已过期或已使用，请重新发起")
		}
		if used.Valid || expires <= s.NowUnix() {
			return errors.New("重置链接已过期或已使用，请重新发起")
		}
		if err := s.CanChangePassword(ctx, u.ID); err != nil {
			return err
		}
		if err := s.SetPassword(ctx, tx, u.ID, hash, "reset", ip); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE password_reset_requests SET status='used', used_at=? WHERE user_id=? AND status='approved'`, s.NowUnix(), u.ID)
		return err
	})
	if err != nil {
		return User{}, err
	}
	return u, nil
}

// ChangePassword is the logged-in self-service path.
func (s *Store) ChangePassword(ctx context.Context, u User, oldPassword, newPassword, ip string) error {
	if !secure.CheckPassword(u.PasswordHash, oldPassword) {
		return errors.New("当前密码错误")
	}
	if err := validate.ValidatePassword(newPassword, u.Role == "admin"); err != nil {
		return err
	}
	if oldPassword == newPassword {
		return errors.New("新密码不能与当前密码相同")
	}
	hash, err := secure.HashPassword(newPassword)
	if err != nil {
		return err
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if u.Status == "soft_deleted" {
			return errors.New("账号已停用，仅可查看历史，不能修改密码")
		}
		if err := s.CanChangePassword(ctx, u.ID); err != nil {
			return err
		}
		return s.SetPassword(ctx, tx, u.ID, hash, "self", ip)
	})
}

// CreateStudentResetRequest starts a student assisted reset.
func (s *Store) CreateStudentResetRequest(ctx context.Context, studentNo, name, ip string) (ResetRequest, string, string, error) {
	no, _, err := validate.NormalizeStudentNo(studentNo)
	if err != nil {
		return ResetRequest{}, "", "", errors.New("信息不匹配，请重试")
	}
	nameNorm := strings.TrimSpace(validate.NFKC(name))
	ident := no + ":" + strings.ToLower(nameNorm)
	u, err := s.GetUserByStudentNo(ctx, no)
	var userID int64
	if err == nil {
		userID = u.ID
	}
	key := s.resetKey(0, ident, ip)
	if userID > 0 {
		key = s.resetKey(userID, u.UsernameNorm, ip)
	}
	if err := s.resetLockedMessage(ctx, key, ip); err != nil {
		return ResetRequest{}, "", "", err
	}
	if err != nil || u.Role != "student" || validate.NFKC(u.Username) != nameNorm ||
		(u.Status == "soft_deleted" && u.PurgeAfter <= s.NowUnix()) {
		if e := s.failReset(ctx, key, userID, ip); e != nil {
			return ResetRequest{}, "", "", e
		}
		return ResetRequest{}, "", "", errors.New("信息不匹配，请重试")
	}
	// IP application limit: 5 per hour.
	var recent int
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM password_reset_requests WHERE requested_ip=? AND requested_at>=?`,
		ip, s.NowUnix()-3600).Scan(&recent)
	if recent >= 5 {
		return ResetRequest{}, "", "", errors.New("申请次数过多，请 1 小时后再试")
	}
	_ = s.ClearResetFailures(ctx, key)
	return s.newResetRequest(ctx, u, ip)
}

// CreateStaffResetRequest starts a staff/admin assisted reset.
func (s *Store) CreateStaffResetRequest(ctx context.Context, ident, phone, ip string) (ResetRequest, string, string, error) {
	u, err := s.FindStaffForReset(ctx, ident, false)
	var userID int64
	userIdent := strings.ToLower(validate.NFKC(ident))
	key := s.resetKey(0, "staff:"+userIdent, ip)
	if err == nil {
		userID = u.ID
		key = s.resetKey(u.ID, u.UsernameNorm, ip)
	}
	if err := s.resetLockedMessage(ctx, key, ip); err != nil {
		return ResetRequest{}, "", "", err
	}
	normPhone, perr := validate.NormalizePhone(phone)
	if err != nil || perr != nil || normPhone != u.Phone || (u.Status == "soft_deleted" && u.PurgeAfter <= s.NowUnix()) {
		if e := s.failReset(ctx, key, userID, ip); e != nil {
			return ResetRequest{}, "", "", e
		}
		return ResetRequest{}, "", "", errors.New("信息不匹配，请重试")
	}
	var recent int
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM password_reset_requests WHERE requested_ip=? AND requested_at>=?`,
		ip, s.NowUnix()-3600).Scan(&recent)
	if recent >= 5 {
		return ResetRequest{}, "", "", errors.New("申请次数过多，请 1 小时后再试")
	}
	_ = s.ClearResetFailures(ctx, key)
	return s.newResetRequest(ctx, u, ip)
}

func (s *Store) newResetRequest(ctx context.Context, u User, ip string) (ResetRequest, string, string, error) {
	code, err := secure.ResetCode()
	if err != nil {
		return ResetRequest{}, "", "", err
	}
	codeHash, err := secure.HashPassword(code)
	if err != nil {
		return ResetRequest{}, "", "", err
	}
	browserToken, err := secure.RandomToken(32)
	if err != nil {
		return ResetRequest{}, "", "", err
	}
	now := s.NowUnix()
	var classID int64
	var teacherID, counselorID int64
	if u.Role == "student" {
		st, err := s.GetStudent(ctx, u.ID)
		if err == nil {
			classID = st.ClassID
			if classID != 0 {
				c, err := s.GetClass(ctx, classID)
				if err == nil {
					teacherID, counselorID = c.TeacherUserID, c.CounselorUserID
				}
			}
		}
	}
	var reqID int64
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE password_reset_requests SET status='expired' WHERE user_id=? AND status IN ('pending','approved')`, u.ID); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO password_reset_requests
			(user_id,class_id,status,request_code_hash,browser_token_hash,requested_ip,requested_at,expires_at)
			VALUES (?,?, 'pending', ?,?,?,?,?)`, u.ID, nullableID(classID), codeHash, secure.SHA256Hex(browserToken), ip, now, now+86400)
		if err != nil {
			return err
		}
		reqID, _ = res.LastInsertId()
		title := "重置申请待批准"
		body := u.Username + " 发起了密码重置申请，请核对身份后批准。"
		link := "/t/recover"
		switch u.Role {
		case "admin":
			link = "/admin/recover"
		case "community":
			link = "/c/recover"
		}
		if u.Role == "admin" {
			return s.NotifyAdmins(ctx, tx, "reset_request", title, body, link)
		}
		if u.Role == "student" {
			if teacherID == 0 && counselorID == 0 {
				return s.NotifyAdmins(ctx, tx, "reset_request", title, body+"（该班暂无班主任和辅导员）", "/admin/recover")
			}
			_ = s.Notify(ctx, tx, teacherID, "reset_request", title, body, "/t/recover", 0)
			_ = s.Notify(ctx, tx, counselorID, "reset_request", title, body, "/t/recover", 0)
			return nil
		}
		// Staff colleague approval: notify same-role active staff.
		rows, err := tx.QueryContext(ctx, `SELECT id FROM users WHERE role=? AND status='active' AND id<>?`, u.Role, u.ID)
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		staffLink := "/t/recover"
		if u.Role == "community" {
			staffLink = "/c/recover"
		}
		for _, id := range ids {
			_ = s.Notify(ctx, tx, id, "reset_request", title, body, staffLink, 0)
		}
		return nil
	})
	if err != nil {
		return ResetRequest{}, "", "", err
	}
	req, err := s.GetResetRequest(ctx, reqID)
	return req, code, browserToken, err
}

// GetResetRequest loads one request.
func (s *Store) GetResetRequest(ctx context.Context, id int64) (ResetRequest, error) {
	return s.scanResetRequest(s.DB.QueryRowContext(ctx, `SELECT r.id, r.user_id, COALESCE(r.class_id,0), r.status,
		r.requested_ip, r.requested_at, COALESCE(r.approved_by,0), COALESCE(r.approved_at,0), r.expires_at, COALESCE(r.used_at,0),
		u.username, COALESCE(u.student_no,''), u.role, COALESCE(c.name,''), COALESCE(c.department,''),
		COALESCE(c.teacher_user_id,0), COALESCE(c.counselor_user_id,0)
		FROM password_reset_requests r JOIN users u ON u.id=r.user_id
		LEFT JOIN classes c ON c.id=r.class_id WHERE r.id=?`, id))
}

// GetResetRequestByBrowser resolves the applicant's browser cookie.
func (s *Store) GetResetRequestByBrowser(ctx context.Context, browserToken string) (ResetRequest, error) {
	return s.scanResetRequest(s.DB.QueryRowContext(ctx, `SELECT r.id, r.user_id, COALESCE(r.class_id,0), r.status,
		r.requested_ip, r.requested_at, COALESCE(r.approved_by,0), COALESCE(r.approved_at,0), r.expires_at, COALESCE(r.used_at,0),
		u.username, COALESCE(u.student_no,''), u.role, COALESCE(c.name,''), COALESCE(c.department,''),
		COALESCE(c.teacher_user_id,0), COALESCE(c.counselor_user_id,0)
		FROM password_reset_requests r JOIN users u ON u.id=r.user_id
		LEFT JOIN classes c ON c.id=r.class_id
		WHERE r.browser_token_hash=? ORDER BY r.id DESC LIMIT 1`, secure.SHA256Hex(browserToken)))
}

// canApproveReset enforces the assisted-reset approval matrix.
func (s *Store) canApproveReset(ctx context.Context, actor User, q ResetRequest) bool {
	if q.UserID == actor.ID {
		return false
	}
	switch q.Role {
	case "student":
		if actor.Role == "admin" {
			return q.TeacherID == 0 && q.CounselorID == 0
		}
		if q.ClassID == 0 {
			return false
		}
		c, err := s.GetClass(ctx, q.ClassID)
		if err != nil {
			return false
		}
		if actor.Role == "teacher" {
			return c.TeacherUserID == actor.ID
		}
		if actor.Role == "counselor" {
			return c.CounselorUserID == actor.ID
		}
		return false
	case "admin":
		return actor.Role == "admin"
	default:
		return actor.Role == q.Role
	}
}

// checkResetCode validates a 6-digit application code for one request.
func (s *Store) checkResetCode(ctx context.Context, q ResetRequest, code string) error {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return errors.New("申请码无效或已过期")
	}
	var hash string
	var attempts int
	if err := s.DB.QueryRowContext(ctx, `SELECT request_code_hash, attempts FROM password_reset_requests WHERE id=?`, q.ID).Scan(&hash, &attempts); err != nil {
		return errors.New("申请码无效或已过期")
	}
	if attempts >= 5 || q.ExpiresAt <= s.NowUnix() {
		return errors.New("申请码无效或已过期")
	}
	if !secure.CheckPassword(hash, code) {
		_, _ = s.DB.ExecContext(ctx, `UPDATE password_reset_requests SET attempts=attempts+1,
			status=CASE WHEN attempts+1>=5 THEN 'expired' ELSE status END WHERE id=?`, q.ID)
		_ = s.RecordLoginFailure(ctx, "reset_code", q.RequestedIP, 10*time.Minute, 30*time.Minute, 10)
		return errors.New("申请码无效或已过期")
	}
	return nil
}

// VerifyResetCode finds a pending request by applicant identifier and its
// 6-digit code. Attempts are capped at five per request; wrong guesses are
// attributed through the applicant identifier.
func (s *Store) VerifyResetCode(ctx context.Context, actor User, ident, code string) (ResetRequest, error) {
	code = strings.TrimSpace(code)
	ident = strings.TrimSpace(ident)
	if len(code) != 6 || ident == "" {
		return ResetRequest{}, errors.New("申请码无效或已过期")
	}
	var u User
	var err error
	if no, _, e := validate.NormalizeStudentNo(ident); e == nil {
		u, err = s.GetUserByStudentNo(ctx, no)
	} else {
		_, norm, e := validate.NormalizeUsername(ident)
		if e != nil {
			return ResetRequest{}, errors.New("申请码无效或已过期")
		}
		u, err = s.GetUserByUsername(ctx, norm)
	}
	if err != nil {
		return ResetRequest{}, errors.New("申请码无效或已过期")
	}
	q, err := s.PendingResetForUser(ctx, u.ID)
	if err != nil {
		return ResetRequest{}, errors.New("申请码无效或已过期")
	}
	// Authorize before consuming an attempt. Otherwise any signed-in staff
	// member could expire someone else's request with five wrong guesses.
	if !s.canApproveReset(ctx, actor, q) {
		return ResetRequest{}, errors.New("无权批准该申请：请由本班班主任/辅导员、同角色同事或另一位管理员处理")
	}
	if until, _ := s.LoginLocked(ctx, "reset_code", q.RequestedIP); until > s.NowUnix() {
		return ResetRequest{}, errors.New("申请码尝试次数过多，请稍后再试")
	}
	if err := s.checkResetCode(ctx, q, code); err != nil {
		return ResetRequest{}, err
	}
	return q, nil
}

// ApproveResetRequest marks a request approved after re-validating the
// applicant-supplied 6-digit code. A staff member cannot approve a request
// created in the same browser session (browserToken is the lp_recover cookie).
func (s *Store) ApproveResetRequest(ctx context.Context, actor User, requestID int64, code, browserToken string) error {
	q, err := s.GetResetRequest(ctx, requestID)
	if err != nil {
		return ErrNotFound
	}
	if q.Status != "pending" || q.ExpiresAt <= s.NowUnix() {
		return errors.New("该申请已失效")
	}
	if !s.canApproveReset(ctx, actor, q) {
		return errors.New("无权批准该申请")
	}
	if browserToken != "" {
		var creatorHash string
		_ = s.DB.QueryRowContext(ctx, `SELECT browser_token_hash FROM password_reset_requests WHERE id=?`, requestID).Scan(&creatorHash)
		if creatorHash != "" && secure.Equal(creatorHash, secure.SHA256Hex(browserToken)) {
			return errors.New("不能批准本浏览器发起的申请；请让申请人在自己的设备上提交，或由另一位老师核验后处理")
		}
	}
	if until, _ := s.LoginLocked(ctx, "reset_code", q.RequestedIP); until > s.NowUnix() {
		return errors.New("申请码尝试次数过多，请稍后再试")
	}
	if err := s.checkResetCode(ctx, q, code); err != nil {
		return err
	}
	var approvedToday int
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM password_reset_requests WHERE approved_by=? AND approved_at>=?`,
		actor.ID, StartOfDayCST(s.Now()).Unix()).Scan(&approvedToday)
	if approvedToday >= 3 {
		return errors.New("你今日批准次数已达上限（3 次）")
	}
	now := s.NowUnix()
	return s.withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE password_reset_requests SET status='approved', approved_by=?, approved_at=?
			WHERE id=? AND status='pending' AND expires_at>?`, actor.ID, now, requestID, now)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return errors.New("该申请已被处理，请刷新")
		}
		return s.Notify(ctx, tx, q.UserID, "reset_approved", "重置申请已批准", "你的密码重置申请已批准，请回到申请页面继续设置新密码。", "/recover/status", 0)
	})
}

// RejectResetRequest rejects a pending request.
func (s *Store) RejectResetRequest(ctx context.Context, actor User, requestID int64) error {
	q, err := s.GetResetRequest(ctx, requestID)
	if err != nil {
		return ErrNotFound
	}
	if !s.canApproveReset(ctx, actor, q) {
		return errors.New("无权处理该申请")
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE password_reset_requests SET status='rejected' WHERE id=? AND status='pending'`, requestID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("该申请已被处理，请刷新")
	}
	return nil
}

// IssueResetTokenForBrowser creates the 10-minute token once a request is
// approved, then marks it used so the link is not re-issued.
func (s *Store) IssueResetTokenForBrowser(ctx context.Context, browserToken, ip string) (string, error) {
	q, err := s.GetResetRequestByBrowser(ctx, browserToken)
	if err != nil {
		return "", ErrNotFound
	}
	now := s.NowUnix()
	if q.Status != "approved" || q.UsedAt != 0 {
		return "", errors.New("申请尚未批准或重置链接已发放")
	}
	if q.ExpiresAt <= now {
		return "", errors.New("申请已过期，请重新发起")
	}
	raw, err := secure.RandomToken(32)
	if err != nil {
		return "", err
	}
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE password_reset_requests SET used_at=?
			WHERE id=? AND status='approved' AND used_at IS NULL AND expires_at>?`, now, q.ID, now)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return errors.New("重置链接已发放，请刷新页面")
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO password_reset_tokens (user_id,token_hash,expires_at,created_at,ip,issued_by)
			VALUES (?,?,?,?,?,?)`, q.UserID, secure.SHA256Hex(raw), now+600, now, ip, nullableID(q.ApprovedBy))
		return err
	})
	if err != nil {
		return "", err
	}
	return raw, nil
}

// PendingResetsForStaff lists pending requests the actor may approve. The
// per-row permission matrix is enforced by canApproveReset.
func (s *Store) PendingResetsForStaff(ctx context.Context, actor User) ([]ResetRequest, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT r.id, r.user_id, COALESCE(r.class_id,0), r.status,
		r.requested_ip, r.requested_at, COALESCE(r.approved_by,0), COALESCE(r.approved_at,0), r.expires_at, COALESCE(r.used_at,0),
		u.username, COALESCE(u.student_no,''), u.role, COALESCE(c.name,''), COALESCE(c.department,''),
		COALESCE(c.teacher_user_id,0), COALESCE(c.counselor_user_id,0)
		FROM password_reset_requests r JOIN users u ON u.id=r.user_id LEFT JOIN classes c ON c.id=r.class_id
		WHERE r.status='pending' AND r.expires_at>? AND r.user_id<>?
		ORDER BY r.requested_at DESC`, s.NowUnix(), actor.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ResetRequest
	for rows.Next() {
		var q ResetRequest
		if err := rows.Scan(&q.ID, &q.UserID, &q.ClassID, &q.Status, &q.RequestedIP, &q.RequestedAt,
			&q.ApprovedBy, &q.ApprovedAt, &q.ExpiresAt, &q.UsedAt, &q.UserName, &q.StudentNo, &q.Role,
			&q.ClassName, &q.Department, &q.TeacherID, &q.CounselorID); err != nil {
			return nil, err
		}
		if s.canApproveReset(ctx, actor, q) {
			out = append(out, q)
		}
	}
	return out, rows.Err()
}

// AdminPendingResets lists requests an administrator may approve.
func (s *Store) AdminPendingResets(ctx context.Context, adminID int64) ([]ResetRequest, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT r.id, r.user_id, COALESCE(r.class_id,0), r.status,
		r.requested_ip, r.requested_at, COALESCE(r.approved_by,0), COALESCE(r.approved_at,0), r.expires_at, COALESCE(r.used_at,0),
		u.username, COALESCE(u.student_no,''), u.role, COALESCE(c.name,''), COALESCE(c.department,''),
		COALESCE(c.teacher_user_id,0), COALESCE(c.counselor_user_id,0)
		FROM password_reset_requests r JOIN users u ON u.id=r.user_id LEFT JOIN classes c ON c.id=r.class_id
		WHERE r.status='pending' AND r.expires_at>? AND r.user_id<>?
		  AND ( (u.role='admin')
		     OR (u.role='student' AND COALESCE(c.teacher_user_id,0)=0 AND COALESCE(c.counselor_user_id,0)=0) )
		ORDER BY r.requested_at DESC`, s.NowUnix(), adminID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ResetRequest
	for rows.Next() {
		var q ResetRequest
		if err := rows.Scan(&q.ID, &q.UserID, &q.ClassID, &q.Status, &q.RequestedIP, &q.RequestedAt,
			&q.ApprovedBy, &q.ApprovedAt, &q.ExpiresAt, &q.UsedAt, &q.UserName, &q.StudentNo, &q.Role,
			&q.ClassName, &q.Department, &q.TeacherID, &q.CounselorID); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

// ExpireResetRequests marks stale pending requests expired.
func (s *Store) ExpireResetRequests(ctx context.Context) (int64, error) {
	res, err := s.DB.ExecContext(ctx, `UPDATE password_reset_requests SET status='expired' WHERE status='pending' AND expires_at<=?`, s.NowUnix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
