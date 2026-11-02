package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"leavepass/internal/secure"
	"leavepass/internal/validate"
)

// StaffRegisterInput is the validated-ish registration request.
type StaffRegisterInput struct {
	Code     string
	Role     string
	Username string
	Password string
	Phone    string
}

// ErrCodeUnavailable intentionally hides why a code failed.
var ErrCodeUnavailable = errors.New("注册码不可用（可能已过期、已用完或已停用），请联系管理员")

// Staff roles are the only roles a registration code may grant. The order is
// also the canonical storage/display order.
var staffRoleOrder = []string{"teacher", "counselor", "community"}

func isStaffRole(role string) bool {
	for _, r := range staffRoleOrder {
		if r == role {
			return true
		}
	}
	return false
}

func staffRoleLabel(role string) string {
	switch role {
	case "teacher":
		return "班主任"
	case "counselor":
		return "辅导员"
	case "community":
		return "社区老师"
	}
	return role
}

// normalizeStaffRoles de-duplicates and orders a role set. An empty set is
// rejected: every code must grant at least one role.
func normalizeStaffRoles(raw []string) ([]string, error) {
	seen := make(map[string]bool, len(raw))
	for _, role := range raw {
		role = strings.TrimSpace(role)
		if role == "" {
			continue
		}
		if !isStaffRole(role) {
			return nil, errors.New("注册码角色只能是班主任、辅导员或社区老师")
		}
		seen[role] = true
	}
	if len(seen) == 0 {
		return nil, errors.New("请至少选择一个注册角色")
	}
	out := make([]string, 0, len(seen))
	for _, role := range staffRoleOrder {
		if seen[role] {
			out = append(out, role)
		}
	}
	return out, nil
}

func splitStoredRoles(raw, fallback string) []string {
	seen := make(map[string]bool)
	for _, role := range strings.Split(raw, ",") {
		role = strings.TrimSpace(role)
		if role == "" || seen[role] || !isStaffRole(role) {
			continue
		}
		seen[role] = true
	}
	if len(seen) == 0 && isStaffRole(fallback) {
		seen[fallback] = true
	}
	out := make([]string, 0, len(seen))
	for _, role := range staffRoleOrder {
		if seen[role] {
			out = append(out, role)
		}
	}
	return out
}

// IPPrefix aggregates IPv6 to /64 for rate limiting.
func IPPrefix(ip string) string {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return ip
	}
	if parsed.To4() != nil {
		return ip
	}
	return parsed.Mask(net.CIDRMask(64, 128)).String()
}

// RegistrationAllowed checks successful-registration quotas and failure locks.
func (s *Store) RegistrationAllowed(ctx context.Context, ip string) error {
	now := s.NowUnix()
	if until, _ := s.LoginLocked(ctx, "reg_fail", ip); until > now {
		return fmt.Errorf("注册尝试次数过多，请 %d 分钟后再试", (until-now+59)/60)
	}
	prefix := IPPrefix(ip)
	var day, week int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM registration_attempts WHERE (ip=? OR ip_prefix=?) AND success=1 AND created_at>=?`,
		ip, prefix, now-86400).Scan(&day); err != nil {
		return err
	}
	if day >= 20 {
		return errors.New("该网络今日注册次数已达上限，请联系管理员")
	}
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM registration_attempts WHERE (ip=? OR ip_prefix=?) AND success=1 AND created_at>=?`,
		ip, prefix, now-7*86400).Scan(&week); err != nil {
		return err
	}
	if week >= 60 {
		return errors.New("该网络本周注册次数已达上限，请联系管理员")
	}
	return nil
}

