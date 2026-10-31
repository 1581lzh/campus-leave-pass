package app

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"leavepass/internal/secure"
	"leavepass/internal/validate"
)

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("记录不存在")

// GetUserByID loads any user, including soft-deleted.
func (s *Store) GetUserByID(ctx context.Context, id int64) (User, error) {
	u, err := scanUser(s.DB.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

// GetUserByStudentNo loads a student by normalized student number.
func (s *Store) GetUserByStudentNo(ctx context.Context, no string) (User, error) {
	u, err := scanUser(s.DB.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE student_no_norm=?`, no))
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

// GetUserByUsername loads a non-student by normalized username.
func (s *Store) GetUserByUsername(ctx context.Context, norm string) (User, error) {
	u, err := scanUser(s.DB.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE username_norm=? AND role<>'student'`, norm))
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

// FindStaffLogin resolves a staff login identifier (username or phone).
func (s *Store) FindStaffLogin(ctx context.Context, ident string) (User, error) {
	ident = strings.TrimSpace(ident)
	if phone, err := validate.NormalizePhone(ident); err == nil {
		u, err := scanUser(s.DB.QueryRowContext(ctx,
			`SELECT `+userCols+` FROM users WHERE phone=? AND role IN ('teacher','counselor','community')`, phone))
		if errors.Is(err, sql.ErrNoRows) {
			return u, ErrNotFound
		}
		return u, err
	}
	norm := strings.ToLower(validate.NFKC(ident))
	u, err := scanUser(s.DB.QueryRowContext(ctx,
		`SELECT `+userCols+` FROM users WHERE username_norm=? AND role IN ('teacher','counselor','community')`, norm))
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

// FindAdminLogin resolves an administrator by username or phone.
func (s *Store) FindAdminLogin(ctx context.Context, ident string) (User, error) {
	ident = strings.TrimSpace(ident)
	if phone, err := validate.NormalizePhone(ident); err == nil {
		u, err := scanUser(s.DB.QueryRowContext(ctx,
			`SELECT `+userCols+` FROM users WHERE phone=? AND role='admin'`, phone))
		if errors.Is(err, sql.ErrNoRows) {
			return u, ErrNotFound
		}
		return u, err
	}
	norm := strings.ToLower(validate.NFKC(ident))
	u, err := scanUser(s.DB.QueryRowContext(ctx,
		`SELECT `+userCols+` FROM users WHERE username_norm=? AND role='admin'`, norm))
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

// CreateSession issues a new session expiring at the next CST midnight and
// returns the raw cookie token.
func (s *Store) CreateSession(ctx context.Context, userID int64, ip, ua string) (string, error) {
	raw, err := secure.RandomToken(32)
	if err != nil {
		return "", err
	}
	now := s.Now()
	exp := NextMidnightCST(now)
	_, err = s.DB.ExecContext(ctx, `INSERT INTO sessions (token_hash,user_id,expires_at,created_at,ip,user_agent)
		VALUES (?,?,?,?,?,?)`, secure.SHA256Hex(raw), userID, exp.Unix(), now.Unix(), ip, truncate(ua, 255))
	if err != nil {
		return "", err
	}
	return raw, nil
}

// SessionUser is a session joined with its user.
type SessionUser struct {
	Session Session
	User    User
}

// LookupSession resolves a raw cookie token to an unexpired session and user.
func (s *Store) LookupSession(ctx context.Context, raw string) (SessionUser, error) {
	if raw == "" {
		return SessionUser{}, ErrNotFound
	}
	hash := secure.SHA256Hex(raw)
	var su SessionUser
	err := s.DB.QueryRowContext(ctx, `SELECT id, token_hash, user_id, expires_at, created_at, ip, user_agent
		FROM sessions WHERE token_hash=?`, hash).Scan(&su.Session.ID, &su.Session.TokenHash, &su.Session.UserID,
		&su.Session.ExpiresAt, &su.Session.CreatedAt, &su.Session.IP, &su.Session.UserAgent)
	if errors.Is(err, sql.ErrNoRows) {
		return su, ErrNotFound
	}
	if err != nil {
		return su, err
	}
	if su.Session.ExpiresAt <= s.NowUnix() {
		_, _ = s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE id=?`, su.Session.ID)
		return su, ErrNotFound
	}
	u, err := s.GetUserByID(ctx, su.Session.UserID)
	if err != nil {
		return su, err
	}
	if u.Status == "soft_deleted" && u.PurgeAfter <= s.NowUnix() {
		return su, ErrNotFound
	}
	su.User = u
	return su, nil
}

// DeleteSession removes one session by raw token.
func (s *Store) DeleteSession(ctx context.Context, raw string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash=?`, secure.SHA256Hex(raw))
	return err
}

// DeleteUserSessions removes every session of a user.
func (s *Store) DeleteUserSessions(ctx context.Context, userID int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=?`, userID)
	return err
}

// CountUserSessions counts unexpired sessions belonging to one user.
func (s *Store) CountUserSessions(ctx context.Context, userID int64) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE user_id=? AND expires_at>?`,
		userID, s.NowUnix()).Scan(&n)
	return n, err
}

// DeleteOtherUserSessions removes a user's sessions except the current browser.
// It deliberately cannot touch another user's sessions.
func (s *Store) DeleteOtherUserSessions(ctx context.Context, userID int64, currentTokenHash string) (int64, error) {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=? AND token_hash<>?`,
		userID, currentTokenHash)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// TouchLastLogin records a successful login.
func (s *Store) TouchLastLogin(ctx context.Context, userID int64) error {
	now := s.NowUnix()
	_, err := s.DB.ExecContext(ctx, `UPDATE users SET last_login_at=?, updated_at=? WHERE id=?`, now, now, userID)
	return err
}

// CSRFFor returns the deterministic per-session CSRF token.
func (s *Store) CSRFFor(sessionTokenHash string) string {
	return secure.HMACHex(s.SessionSecret(), "csrf:"+sessionTokenHash)
}

// CheckCSRF validates a submitted CSRF token against the session token hash.
func (s *Store) CheckCSRF(sessionTokenHash, submitted string) bool {
	if submitted == "" {
		return false
	}
	return secure.Equal(s.CSRFFor(sessionTokenHash), submitted)
}

// LoginLocked returns the lock expiry for a scope/key, or 0.
func (s *Store) LoginLocked(ctx context.Context, scope, key string) (int64, error) {
	var until int64
	err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(locked_until,0) FROM login_attempts WHERE scope=? AND key=?`, scope, key).Scan(&until)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if until > s.NowUnix() {
		return until, nil
	}
	return 0, nil
}

// RecordLoginFailure increments a failure counter and locks at threshold.
func (s *Store) RecordLoginFailure(ctx context.Context, scope, key string, window, lock time.Duration, threshold int) error {
	now := s.Now().Unix()
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var windowStart, count, lockedUntil int64
		err := tx.QueryRowContext(ctx, `SELECT window_start, failed_count, COALESCE(locked_until,0)
			FROM login_attempts WHERE scope=? AND key=?`, scope, key).Scan(&windowStart, &count, &lockedUntil)
		if errors.Is(err, sql.ErrNoRows) {
			_, err = tx.ExecContext(ctx, `INSERT INTO login_attempts (key,scope,window_start,failed_count,locked_until,updated_at)
				VALUES (?,?,?,?,?,?)`, key, scope, now, 1, nil, now)
			return err
		}
		if err != nil {
			return err
		}
		if lockedUntil > now {
			return nil
		}
		if lockedUntil > 0 || now-windowStart >= int64(window.Seconds()) {
			windowStart, count, lockedUntil = now, 0, 0
		}
		count++
		var lockVal any
		if count >= int64(threshold) {
			lockVal = now + int64(lock.Seconds())
			count = 0
			windowStart = now
		}
		_, err = tx.ExecContext(ctx, `UPDATE login_attempts SET window_start=?, failed_count=?, locked_until=?, updated_at=?
			WHERE scope=? AND key=?`, windowStart, count, lockVal, now, scope, key)
		return err
	})
}

