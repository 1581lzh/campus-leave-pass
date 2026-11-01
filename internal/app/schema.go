package app

// schemaV1 is the complete initial schema. All timestamps are UTC epoch
// seconds; display and date boundaries use Asia/Shanghai.
const schemaV1 = `
CREATE TABLE registration_codes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    code_hash TEXT NOT NULL UNIQUE,
    code_hint TEXT NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('teacher','counselor','community')),
    max_uses INTEGER NOT NULL,
    used_count INTEGER NOT NULL DEFAULT 0,
    valid_from INTEGER NOT NULL,
    valid_until INTEGER NOT NULL,
    active INTEGER NOT NULL DEFAULT 1,
    note TEXT NOT NULL DEFAULT '',
    created_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    disabled_at INTEGER,
    last_used_at INTEGER
);
CREATE INDEX idx_regcodes_role ON registration_codes(role);
CREATE INDEX idx_regcodes_valid_until ON registration_codes(valid_until);
CREATE INDEX idx_regcodes_created_by ON registration_codes(created_by);
CREATE INDEX idx_regcodes_active ON registration_codes(active);

CREATE TABLE users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    phone TEXT,
    username TEXT NOT NULL,
    username_norm TEXT NOT NULL,
    student_no TEXT,
    student_no_norm TEXT,
    password_hash TEXT NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('student','teacher','counselor','community','admin')),
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','soft_deleted')),
    password_is_initial INTEGER NOT NULL DEFAULT 0,
    registration_code_id INTEGER REFERENCES registration_codes(id) ON DELETE SET NULL,
    last_login_at INTEGER,
    deleted_at INTEGER,
    purge_after INTEGER,
    delete_reason TEXT NOT NULL DEFAULT '',
    created_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    CHECK ((role = 'student' AND student_no IS NOT NULL AND phone IS NULL)
        OR (role <> 'student' AND phone IS NOT NULL AND student_no IS NULL))
);
CREATE UNIQUE INDEX idx_users_phone ON users(phone);
CREATE UNIQUE INDEX idx_users_student_no ON users(student_no_norm);
CREATE UNIQUE INDEX idx_users_username_staff ON users(username_norm) WHERE role <> 'student';
CREATE INDEX idx_users_role_status ON users(role, status);
CREATE INDEX idx_users_purge_after ON users(purge_after);

CREATE TABLE sessions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    token_hash TEXT NOT NULL UNIQUE,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    ip TEXT NOT NULL DEFAULT '',
    user_agent TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_sessions_user ON sessions(user_id);
CREATE INDEX idx_sessions_expires ON sessions(expires_at);

CREATE TABLE classes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    name_norm TEXT NOT NULL UNIQUE,
    department TEXT NOT NULL,
    teacher_user_id INTEGER UNIQUE REFERENCES users(id) ON DELETE SET NULL,
    counselor_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
    former_teacher_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
    unclaimed_at INTEGER,
    delete_due_at INTEGER,
    reminded_3d_at INTEGER,
    reminded_1d_at INTEGER,
    version INTEGER NOT NULL DEFAULT 1,
    created_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX idx_classes_counselor ON classes(counselor_user_id);
CREATE INDEX idx_classes_delete_due ON classes(delete_due_at);

CREATE TABLE student_classes (
    user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    class_id INTEGER NOT NULL REFERENCES classes(id) ON DELETE CASCADE,
    dorm_building TEXT NOT NULL,
    dorm_room TEXT NOT NULL,
    dorm_updated_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
    dorm_updated_at INTEGER,
    imported_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
    imported_at INTEGER NOT NULL,
    joined_at INTEGER NOT NULL
);
CREATE INDEX idx_student_classes_class ON student_classes(class_id);
CREATE INDEX idx_student_classes_imported_by ON student_classes(imported_by);
CREATE INDEX idx_student_classes_building ON student_classes(dorm_building);

CREATE TABLE class_counselor_requests (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    class_id INTEGER NOT NULL REFERENCES classes(id) ON DELETE CASCADE,
    counselor_user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    invited_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
    replaces_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','accepted','declined','cancelled')),
    note TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    responded_at INTEGER
);
CREATE UNIQUE INDEX idx_ccr_one_pending ON class_counselor_requests(class_id) WHERE status = 'pending';
CREATE INDEX idx_ccr_class_status ON class_counselor_requests(class_id, status);
CREATE INDEX idx_ccr_counselor_status ON class_counselor_requests(counselor_user_id, status);

CREATE TABLE student_dorm_edits (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    student_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
    student_name_snapshot TEXT NOT NULL,
    student_no_snapshot TEXT NOT NULL,
    class_id INTEGER REFERENCES classes(id) ON DELETE SET NULL,
    class_name_snapshot TEXT NOT NULL DEFAULT '',
    old_building TEXT NOT NULL,
    old_room TEXT NOT NULL,
    new_building TEXT NOT NULL,
    new_room TEXT NOT NULL,
    edited_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
    editor_name_snapshot TEXT NOT NULL,
    editor_role TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
);
CREATE INDEX idx_dorm_edits_student ON student_dorm_edits(student_user_id, created_at);
CREATE INDEX idx_dorm_edits_editor ON student_dorm_edits(edited_by, created_at);
CREATE INDEX idx_dorm_edits_class ON student_dorm_edits(class_id, created_at);

CREATE TABLE archived_classes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    source_class_id INTEGER NOT NULL,
    class_name TEXT NOT NULL,
    department TEXT NOT NULL,
    last_teacher_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
    last_teacher_name TEXT NOT NULL DEFAULT '',
    last_counselor_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
    last_counselor_name TEXT NOT NULL DEFAULT '',
    cleaned_at INTEGER NOT NULL,
    retain_until INTEGER NOT NULL,
    student_count INTEGER NOT NULL DEFAULT 0,
    leave_count INTEGER NOT NULL DEFAULT 0,
    reason TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
);
CREATE INDEX idx_archived_teacher ON archived_classes(last_teacher_user_id);
CREATE INDEX idx_archived_counselor ON archived_classes(last_counselor_user_id);
CREATE INDEX idx_archived_retain ON archived_classes(retain_until);
CREATE INDEX idx_archived_class_name ON archived_classes(class_name);

CREATE TABLE community_buildings (
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    building TEXT NOT NULL,
    PRIMARY KEY (user_id, building)
);

CREATE TABLE leave_requests (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    student_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
    student_name TEXT NOT NULL,
    student_no TEXT NOT NULL,
    contact_phone TEXT NOT NULL DEFAULT '',
    class_id INTEGER REFERENCES classes(id) ON DELETE SET NULL,
    archived_class_id INTEGER REFERENCES archived_classes(id) ON DELETE SET NULL,
    class_name TEXT NOT NULL,
    department TEXT NOT NULL,
    teacher_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
    teacher_name TEXT NOT NULL DEFAULT '',
    counselor_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
    counselor_name TEXT NOT NULL DEFAULT '',
    dorm_building TEXT NOT NULL,
    dorm_room TEXT NOT NULL,
    reason TEXT NOT NULL,
    start_at INTEGER NOT NULL,
    end_at INTEGER NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending','approved','rejected','cancelled')),
    decision TEXT NOT NULL DEFAULT '',
    decided_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
    decided_via TEXT NOT NULL DEFAULT '',
    approver_name TEXT NOT NULL DEFAULT '',
    major_opinion TEXT NOT NULL DEFAULT '',
    dept_opinion TEXT NOT NULL DEFAULT '',
    reject_reason TEXT NOT NULL DEFAULT '',
    cancel_reason TEXT NOT NULL DEFAULT '',
    deadline_at INTEGER NOT NULL,
    decided_at INTEGER,
    created_at INTEGER NOT NULL,
    archived_at INTEGER,
    retain_until INTEGER,
    created_by INTEGER REFERENCES users(id) ON DELETE SET NULL
);
CREATE INDEX idx_leaves_status_deadline ON leave_requests(status, deadline_at);
CREATE INDEX idx_leaves_created ON leave_requests(created_at);
CREATE INDEX idx_leaves_student_created ON leave_requests(student_user_id, created_at);
CREATE INDEX idx_leaves_studentno_created ON leave_requests(student_no, created_at);
CREATE INDEX idx_leaves_class_created ON leave_requests(class_id, created_at);
CREATE INDEX idx_leaves_archived_class ON leave_requests(archived_class_id, created_at);
CREATE INDEX idx_leaves_building_start ON leave_requests(dorm_building, start_at);
CREATE INDEX idx_leaves_teacher ON leave_requests(teacher_user_id);
CREATE INDEX idx_leaves_counselor ON leave_requests(counselor_user_id);
CREATE INDEX idx_leaves_retain ON leave_requests(retain_until);

CREATE TABLE leave_time_edits (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    leave_id INTEGER NOT NULL REFERENCES leave_requests(id) ON DELETE CASCADE,
    editor_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
    editor_name TEXT NOT NULL,
    old_start INTEGER NOT NULL,
    old_end INTEGER NOT NULL,
    new_start INTEGER NOT NULL,
    new_end INTEGER NOT NULL,
    created_at INTEGER NOT NULL
);
CREATE INDEX idx_leave_edits_leave ON leave_time_edits(leave_id);

CREATE TABLE proxy_links (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    leave_id INTEGER NOT NULL REFERENCES leave_requests(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    created_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
    show_contact_phone INTEGER NOT NULL DEFAULT 0,
    expires_at INTEGER NOT NULL,
    revoked_at INTEGER,
    created_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX idx_proxy_active ON proxy_links(leave_id) WHERE revoked_at IS NULL;

CREATE TABLE qr_tokens (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    leave_id INTEGER NOT NULL UNIQUE REFERENCES leave_requests(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    created_at INTEGER NOT NULL,
    revoked_at INTEGER
);

CREATE TABLE notifications (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    title TEXT NOT NULL,
    body TEXT NOT NULL DEFAULT '',
    link TEXT NOT NULL DEFAULT '',
    leave_id INTEGER REFERENCES leave_requests(id) ON DELETE CASCADE,
    read_at INTEGER,
    created_at INTEGER NOT NULL
);
CREATE INDEX idx_notifications_user ON notifications(user_id, read_at, created_at);

CREATE TABLE popup_receipts (
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    popup_key TEXT NOT NULL,
    day TEXT NOT NULL,
    shown_at INTEGER NOT NULL,
    PRIMARY KEY (user_id, popup_key, day)
);

CREATE TABLE password_reset_tokens (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    expires_at INTEGER NOT NULL,
    used_at INTEGER,
    created_at INTEGER NOT NULL,
    ip TEXT NOT NULL DEFAULT '',
    issued_by INTEGER REFERENCES users(id) ON DELETE SET NULL
);
CREATE INDEX idx_prt_user ON password_reset_tokens(user_id);
CREATE INDEX idx_prt_expires ON password_reset_tokens(expires_at);

CREATE TABLE password_change_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    changed_at INTEGER NOT NULL,
    method TEXT NOT NULL,
    ip TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_pce_user ON password_change_events(user_id, changed_at);

CREATE TABLE password_reset_attempts (
    key_hash TEXT PRIMARY KEY,
    user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
    ip TEXT NOT NULL DEFAULT '',
    stage INTEGER NOT NULL DEFAULT 0,
    failures INTEGER NOT NULL DEFAULT 0,
    locked_until INTEGER,
    updated_at INTEGER NOT NULL
);
CREATE INDEX idx_pra_ip ON password_reset_attempts(ip, updated_at);
CREATE INDEX idx_pra_locked ON password_reset_attempts(locked_until);

CREATE TABLE registration_attempts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ip TEXT NOT NULL DEFAULT '',
    ip_prefix TEXT NOT NULL DEFAULT '',
    user_agent TEXT NOT NULL DEFAULT '',
    device_hash TEXT NOT NULL DEFAULT '',
    code_id INTEGER REFERENCES registration_codes(id) ON DELETE SET NULL,
    role TEXT NOT NULL DEFAULT '',
    user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
    success INTEGER NOT NULL DEFAULT 0,
    reason TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
);
CREATE INDEX idx_regatt_ip ON registration_attempts(ip, created_at);
CREATE INDEX idx_regatt_prefix ON registration_attempts(ip_prefix, created_at);
CREATE INDEX idx_regatt_device ON registration_attempts(device_hash, created_at);
CREATE INDEX idx_regatt_code ON registration_attempts(code_id, created_at);
CREATE INDEX idx_regatt_user ON registration_attempts(user_id, created_at);

CREATE TABLE login_attempts (
    key TEXT NOT NULL,
    scope TEXT NOT NULL,
    window_start INTEGER NOT NULL,
    failed_count INTEGER NOT NULL DEFAULT 0,
    locked_until INTEGER,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (key, scope)
);
CREATE INDEX idx_login_attempts_updated ON login_attempts(updated_at);

CREATE TABLE password_reset_requests (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    class_id INTEGER REFERENCES classes(id) ON DELETE SET NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','rejected','expired','used')),
    request_code_hash TEXT NOT NULL,
    browser_token_hash TEXT NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    requested_ip TEXT NOT NULL DEFAULT '',
    requested_at INTEGER NOT NULL,
    approved_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
    approved_at INTEGER,
    expires_at INTEGER NOT NULL,
    used_at INTEGER
);
CREATE UNIQUE INDEX idx_prr_one_pending ON password_reset_requests(user_id) WHERE status = 'pending';
CREATE INDEX idx_prr_user_status ON password_reset_requests(user_id, status);
CREATE INDEX idx_prr_expires ON password_reset_requests(expires_at);

CREATE TABLE student_import_batches (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    class_id INTEGER REFERENCES classes(id) ON DELETE SET NULL,
    imported_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
    file_name TEXT NOT NULL DEFAULT '',
    total_rows INTEGER NOT NULL DEFAULT 0,
    created_count INTEGER NOT NULL DEFAULT 0,
    error_count INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'success' CHECK (status IN ('success','failed')),
    ip TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
);
CREATE INDEX idx_imports_class ON student_import_batches(class_id, created_at);
CREATE INDEX idx_imports_importer ON student_import_batches(imported_by, created_at);

CREATE TABLE admin_audit_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    actor_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
    actor_name TEXT NOT NULL DEFAULT '',
    action TEXT NOT NULL,
    target_type TEXT NOT NULL DEFAULT '',
    target_id INTEGER,
    target_class_id INTEGER,
    target_class_name TEXT NOT NULL DEFAULT '',
    target_department TEXT NOT NULL DEFAULT '',
    target_building TEXT NOT NULL DEFAULT '',
    detail TEXT NOT NULL DEFAULT '',
    ip TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
);
CREATE INDEX idx_audit_actor ON admin_audit_logs(actor_user_id, created_at);
CREATE INDEX idx_audit_target ON admin_audit_logs(target_type, target_id);
CREATE INDEX idx_audit_target_class ON admin_audit_logs(target_class_id);
CREATE INDEX idx_audit_target_department ON admin_audit_logs(target_department);
CREATE INDEX idx_audit_target_building ON admin_audit_logs(target_building);
`