// DeviceAllowed enforces the soft 3-accounts-per-device limit.
func (s *Store) DeviceAllowed(ctx context.Context, deviceHash string) error {
	if deviceHash == "" {
		return nil
	}
	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(DISTINCT user_id) FROM registration_attempts
		WHERE device_hash=? AND success=1 AND user_id IS NOT NULL AND created_at>=?`, deviceHash, s.NowUnix()-7*86400).Scan(&n); err != nil {
		return err
	}
	if n >= 3 {
		return errors.New("该设备近期已绑定多个账号，请使用其他设备或联系管理员")
	}
	return nil
}

func (s *Store) recordRegistrationAttempt(ctx context.Context, q queryer, ip, ua, deviceHash string, codeID int64, role string, userID int64, success bool, reason string) {
	if q == nil {
		q = s.DB
	}
	ua = truncate(ua, 255)
	var cid, uid any
	if codeID > 0 {
		cid = codeID
	}
	if userID > 0 {
		uid = userID
	}
	_, _ = q.ExecContext(ctx, `INSERT INTO registration_attempts (ip,ip_prefix,user_agent,device_hash,code_id,role,user_id,success,reason,created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`, ip, IPPrefix(ip), ua, deviceHash, cid, role, uid, boolInt(success), reason, s.NowUnix())
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

const registrationCodeCols = `id, code_hash, code_hint, role, roles, max_uses, used_count,
	valid_from, valid_until, active, note, COALESCE(created_by,0), created_at, updated_at,
	COALESCE(disabled_at,0), COALESCE(last_used_at,0)`

func scanRegistrationCode(r rowScanner) (RegistrationCode, error) {
	var c RegistrationCode
	var roles string
	err := r.Scan(&c.ID, &c.CodeHash, &c.CodeHint, &c.Role, &roles, &c.MaxUses, &c.UsedCount,
		&c.ValidFrom, &c.ValidUntil, &c.Active, &c.Note, &c.CreatedBy, &c.CreatedAt,
		&c.UpdatedAt, &c.DisabledAt, &c.LastUsedAt)
	if err != nil {
		return c, err
	}
	c.Roles = splitStoredRoles(roles, c.Role)
	return c, nil
}

// InspectRegistrationCode validates a code without consuming a use. It is
// used by the two-step registration form to show only the roles this code
// allows.
func (s *Store) InspectRegistrationCode(ctx context.Context, raw string) (RegistrationCode, error) {
	norm := secure.NormalizeCode(raw)
	if norm == "" {
		return RegistrationCode{}, ErrCodeUnavailable
	}
	c, err := scanRegistrationCode(s.DB.QueryRowContext(ctx,
		`SELECT `+registrationCodeCols+` FROM registration_codes WHERE code_hash=?`,
		secure.HMACHex(s.RegistrationSecret(), norm)))
	if errors.Is(err, sql.ErrNoRows) {
		return RegistrationCode{}, ErrCodeUnavailable
	}
	if err != nil {
		return RegistrationCode{}, err
	}
	now := s.NowUnix()
	if !c.Active || now < c.ValidFrom || now > c.ValidUntil || c.UsedCount >= c.MaxUses {
		return RegistrationCode{}, ErrCodeUnavailable
	}
	return c, nil
}

// RegisterStaff creates a teacher/counselor/community account using an
// administrator-issued code. Any failure rolls the code usage back.
func (s *Store) RegisterStaff(ctx context.Context, in StaffRegisterInput, ip, ua, deviceHash string) (User, error) {
	if err := s.RegistrationAllowed(ctx, ip); err != nil {
		return User{}, err
	}
	if err := s.DeviceAllowed(ctx, deviceHash); err != nil {
		return User{}, err
	}
	normCode := secure.NormalizeCode(in.Code)
	hash := secure.HMACHex(s.RegistrationSecret(), normCode)
	displayName, usernameNorm, err := validate.NormalizeUsername(in.Username)
	if err != nil {
		s.recordRegistrationAttempt(ctx, nil, ip, ua, deviceHash, 0, "", 0, false, "用户名格式")
		return User{}, err
	}
	phone, err := validate.NormalizePhone(in.Phone)
	if err != nil {
		s.recordRegistrationAttempt(ctx, nil, ip, ua, deviceHash, 0, "", 0, false, "手机号格式")
		return User{}, err
	}
	if err := validate.ValidatePassword(in.Password, false); err != nil {
		s.recordRegistrationAttempt(ctx, nil, ip, ua, deviceHash, 0, "", 0, false, "密码格式")
		return User{}, err
	}
	now := s.NowUnix()
	var newUser User
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		code, err := scanRegistrationCode(tx.QueryRowContext(ctx,
			`SELECT `+registrationCodeCols+` FROM registration_codes WHERE code_hash=?`, hash))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrCodeUnavailable
		}
		if err != nil {
			return err
		}
		if !code.Active || now < code.ValidFrom || now > code.ValidUntil || code.UsedCount >= code.MaxUses {
			return ErrCodeUnavailable
		}
		allowed := code.AllowedRoles()
		chosen := strings.TrimSpace(in.Role)
		if chosen == "" {
			if len(allowed) != 1 {
				return errors.New("该注册码可用于多个角色，请选择注册角色")
			}
			chosen = allowed[0]
		}
		if !code.AllowsRole(chosen) {
			return errors.New("所选角色不在该注册码允许范围内，请返回上一步重新选择")
		}
		res, err := tx.ExecContext(ctx, `UPDATE registration_codes SET used_count=used_count+1, last_used_at=?, updated_at=?
			WHERE id=? AND active=1 AND used_count<max_uses AND valid_from<=? AND valid_until>=?`,
			now, now, code.ID, now, now)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrCodeUnavailable
		}
		if _, err := scanUser(tx.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE username_norm=? AND role<>'student'`, usernameNorm)); err == nil {
			return fmt.Errorf("用户名已被使用，可用：%s", strings.Join(validate.UsernameSuggestions(displayName), "、"))
		}
		var tmp int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE phone=?`, phone).Scan(&tmp); err == nil {
			return errors.New("该手机号已被使用，请直接登录或找回密码")
		}
		pwHash, err := secure.HashPassword(in.Password)
		if err != nil {
			return err
		}
		res, err = tx.ExecContext(ctx, `INSERT INTO users (phone,username,username_norm,password_hash,role,status,
			password_is_initial,registration_code_id,created_at,updated_at) VALUES (?,?,?,?,?, 'active', 0, ?,?,?)`,
			phone, displayName, usernameNorm, pwHash, chosen, code.ID, now, now)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return errors.New("用户名或手机号已被使用")
			}
			return err
		}
		id, _ := res.LastInsertId()
		newUser, err = scanUser(tx.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id=?`, id))
		if err != nil {
			return err
		}
		s.recordRegistrationAttempt(ctx, tx, ip, ua, deviceHash, code.ID, chosen, id, true, "成功")
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrCodeUnavailable) {
			_ = s.RecordLoginFailure(ctx, "reg_fail", ip, 10*time.Minute, 30*time.Minute, 10)
		}
		reason := err.Error()
		if len(reason) > 100 {
			reason = reason[:100]
		}
		s.recordRegistrationAttempt(ctx, nil, ip, ua, deviceHash, 0, strings.TrimSpace(in.Role), 0, false, reason)
		return User{}, err
	}
	return newUser, nil
}