// ClearLoginFailures removes a counter after success.
func (s *Store) ClearLoginFailures(ctx context.Context, scope, key string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM login_attempts WHERE scope=? AND key=?`, scope, key)
	return err
}

// UnlockIP clears counters for an IP across scopes.
func (s *Store) UnlockIP(ctx context.Context, ip string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM login_attempts WHERE key=? OR key LIKE ?`, ip, ip+":%")
	return err
}

// ResetLocked returns the lock expiry for a reset-attempt key.
func (s *Store) ResetLocked(ctx context.Context, keyHash string) (int64, error) {
	var until int64
	err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(locked_until,0) FROM password_reset_attempts WHERE key_hash=?`, keyHash).Scan(&until)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if until > s.NowUnix() {
		return until, nil
	}
	return 0, nil
}

// RecordResetFailure implements the 4→10min / 2→30min / always→30min ladder.
func (s *Store) RecordResetFailure(ctx context.Context, keyHash string, userID int64, ip string) (lockUntil int64, err error) {
	now := s.NowUnix()
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		var stage, failures, lockedUntil int64
		err := tx.QueryRowContext(ctx, `SELECT stage, failures, COALESCE(locked_until,0)
			FROM password_reset_attempts WHERE key_hash=?`, keyHash).Scan(&stage, &failures, &lockedUntil)
		if errors.Is(err, sql.ErrNoRows) {
			stage, failures, lockedUntil = 0, 0, 0
		} else if err != nil {
			return err
		}
		if lockedUntil > now {
			lockUntil = lockedUntil
			return nil
		}
		if lockedUntil > 0 {
			failures = 0
		}
		failures++
		switch {
		case stage == 0 && failures >= 4:
			stage, failures, lockUntil = 1, 0, now+600
		case stage == 1 && failures >= 2:
			stage, failures, lockUntil = 2, 0, now+1800
		case stage == 2:
			stage, failures, lockUntil = 2, 0, now+1800
		}
		var lockVal any
		if lockUntil > 0 {
			lockVal = lockUntil
		}
		var uid any
		if userID > 0 {
			uid = userID
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO password_reset_attempts (key_hash,user_id,ip,stage,failures,locked_until,updated_at)
			VALUES (?,?,?,?,?,?,?)
			ON CONFLICT(key_hash) DO UPDATE SET user_id=excluded.user_id, ip=excluded.ip, stage=excluded.stage,
			failures=excluded.failures, locked_until=excluded.locked_until, updated_at=excluded.updated_at`,
			keyHash, uid, ip, stage, failures, lockVal, now)
		return err
	})
	return lockUntil, err
}