func (s *Store) migrate() error {
	var v int
	if err := s.DB.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v >= 4 {
		return nil
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	switch v {
	case 0:
		if _, err := tx.Exec(schemaV1); err != nil {
			return err
		}
	case 1:
		// v1 databases predate staff-proxy submissions; add the creator column.
		if _, err := tx.Exec(`ALTER TABLE leave_requests ADD COLUMN created_by INTEGER REFERENCES users(id) ON DELETE SET NULL`); err != nil {
			return err
		}
	}
	if v > 0 {
		// Keep the v3 index migration for the TTL cleanup of rate-limit rows.
		if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_login_attempts_updated ON login_attempts(updated_at)`); err != nil {
			return err
		}
		// v4 snapshots the class context of audited targets so historical
		// filters keep working after classes are renamed or deleted.
		for _, stmt := range []string{
			`ALTER TABLE admin_audit_logs ADD COLUMN target_class_id INTEGER`,
			`ALTER TABLE admin_audit_logs ADD COLUMN target_class_name TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE admin_audit_logs ADD COLUMN target_department TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE admin_audit_logs ADD COLUMN target_building TEXT NOT NULL DEFAULT ''`,
		} {
			if _, err := tx.Exec(stmt); err != nil {
				return err
			}
		}
	}
	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS idx_audit_target_class ON admin_audit_logs(target_class_id)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_target_department ON admin_audit_logs(target_department)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_target_building ON admin_audit_logs(target_building)`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`PRAGMA user_version = 4`); err != nil {
		return err
	}
	return tx.Commit()
}
