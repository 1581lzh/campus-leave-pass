package app

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"leavepass/internal/config"
	"leavepass/internal/secure"
	"leavepass/internal/validate"
)

// testClock is a concurrency-safe injectable clock. Tests never depend on the
// real wall clock.
type testClock struct {
	mu   sync.Mutex
	unix int64
}

func newTestClock(t time.Time) *testClock { return &testClock{unix: t.Unix()} }

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Unix(c.unix, 0).In(config.Location)
}

func (c *testClock) Unix() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.unix
}

func (c *testClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.unix = t.Unix()
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.unix += int64(d.Seconds())
}

const (
	testPassword      = "Passw0rd!"
	testSessionSecret = "0123456789abcdef0123456789abcdef"
	testRegSecret     = "abcdef0123456789abcdef0123456789"
	testAdminPhone    = "13800000000"
)

func testConfig(dbPath string, clock *testClock) config.Config {
	return config.Config{
		BaseURL:            "http://leave.test",
		ListenAddr:         "127.0.0.1:0",
		DBPath:             dbPath,
		SessionSecret:      []byte(testSessionSecret),
		RegistrationSecret: []byte(testRegSecret),
		AdminInitialName:   "Admin",
		AdminInitialPass:   "AA",
		AdminInitialPhone:  testAdminPhone,
		CampusBuildings:    []string{"21", "22"},
		CookieSecure:       false,
		Now:                clock.Now,
	}
}

// newTestStore creates an isolated SQLite store seeded with one administrator.
func newTestStore(t *testing.T) (*Store, *testClock, User) {
	t.Helper()
	clock := newTestClock(time.Date(2026, 3, 15, 10, 0, 0, 0, config.Location))
	cfg := testConfig(filepath.Join(t.TempDir(), "leavepass.db"), clock)
	s, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open test store: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Logf("close test store: %v", err)
		}
	})
	admin, err := s.GetUserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatalf("load seeded admin: %v", err)
	}
	return s, clock, admin
}

func ctx() context.Context { return context.Background() }

func mustHash(t *testing.T, pw string) string {
	t.Helper()
	h, err := secure.HashPassword(pw)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	return h
}

// addStaff inserts a staff account directly because the only production path
// is registration-code based (covered separately). It returns the stored row.
func addStaff(t *testing.T, s *Store, role, username, phone string) User {
	t.Helper()
	if role != "teacher" && role != "counselor" && role != "community" && role != "admin" {
		t.Fatalf("addStaff: 非法角色 %q", role)
	}
	display, login, err := validate.NormalizeUsername(username)
	if err != nil {
		t.Fatalf("addStaff username: %v", err)
	}
	p, err := validate.NormalizePhone(phone)
	if err != nil {
		t.Fatalf("addStaff phone: %v", err)
	}
	now := s.NowUnix()
	hash := mustHash(t, testPassword)
	res, err := s.DB.Exec(`INSERT INTO users
		(phone,username,username_norm,password_hash,role,status,password_is_initial,created_at,updated_at)
		VALUES (?,?,?,?,?, 'active', 0, ?,?)`,
		p, display, login, hash, role, now, now)
	if err != nil {
		t.Fatalf("addStaff insert: %v", err)
	}
	id, _ := res.LastInsertId()
	u, err := s.GetUserByID(ctx(), id)
	if err != nil {
		t.Fatalf("addStaff reload: %v", err)
	}
	return u
}

// addAdmin creates another administrator (needed for reset approval tests).
func addAdmin(t *testing.T, s *Store, admin User, username, phone string) User {
	t.Helper()
	u, err := s.CreateAdmin(ctx(), admin, username, phone, "AA", "127.0.0.1")
	if err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	return u
}