// CreateRegistrationCode issues a new code and returns its one-time plaintext.
func (s *Store) CreateRegistrationCode(ctx context.Context, admin User, roles []string, maxUses int64, validFrom, validUntil int64, note, ip string) (RegistrationCode, string, error) {
	if admin.Role != "admin" {
		return RegistrationCode{}, "", errors.New("只有管理员可以创建注册码")
	}
	roles, err := normalizeStaffRoles(roles)
	if err != nil {
		return RegistrationCode{}, "", err
	}
	if maxUses < 1 || maxUses > 500 {
		return RegistrationCode{}, "", errors.New("可用次数需为 1–500")
	}
	now := s.NowUnix()
	if validFrom == 0 {
		validFrom = now
	}
	if validUntil == 0 {
		validUntil = now + 7*86400
	}
	if validUntil <= validFrom {
		return RegistrationCode{}, "", errors.New("失效时间必须晚于生效时间")
	}
	if validUntil > now+90*86400 {
		return RegistrationCode{}, "", errors.New("注册码最长有效期为 90 天")
	}
	var noteErr error
	note, noteErr = validate.ValidateRejectReason(note)
	if noteErr != nil {
		return RegistrationCode{}, "", noteErr
	}
	norm, display, err := secure.NewRegistrationCode()
	if err != nil {
		return RegistrationCode{}, "", err
	}
	hash := secure.HMACHex(s.RegistrationSecret(), norm)
	roleLabels := make([]string, 0, len(roles))
	for _, role := range roles {
		roleLabels = append(roleLabels, staffRoleLabel(role))
	}
	res, err := s.DB.ExecContext(ctx, `INSERT INTO registration_codes
		(code_hash,code_hint,role,roles,max_uses,used_count,valid_from,valid_until,active,note,created_by,created_at,updated_at)
		VALUES (?,?,?,?,?,0,?,?,1,?,?,?,?)`, hash, secure.CodeHint(norm), roles[0], strings.Join(roles, ","),
		maxUses, validFrom, validUntil, note, admin.ID, now, now)
	if err != nil {
		return RegistrationCode{}, "", err
	}
	id, _ := res.LastInsertId()
	_ = s.Audit(ctx, nil, admin, "regcode.create", "registration_code", id,
		fmt.Sprintf("角色 %s，次数 %d，有效期至 %s", strings.Join(roleLabels, "、"), maxUses,
			time.Unix(validUntil, 0).In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04")), ip)
	return s.GetRegistrationCode(ctx, id), display, nil
}

// GetRegistrationCode loads one code row.
func (s *Store) GetRegistrationCode(ctx context.Context, id int64) RegistrationCode {
	c, _ := scanRegistrationCode(s.DB.QueryRowContext(ctx,
		`SELECT `+registrationCodeCols+` FROM registration_codes WHERE id=?`, id))
	return c
}

// ListRegistrationCodes lists all codes with creator names.
func (s *Store) ListRegistrationCodes(ctx context.Context) ([]RegistrationCode, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT rc.id, rc.code_hash, rc.code_hint, rc.role, rc.roles, rc.max_uses, rc.used_count,
		rc.valid_from, rc.valid_until, rc.active, rc.note, COALESCE(rc.created_by,0), rc.created_at, rc.updated_at,
		COALESCE(rc.disabled_at,0), COALESCE(rc.last_used_at,0), COALESCE(u.username,'')
		FROM registration_codes rc LEFT JOIN users u ON u.id=rc.created_by ORDER BY rc.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RegistrationCode
	for rows.Next() {
		var c RegistrationCode
		var roles string
		if err := rows.Scan(&c.ID, &c.CodeHash, &c.CodeHint, &c.Role, &roles, &c.MaxUses, &c.UsedCount,
			&c.ValidFrom, &c.ValidUntil, &c.Active, &c.Note, &c.CreatedBy, &c.CreatedAt, &c.UpdatedAt,
			&c.DisabledAt, &c.LastUsedAt, &c.CreatorName); err != nil {
			return nil, err
		}
		c.Roles = splitStoredRoles(roles, c.Role)
		out = append(out, c)
	}
	return out, rows.Err()
}

// UpdateRegistrationCode extends validity, raises max uses, or disables it.
func (s *Store) UpdateRegistrationCode(ctx context.Context, admin User, id int64, newMax int64, newUntil int64, active *bool, ip string) error {
	if admin.Role != "admin" {
		return errors.New("只有管理员可以修改注册码")
	}
	c := s.GetRegistrationCode(ctx, id)
	if c.ID == 0 {
		return ErrNotFound
	}
	now := s.NowUnix()
	if newMax > 0 {
		if newMax < c.UsedCount {
			return fmt.Errorf("可用次数不能低于已用次数（%d）", c.UsedCount)
		}
		if newMax > 500 {
			return errors.New("可用次数不能超过 500")
		}
		c.MaxUses = newMax
	}
	if newUntil > 0 {
		if newUntil <= now {
			return errors.New("失效时间必须晚于当前时间")
		}
		if newUntil > now+90*86400 {
			return errors.New("注册码最长有效期为 90 天")
		}
		c.ValidUntil = newUntil
	}
	if active != nil {
		c.Active = *active
		if !*active {
			c.DisabledAt = now
		}
	}
	forUpdate := `UPDATE registration_codes SET max_uses=?, valid_until=?, active=?, disabled_at=?, updated_at=? WHERE id=?`
	var disabled any
	if c.DisabledAt > 0 {
		disabled = c.DisabledAt
	}
	if _, err := s.DB.ExecContext(ctx, forUpdate, c.MaxUses, c.ValidUntil, boolInt(c.Active), disabled, now, id); err != nil {
		return err
	}
	return s.Audit(ctx, nil, admin, "regcode.update", "registration_code", id,
		fmt.Sprintf("次数 %d，有效期至 %d，启用 %v", c.MaxUses, c.ValidUntil, c.Active), ip)
}

// RegistrationAttemptRow is one audit row for the security page.
type RegistrationAttemptRow struct {
	ID        int64
	IP        string
	Role      string
	Success   bool
	Reason    string
	CreatedAt int64
}

// ListRegistrationAttempts returns recent attempts.
func (s *Store) ListRegistrationAttempts(ctx context.Context, limit int) ([]RegistrationAttemptRow, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id,ip,role,success,reason,created_at FROM registration_attempts
		ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RegistrationAttemptRow
	for rows.Next() {
		var r RegistrationAttemptRow
		if err := rows.Scan(&r.ID, &r.IP, &r.Role, &r.Success, &r.Reason, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListLockedIPs returns currently locked login counters.
func (s *Store) ListLockedIPs(ctx context.Context) ([]struct {
	Key     string
	Scope   string
	Until   int64
	Count   int64
	Updated int64
}, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT key, scope, COALESCE(locked_until,0), failed_count, updated_at
		FROM login_attempts WHERE locked_until>? ORDER BY locked_until DESC`, s.NowUnix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []struct {
		Key     string
		Scope   string
		Until   int64
		Count   int64
		Updated int64
	}
	for rows.Next() {
		var r struct {
			Key     string
			Scope   string
			Until   int64
			Count   int64
			Updated int64
		}
		if err := rows.Scan(&r.Key, &r.Scope, &r.Until, &r.Count, &r.Updated); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
