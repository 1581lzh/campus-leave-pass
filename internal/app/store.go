package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"leavepass/internal/config"
	"leavepass/internal/secure"
	"leavepass/internal/validate"
	_ "modernc.org/sqlite"
)

// Store owns the database connection and all persistence.
type Store struct {
	DB  *sql.DB
	cfg config.Config
}

// Open opens (and migrates) the SQLite database, then seeds the initial admin
// when no administrator exists yet.
func Open(cfg config.Config) (*Store, error) {
	path := cfg.DBPath
	if path != ":memory:" && !strings.HasPrefix(path, "file:") {
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
			return nil, err
		}
		path = abs
	}
	dsn := "file:" + path + "?_txlock=immediate" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	s := &Store{DB: db, cfg: cfg}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("数据库迁移失败: %w", err)
	}
	if path != ":memory:" && !strings.HasPrefix(cfg.DBPath, "file:") {
		_ = os.Chmod(path, 0o600)
	}
	if err := s.seedAdmin(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.DB.Close() }

// Now returns the injected clock time.
func (s *Store) Now() time.Time {
	if s.cfg.Now != nil {
		return s.cfg.Now()
	}
	return time.Now()
}

// NowUnix returns epoch seconds.
func (s *Store) NowUnix() int64 { return s.Now().Unix() }

// BaseURL is the externally visible origin, used for QR payloads.
func (s *Store) BaseURL() string { return s.cfg.BaseURL }

// CookieSecure reports whether cookies must carry the Secure attribute.
func (s *Store) CookieSecure() bool { return s.cfg.CookieSecure }

// CampusBuildings is the optional seed list for community building choices.
func (s *Store) CampusBuildings() []string { return s.cfg.CampusBuildings }

// SessionSecret is used to derive CSRF tokens.
func (s *Store) SessionSecret() []byte { return s.cfg.SessionSecret }

// RegistrationSecret keys registration-code HMACs.
func (s *Store) RegistrationSecret() []byte { return s.cfg.RegistrationSecret }

// TodayCST returns the current CST date string YYYY-MM-DD.
func (s *Store) TodayCST() string { return s.Now().In(config.Location).Format("2006-01-02") }

// StartOfDayCST returns CST midnight for t.
func StartOfDayCST(t time.Time) time.Time {
	t = t.In(config.Location)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, config.Location)
}

// NextMidnightCST returns the following CST midnight.
func NextMidnightCST(t time.Time) time.Time { return StartOfDayCST(t).AddDate(0, 0, 1) }

// StartOfWeekCST returns Monday 00:00 CST of t's week.
func StartOfWeekCST(t time.Time) time.Time {
	d := StartOfDayCST(t)
	wd := int(d.Weekday())
	if wd == 0 {
		wd = 7
	}
	return d.AddDate(0, 0, -(wd - 1))
}

// ParseCSTDate parses YYYY-MM-DD into the start of that CST day.
func ParseCSTDate(s string) (time.Time, error) {
	return time.ParseInLocation("2006-01-02", s, config.Location)
}

// DateBounds converts an inclusive CST date range into [start,end) epoch
// seconds. Empty bounds are open.
func DateBounds(from, to string) (int64, int64, error) {
	var start, end int64
	if from != "" {
		t, err := ParseCSTDate(from)
		if err != nil {
			return 0, 0, errors.New("开始日期格式应为 YYYY-MM-DD")
		}
		start = t.Unix()
	}
	if to != "" {
		t, err := ParseCSTDate(to)
		if err != nil {
			return 0, 0, errors.New("结束日期格式应为 YYYY-MM-DD")
		}
		end = t.AddDate(0, 0, 1).Unix()
	}
	return start, end, nil
}