// addStudent inserts a student and optional class relation/dorm. There is no
// single-student production API (students are created by XLSX import), so the
// helper writes the same rows the importer writes.
func addStudent(t *testing.T, s *Store, name, studentNo string, classID int64, dorm string) User {
	t.Helper()
	display, norm, err := validate.NormalizeStudentNo(studentNo)
	if err != nil {
		t.Fatalf("addStudent no: %v", err)
	}
	if _, err := validate.NormalizeName(name); err != nil {
		t.Fatalf("addStudent name: %v", err)
	}
	now := s.NowUnix()
	hash := mustHash(t, testPassword)
	res, err := s.DB.Exec(`INSERT INTO users
		(username,username_norm,student_no,student_no_norm,password_hash,role,status,password_is_initial,created_at,updated_at)
		VALUES (?,?,?,?,?, 'student', 'active', 1, ?,?)`,
		name, strings.ToLower(name), display, norm, hash, now, now)
	if err != nil {
		t.Fatalf("addStudent insert: %v", err)
	}
	id, _ := res.LastInsertId()
	if classID != 0 {
		building, room := "", ""
		if strings.TrimSpace(dorm) != "" {
			building, room, err = validate.SplitDorm(dorm)
			if err != nil {
				t.Fatalf("addStudent dorm: %v", err)
			}
		}
		if _, err := s.DB.Exec(`INSERT INTO student_classes
			(user_id,class_id,dorm_building,dorm_room,imported_at,joined_at) VALUES (?,?,?,?,?,?)`,
			id, classID, building, room, now, now); err != nil {
			t.Fatalf("addStudent class relation: %v", err)
		}
	}
	u, err := s.GetUserByID(ctx(), id)
	if err != nil {
		t.Fatalf("addStudent reload: %v", err)
	}
	return u
}

// makeClass creates a class through the production service and optionally
// assigns a counselor. At least one of teacher/counselor must be non-zero.
func makeClass(t *testing.T, s *Store, admin, teacher, counselor User, name string) Class {
	t.Helper()
	var actor User
	switch {
	case teacher.ID != 0:
		actor = teacher
	case counselor.ID != 0:
		actor = counselor
	default:
		t.Fatal("makeClass: teacher/counselor 至少需要一个")
	}
	c, err := s.CreateClass(ctx(), actor, name, "计算机系", false)
	if err != nil {
		t.Fatalf("CreateClass(%s): %v", name, err)
	}
	if teacher.ID != 0 && counselor.ID != 0 {
		if err := s.AssignCounselor(ctx(), admin, c.ID, counselor.ID, "127.0.0.1"); err != nil {
			t.Fatalf("AssignCounselor(%s): %v", name, err)
		}
	}
	c, err = s.GetClass(ctx(), c.ID)
	if err != nil {
		t.Fatalf("GetClass(%s): %v", name, err)
	}
	return c
}

func countRows(t *testing.T, s *Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.DB.QueryRow(query, args...).Scan(&n); err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("count query: %v", err)
	}
	return n
}

func countNotifications(t *testing.T, s *Store, userID int64, kind string) int {
	t.Helper()
	return countRows(t, s, `SELECT COUNT(*) FROM notifications WHERE user_id=? AND kind=?`, userID, kind)
}

func mustGetLeave(t *testing.T, s *Store, id int64) Leave {
	t.Helper()
	l, err := s.GetLeave(ctx(), id)
	if err != nil {
		t.Fatalf("GetLeave(%d): %v", id, err)
	}
	return l
}

func TestDateBoundsAndCSTBoundaries(t *testing.T) {
	start, end, err := DateBounds("2026-03-15", "2026-03-16")
	if err != nil {
		t.Fatalf("DateBounds: %v", err)
	}
	wantStart, _ := ParseCSTDate("2026-03-15")
	wantEnd, _ := ParseCSTDate("2026-03-17")
	if start != wantStart.Unix() || end != wantEnd.Unix() {
		t.Fatalf("DateBounds = (%d,%d)，期望 (%d,%d)", start, end, wantStart.Unix(), wantEnd.Unix())
	}
	if s, e, err := DateBounds("", ""); err != nil || s != 0 || e != 0 {
		t.Fatalf("空日期范围 = (%d,%d,%v)", s, e, err)
	}
	if _, _, err := DateBounds("2026/03/15", ""); err == nil {
		t.Fatal("非法日期格式应报错")
	}
	if _, _, err := DateBounds("", "2026-13-40"); err == nil {
		t.Fatal("非法结束日期应报错")
	}

	tm := time.Date(2026, 3, 15, 23, 59, 0, 0, config.Location)
	if got := StartOfDayCST(tm).Format("2006-01-02 15:04"); got != "2026-03-15 00:00" {
		t.Fatalf("StartOfDayCST = %s", got)
	}
	if got := NextMidnightCST(tm).Format("2006-01-02 15:04"); got != "2026-03-16 00:00" {
		t.Fatalf("NextMidnightCST = %s", got)
	}
	// 2026-03-15 is a Sunday; the week starts Monday 2026-03-09.
	if got := StartOfWeekCST(tm).Format("2006-01-02 15:04"); got != "2026-03-09 00:00" {
		t.Fatalf("StartOfWeekCST = %s", got)
	}
}