// ClearResetFailures resets the ladder after a successful check.
func (s *Store) ClearResetFailures(ctx context.Context, keyHash string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM password_reset_attempts WHERE key_hash=?`, keyHash)
	return err
}

// CanChangePassword enforces 2/day and 3/week CST limits.
func (s *Store) CanChangePassword(ctx context.Context, userID int64) error {
	now := s.Now()
	var dayCount, weekCount int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM password_change_events WHERE user_id=? AND changed_at>=?`,
		userID, StartOfDayCST(now).Unix()).Scan(&dayCount); err != nil {
		return err
	}
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM password_change_events WHERE user_id=? AND changed_at>=?`,
		userID, StartOfWeekCST(now).Unix()).Scan(&weekCount); err != nil {
		return err
	}
	if dayCount >= 2 {
		return errors.New("今日密码修改次数已达上限（2 次），请明天再试")
	}
	if weekCount >= 3 {
		return errors.New("本周密码修改次数已达上限（3 次），请下周再试")
	}
	return nil
}

// SetPassword updates the password, records the change event, clears the
// initial flag and deletes all outstanding reset tokens.
func (s *Store) SetPassword(ctx context.Context, tx *sql.Tx, userID int64, hash, method, ip string) error {
	now := s.NowUnix()
	if _, err := tx.ExecContext(ctx, `UPDATE users SET password_hash=?, password_is_initial=0, updated_at=? WHERE id=?`,
		hash, now, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO password_change_events (user_id,changed_at,method,ip) VALUES (?,?,?,?)`,
		userID, now, method, ip); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM password_reset_tokens WHERE user_id=?`, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE password_reset_requests SET status='expired'
		WHERE user_id=? AND status IN ('pending','approved')`, userID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=?`, userID)
	return err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
