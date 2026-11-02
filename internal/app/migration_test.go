package app

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

const oldAuditSchema = `
CREATE TABLE users (id INTEGER PRIMARY KEY AUTOINCREMENT);
CREATE TABLE leave_requests (id INTEGER PRIMARY KEY AUTOINCREMENT);
CREATE TABLE registration_codes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    role TEXT NOT NULL
);
INSERT INTO registration_codes (role) VALUES ('teacher');
CREATE TABLE login_attempts (
    key TEXT NOT NULL,
    scope TEXT NOT NULL,
    window_start INTEGER NOT NULL,
    failed_count INTEGER NOT NULL DEFAULT 0,
    locked_until INTEGER,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (key, scope)
);
CREATE TABLE admin_audit_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    actor_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
    actor_name TEXT NOT NULL DEFAULT '',
    action TEXT NOT NULL,
    target_type TEXT NOT NULL DEFAULT '',
    target_id INTEGER,
    detail TEXT NOT NULL DEFAULT '',
    ip TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
);
INSERT INTO admin_audit_logs (actor_name,action,created_at) VALUES ('旧管理员','audit.old',1);
`

func TestMigrateAddsRegistrationRoles(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "v4.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`
CREATE TABLE registration_codes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    role TEXT NOT NULL
);
INSERT INTO registration_codes (role) VALUES ('counselor');
CREATE TABLE admin_audit_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    target_class_id INTEGER,
    target_class_name TEXT NOT NULL DEFAULT '',
    target_department TEXT NOT NULL DEFAULT '',
    target_building TEXT NOT NULL DEFAULT ''
);
PRAGMA user_version = 4;`); err != nil {
		t.Fatalf("create v4 schema: %v", err)
	}
	s := &Store{DB: db}
	if err := s.migrate(); err != nil {
		t.Fatalf("migrate v4: %v", err)
	}
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 5 {
		t.Fatalf("user_version = %d err=%v，期望 5", version, err)
	}
	var role, roles string
	if err := db.QueryRow(`SELECT role, roles FROM registration_codes WHERE id=1`).Scan(&role, &roles); err != nil {
		t.Fatalf("读取注册码: %v", err)
	}
	if role != "counselor" || roles != "counselor" {
		t.Fatalf("role/roles = %q/%q，期望 counselor/counselor", role, roles)
	}
}

func TestMigrateUpgradesOldAuditSchema(t *testing.T) {
	for _, oldVersion := range []int{1, 2, 3} {
		t.Run(fmt.Sprintf("v%d", oldVersion), func(t *testing.T) {
			db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "old.db"))
			if err != nil {
				t.Fatalf("open old db: %v", err)
			}
			defer db.Close()
			if _, err := db.Exec(oldAuditSchema); err != nil {
				t.Fatalf("create old schema: %v", err)
			}
			if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, oldVersion)); err != nil {
				t.Fatalf("set old user_version: %v", err)
			}
			s := &Store{DB: db}
			if err := s.migrate(); err != nil {
				t.Fatalf("migrate v%d: %v", oldVersion, err)
			}
			var version int
			if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 5 {
				t.Fatalf("user_version = %d err=%v，期望 5", version, err)
			}
			var roles string
			if err := db.QueryRow(`SELECT roles FROM registration_codes WHERE id=1`).Scan(&roles); err != nil || roles != "teacher" {
				t.Fatalf("registration_codes.roles = %q err=%v，期望 teacher", roles, err)
			}
			for _, col := range []string{"target_class_id", "target_class_name", "target_department", "target_building"} {
				if !tableHasColumn(t, db, "admin_audit_logs", col) {
					t.Fatalf("admin_audit_logs 缺少列 %s", col)
				}
			}
			var classID sql.NullInt64
			var className, department, building string
			if err := db.QueryRow(`SELECT target_class_id,target_class_name,target_department,target_building
				FROM admin_audit_logs WHERE action='audit.old'`).Scan(&classID, &className, &department, &building); err != nil {
				t.Fatalf("读取旧审计行: %v", err)
			}
			if classID.Valid || className != "" || department != "" || building != "" {
				t.Fatalf("旧审计行快照 = %v/%q/%q/%q", classID, className, department, building)
			}
			for _, idx := range []string{"idx_audit_target_class", "idx_audit_target_department", "idx_audit_target_building"} {
				var n int
				if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, idx).Scan(&n); err != nil || n != 1 {
					t.Fatalf("索引 %s 缺失: n=%d err=%v", idx, n, err)
				}
			}
			if oldVersion == 1 && !tableHasColumn(t, db, "leave_requests", "created_by") {
				t.Fatal("v1 升级后 leave_requests 缺少 created_by")
			}
		})
	}
}

func tableHasColumn(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatalf("table_info(%s): %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("table_info scan: %v", err)
		}
		if name == column {
			return true
		}
	}
	return false
}