func (s *Store) seedAdmin() error {
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM users WHERE role='admin'`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	if s.cfg.AdminInitialPhone == "" {
		return errors.New("首次部署必须设置 ADMIN_INITIAL_PHONE（管理员初始手机号）")
	}
	phone, err := validate.NormalizePhone(s.cfg.AdminInitialPhone)
	if err != nil {
		return fmt.Errorf("ADMIN_INITIAL_PHONE 无效: %w", err)
	}
	name, login, err := validate.NormalizeUsername(s.cfg.AdminInitialName)
	if err != nil {
		return fmt.Errorf("ADMIN_INITIAL_NAME 无效: %w", err)
	}
	hash, err := secure.HashPassword(s.cfg.AdminInitialPass)
	if err != nil {
		return err
	}
	now := s.NowUnix()
	_, err = s.DB.Exec(`INSERT INTO users (phone, username, username_norm, password_hash, role, status, password_is_initial, created_at, updated_at)
		VALUES (?,?,?,?, 'admin', 'active', 1, ?, ?)`, phone, name, login, hash, now, now)
	if err != nil {
		return fmt.Errorf("创建初始管理员失败: %w", err)
	}
	return nil
}

// queryer is satisfied by *sql.DB and *sql.Tx.
type queryer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// withTx runs fn inside BEGIN IMMEDIATE (configured on the connection).
func (s *Store) withTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// User is a row of users with nullable columns flattened.
type User struct {
	ID                 int64
	Phone              string
	Username           string
	UsernameNorm       string
	StudentNo          string
	StudentNoNorm      string
	PasswordHash       string
	Role               string
	Status             string
	PasswordIsInitial  bool
	RegistrationCodeID int64
	LastLoginAt        int64
	DeletedAt          int64
	PurgeAfter         int64
	DeleteReason       string
	CreatedBy          int64
	CreatedAt          int64
	UpdatedAt          int64
}

// Active reports whether the account can act normally.
func (u User) Active() bool { return u.Status == "active" }

// ReadOnlyHistory reports whether the account is in the 7-day soft-delete
// read-only window.
func (u User) ReadOnlyHistory(now int64) bool {
	return u.Status == "soft_deleted" && u.PurgeAfter > now
}

// IsStudent reports the role.
func (u User) IsStudent() bool { return u.Role == "student" }

// IsStaff reports teacher/counselor/community.
func (u User) IsStaff() bool {
	return u.Role == "teacher" || u.Role == "counselor" || u.Role == "community"
}

const userCols = `id, COALESCE(phone,''), username, username_norm, COALESCE(student_no,''), COALESCE(student_no_norm,''),
	password_hash, role, status, password_is_initial, COALESCE(registration_code_id,0),
	COALESCE(last_login_at,0), COALESCE(deleted_at,0), COALESCE(purge_after,0), delete_reason,
	COALESCE(created_by,0), created_at, updated_at`

type rowScanner interface{ Scan(dest ...any) error }

func scanUser(r rowScanner) (User, error) {
	var u User
	err := r.Scan(&u.ID, &u.Phone, &u.Username, &u.UsernameNorm, &u.StudentNo, &u.StudentNoNorm,
		&u.PasswordHash, &u.Role, &u.Status, &u.PasswordIsInitial, &u.RegistrationCodeID,
		&u.LastLoginAt, &u.DeletedAt, &u.PurgeAfter, &u.DeleteReason, &u.CreatedBy, &u.CreatedAt, &u.UpdatedAt)
	return u, err
}

// Session is an authenticated browser session.
type Session struct {
	ID        int64
	TokenHash string
	UserID    int64
	ExpiresAt int64
	CreatedAt int64
	IP        string
	UserAgent string
}

// Class is a class row plus joined teacher/counselor names.
type Class struct {
	ID                int64
	Name              string
	NameNorm          string
	Department        string
	TeacherUserID     int64
	CounselorUserID   int64
	FormerTeacherID   int64
	UnclaimedAt       int64
	DeleteDueAt       int64
	Reminded3dAt      int64
	Reminded1dAt      int64
	Version           int64
	CreatedBy         int64
	CreatedAt         int64
	UpdatedAt         int64
	TeacherName       string
	CounselorName     string
	FormerTeacherName string
	StudentCount      int64
}

// Unclaimed reports whether the class has no teacher.
func (c Class) Unclaimed() bool { return c.TeacherUserID == 0 }

// Leave is a leave request row.
type Leave struct {
	ID              int64
	StudentUserID   int64
	StudentName     string
	StudentNo       string
	ContactPhone    string
	ClassID         int64
	ArchivedClassID int64
	ClassName       string
	Department      string
	TeacherUserID   int64
	TeacherName     string
	CounselorUserID int64
	CounselorName   string
	DormBuilding    string
	DormRoom        string
	Reason          string
	StartAt         int64
	EndAt           int64
	Status          string
	Decision        string
	DecidedBy       int64
	DecidedVia      string
	ApproverName    string
	MajorOpinion    string
	DeptOpinion     string
	RejectReason    string
	CancelReason    string
	DeadlineAt      int64
	DecidedAt       int64
	CreatedAt       int64
	ArchivedAt      int64
	RetainUntil     int64
	CreatedBy       int64
}

// Dorm returns 楼栋-房间.
func (l Leave) Dorm() string { return l.DormBuilding + "-" + l.DormRoom }

// Notification is an in-app notification.
type Notification struct {
	ID        int64
	UserID    int64
	Kind      string
	Title     string
	Body      string
	Link      string
	LeaveID   int64
	ReadAt    int64
	CreatedAt int64
}

// RegistrationCode is an admin-issued staff registration code.
type RegistrationCode struct {
	ID          int64
	CodeHash    string
	CodeHint    string
	Role        string
	Roles       []string
	MaxUses     int64
	UsedCount   int64
	ValidFrom   int64
	ValidUntil  int64
	Active      bool
	Note        string
	CreatedBy   int64
	CreatedAt   int64
	UpdatedAt   int64
	DisabledAt  int64
	LastUsedAt  int64
	CreatorName string
}

// AllowedRoles returns the staff roles this code may register. Older rows
// migrated from the single-role schema fall back to Role.
func (rc RegistrationCode) AllowedRoles() []string {
	if len(rc.Roles) > 0 {
		return rc.Roles
	}
	if rc.Role != "" {
		return []string{rc.Role}
	}
	return nil
}

// AllowsRole reports whether the code permits the given staff role.
func (rc RegistrationCode) AllowsRole(role string) bool {
	for _, r := range rc.AllowedRoles() {
		if r == role {
			return true
		}
	}
	return false
}

// Status renders the derived lifecycle state.
func (rc RegistrationCode) Status(now int64) string {
	switch {
	case !rc.Active:
		return "已停用"
	case now < rc.ValidFrom:
		return "未生效"
	case now > rc.ValidUntil:
		return "已过期"
	case rc.UsedCount >= rc.MaxUses:
		return "已用完"
	default:
		return "可用"
	}
}

// ArchivedClass is a snapshot of a cleaned-up class.
type ArchivedClass struct {
	ID                int64
	SourceClassID     int64
	ClassName         string
	Department        string
	LastTeacherUserID int64
	LastTeacherName   string
	LastCounselorID   int64
	LastCounselorName string
	CleanedAt         int64
	RetainUntil       int64
	StudentCount      int64
	LeaveCount        int64
	Reason            string
	CreatedAt         int64
}

// ImportBatch records one student import attempt.
type ImportBatch struct {
	ID           int64
	ClassID      int64
	ImportedBy   int64
	FileName     string
	TotalRows    int64
	CreatedCount int64
	ErrorCount   int64
	Status       string
	IP           string
	CreatedAt    int64
	ClassName    string
	ImporterName string
}

// AuditLog is an admin action record.
type AuditLog struct {
	ID               int64
	ActorUserID      int64
	ActorName        string
	Action           string
	TargetType       string
	TargetID         int64
	TargetClassID    int64
	TargetClassName  string
	TargetDepartment string
	TargetBuilding   string
	Detail           string
	IP               string
	CreatedAt        int64
}

// StudentClass is a student's class relation.
type StudentClass struct {
	UserID        int64
	ClassID       int64
	DormBuilding  string
	DormRoom      string
	DormUpdatedBy int64
	DormUpdatedAt int64
	ImportedBy    int64
	ImportedAt    int64
	JoinedAt      int64
}
