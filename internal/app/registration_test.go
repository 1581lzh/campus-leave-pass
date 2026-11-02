package app

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"leavepass/internal/secure"
)

func mustCreateCode(t *testing.T, s *Store, admin User, role string, maxUses int64) (RegistrationCode, string) {
	t.Helper()
	c, plain, err := s.CreateRegistrationCode(ctx(), admin, []string{role}, maxUses, 0, 0, "测试", "127.0.0.1")
	if err != nil {
		t.Fatalf("CreateRegistrationCode: %v", err)
	}
	return c, plain
}

func TestCreateRegistrationCodeValidation(t *testing.T) {
	s, clock, admin := newTestStore(t)
	teacher := addStaff(t, s, "teacher", "RG1", "13900000001")
	now := clock.Unix()

	tests := []struct {
		name    string
		actor   User
		role    string
		max     int64
		from    int64
		until   int64
		wantSub string
	}{
		{name: "非管理员", actor: teacher, role: "teacher", max: 1, wantSub: "只有管理员"},
		{name: "学生角色", actor: admin, role: "student", max: 1, wantSub: "班主任、辅导员或社区老师"},
		{name: "管理员角色", actor: admin, role: "admin", max: 1, wantSub: "班主任、辅导员或社区老师"},
		{name: "次数为零", actor: admin, role: "teacher", max: 0, wantSub: "1–500"},
		{name: "次数超限", actor: admin, role: "teacher", max: 501, wantSub: "1–500"},
		{name: "失效早于生效", actor: admin, role: "teacher", max: 1, from: now + 100, until: now + 50, wantSub: "失效时间必须晚于生效时间"},
		{name: "超过 90 天", actor: admin, role: "teacher", max: 1, until: now + 90*86400 + 1, wantSub: "90 天"},
		{name: "生效已过但失效在未来", actor: admin, role: "teacher", max: 1, from: now - 86400, until: now + 86400},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := s.CreateRegistrationCode(ctx(), tc.actor, []string{tc.role}, tc.max, tc.from, tc.until, "", "127.0.0.1")
			if tc.wantSub == "" {
				if err != nil {
					t.Fatalf("期望成功，实际错误: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("错误 = %v，期望包含 %q", err, tc.wantSub)
			}
		})
	}

	// 次数上下边界 1/500 都应可用。
	for _, max := range []int64{1, 500} {
		c, _, err := s.CreateRegistrationCode(ctx(), admin, []string{"community"}, max, 0, 0, "", "127.0.0.1")
		if err != nil {
			t.Fatalf("max=%d 应成功: %v", max, err)
		}
		if c.MaxUses != max {
			t.Fatalf("MaxUses = %d", c.MaxUses)
		}
	}
}

func TestCreateRegistrationCodeDefaultsAndAtRest(t *testing.T) {
	s, clock, admin := newTestStore(t)
	now := clock.Unix()

	c, plain, err := s.CreateRegistrationCode(ctx(), admin, []string{"teacher"}, 5, 0, 0, "备注", "127.0.0.1")
	if err != nil {
		t.Fatalf("CreateRegistrationCode: %v", err)
	}
	if c.ValidFrom != now {
		t.Fatalf("默认生效时间 = %d，期望 %d", c.ValidFrom, now)
	}
	if c.ValidUntil != now+7*86400 {
		t.Fatalf("默认失效时间 = %d，期望 7 天后 %d", c.ValidUntil, now+7*86400)
	}
	norm := secure.NormalizeCode(plain)
	if len(norm) != 12 {
		t.Fatalf("明文规范化长度 = %d", len(norm))
	}
	if c.CodeHint != norm[:4] {
		t.Fatalf("CodeHint = %q，期望 %q", c.CodeHint, norm[:4])
	}
	if c.CodeHash == norm || strings.Contains(c.CodeHash, norm) {
		t.Fatalf("库中 CodeHash 不应包含明文: %q", c.CodeHash)
	}
	wantHash := secure.HMACHex(s.RegistrationSecret(), norm)
	if c.CodeHash != wantHash {
		t.Fatalf("CodeHash = %q，期望 HMAC %q", c.CodeHash, wantHash)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM registration_codes WHERE code_hash=?`, norm); n != 0 {
		t.Fatalf("库中出现明文注册码行: %d", n)
	}
	// 列表结构没有明文字段，只能看到 hint 和哈希。
	list, err := s.ListRegistrationCodes(ctx())
	if err != nil {
		t.Fatalf("ListRegistrationCodes: %v", err)
	}
	found := false
	for _, rc := range list {
		if rc.ID == c.ID {
			found = true
			if strings.Contains(plain, rc.CodeHash) || strings.Contains(rc.Note, plain) {
				t.Fatal("列表泄露明文注册码")
			}
		}
	}
	if !found {
		t.Fatal("列表缺少刚创建的注册码")
	}
	// 状态机覆盖：未生效、已过期、已停用、已用完。
	if got := (RegistrationCode{Active: true, ValidFrom: now + 10, ValidUntil: now + 20, MaxUses: 1}).Status(now); got != "未生效" {
		t.Fatalf("未生效状态 = %q", got)
	}
	if got := (RegistrationCode{Active: true, ValidFrom: now - 20, ValidUntil: now - 10, MaxUses: 1}).Status(now); got != "已过期" {
		t.Fatalf("已过期状态 = %q", got)
	}
	if got := (RegistrationCode{Active: false}).Status(now); got != "已停用" {
		t.Fatalf("已停用状态 = %q", got)
	}
	if got := (RegistrationCode{Active: true, ValidFrom: now - 1, ValidUntil: now + 10, MaxUses: 1, UsedCount: 1}).Status(now); got != "已用完" {
		t.Fatalf("已用完状态 = %q", got)
	}
}

func TestRegisterStaffSuccessAndUsage(t *testing.T) {
	s, clock, admin := newTestStore(t)
	code, plain := mustCreateCode(t, s, admin, "teacher", 2)

	u, err := s.RegisterStaff(ctx(), StaffRegisterInput{
		Code: plain, Username: "王老师", Password: "Passw0rd", Phone: "13911110001",
	}, "203.0.113.10", "unit-test", "")
	if err != nil {
		t.Fatalf("RegisterStaff: %v", err)
	}
	if u.Role != "teacher" || u.Status != "active" {
		t.Fatalf("注册结果角色/状态 = %s/%s", u.Role, u.Status)
	}
	if u.Username != "王老师" || u.Phone != "13911110001" {
		t.Fatalf("注册结果字段 = %q/%q", u.Username, u.Phone)
	}
	if !secure.CheckPassword(u.PasswordHash, "Passw0rd") {
		t.Fatal("注册密码未被正确哈希")
	}
	if got := s.GetRegistrationCode(ctx(), code.ID); got.UsedCount != 1 || got.LastUsedAt != clock.Unix() {
		t.Fatalf("注册码使用计数 = %d lastUsed=%d", got.UsedCount, got.LastUsedAt)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM registration_attempts WHERE user_id=? AND success=1`, u.ID); n != 1 {
		t.Fatalf("成功注册审计行 = %d", n)
	}

	// 第二次使用同一注册码，带空格和横线的展示格式也应能识别。
	u2, err := s.RegisterStaff(ctx(), StaffRegisterInput{
		Code: " " + plain + " ", Username: "李老师", Password: "Passw0rd", Phone: "13911110002",
	}, "203.0.113.10", "unit-test", "")
	if err != nil {
		t.Fatalf("第二次 RegisterStaff: %v", err)
	}
	if u2.Role != "teacher" {
		t.Fatalf("第二次角色 = %s", u2.Role)
	}
	if got := s.GetRegistrationCode(ctx(), code.ID); got.UsedCount != 2 {
		t.Fatalf("使用计数 = %d，期望 2", got.UsedCount)
	}

	// 次数用完后拒绝，且不再增长。
	_, err = s.RegisterStaff(ctx(), StaffRegisterInput{
		Code: plain, Username: "赵老师", Password: "Passw0rd", Phone: "13911110003",
	}, "203.0.113.10", "unit-test", "")
	if !errors.Is(err, ErrCodeUnavailable) {
		t.Fatalf("用完后的错误 = %v，期望 ErrCodeUnavailable", err)
	}
	if got := s.GetRegistrationCode(ctx(), code.ID); got.UsedCount != 2 {
		t.Fatalf("失败后使用计数 = %d", got.UsedCount)
	}
}

func TestCreateRegistrationCodeMultiRole(t *testing.T) {
	s, _, admin := newTestStore(t)

	c, _, err := s.CreateRegistrationCode(ctx(), admin, []string{"community", "teacher", "teacher"}, 2, 0, 0, "", "")
	if err != nil {
		t.Fatalf("CreateRegistrationCode: %v", err)
	}
	if c.Role != "teacher" {
		t.Fatalf("兼容 role = %q，期望首个角色 teacher", c.Role)
	}
	if got := strings.Join(c.Roles, ","); got != "teacher,community" {
		t.Fatalf("roles = %q，期望规范化 teacher,community", got)
	}
	if !c.AllowsRole("community") || c.AllowsRole("counselor") {
		t.Fatalf("AllowsRole 判断错误: %v", c.Roles)
	}

	if _, _, err := s.CreateRegistrationCode(ctx(), admin, nil, 1, 0, 0, "", ""); err == nil || !strings.Contains(err.Error(), "至少选择一个") {
		t.Fatalf("空角色错误 = %v", err)
	}
	if _, _, err := s.CreateRegistrationCode(ctx(), admin, []string{"admin"}, 1, 0, 0, "", ""); err == nil || !strings.Contains(err.Error(), "班主任、辅导员或社区老师") {
		t.Fatalf("非法角色错误 = %v", err)
	}
}

func TestInspectRegistrationCodeDoesNotConsume(t *testing.T) {
	s, _, admin := newTestStore(t)
	code, plain := mustCreateCode(t, s, admin, "teacher", 1)

	got, err := s.InspectRegistrationCode(ctx(), " "+plain+" ")
	if err != nil {
		t.Fatalf("InspectRegistrationCode: %v", err)
	}
	if got.ID != code.ID || !got.AllowsRole("teacher") {
		t.Fatalf("检查结果 = %+v", got)
	}
	if used := s.GetRegistrationCode(ctx(), code.ID).UsedCount; used != 0 {
		t.Fatalf("检查不应消耗次数，used=%d", used)
	}

	if _, err := s.RegisterStaff(ctx(), StaffRegisterInput{
		Code: plain, Role: "teacher", Username: "检查老师", Password: "Passw0rd", Phone: "13955550001",
	}, "203.0.113.20", "unit-test", ""); err != nil {
		t.Fatalf("RegisterStaff: %v", err)
	}
	if _, err := s.InspectRegistrationCode(ctx(), plain); !errors.Is(err, ErrCodeUnavailable) {
		t.Fatalf("用完后检查错误 = %v，期望 ErrCodeUnavailable", err)
	}
	if _, err := s.InspectRegistrationCode(ctx(), "AAAA-BBBB-CCCC"); !errors.Is(err, ErrCodeUnavailable) {
		t.Fatalf("不存在注册码错误 = %v，期望 ErrCodeUnavailable", err)
	}
}

func TestRegisterStaffMultiRole(t *testing.T) {
	s, _, admin := newTestStore(t)
	code, plain, err := s.CreateRegistrationCode(ctx(), admin, []string{"teacher", "counselor"}, 2, 0, 0, "", "")
	if err != nil {
		t.Fatalf("CreateRegistrationCode: %v", err)
	}

	u1, err := s.RegisterStaff(ctx(), StaffRegisterInput{
		Code: plain, Role: "counselor", Username: "多角色甲", Password: "Passw0rd", Phone: "13955550002",
	}, "203.0.113.21", "unit-test", "")
	if err != nil {
		t.Fatalf("注册辅导员: %v", err)
	}
	if u1.Role != "counselor" {
		t.Fatalf("第一个账号角色 = %q，期望 counselor", u1.Role)
	}
	u2, err := s.RegisterStaff(ctx(), StaffRegisterInput{
		Code: plain, Role: "teacher", Username: "多角色乙", Password: "Passw0rd", Phone: "13955550003",
	}, "203.0.113.21", "unit-test", "")
	if err != nil {
		t.Fatalf("注册班主任: %v", err)
	}
	if u2.Role != "teacher" {
		t.Fatalf("第二个账号角色 = %q，期望 teacher", u2.Role)
	}
	if got := s.GetRegistrationCode(ctx(), code.ID).UsedCount; got != 2 {
		t.Fatalf("共享次数 = %d，期望 2", got)
	}
	var attemptedRole string
	if err := s.DB.QueryRow(`SELECT role FROM registration_attempts WHERE user_id=? AND success=1`, u1.ID).Scan(&attemptedRole); err != nil {
		t.Fatalf("读取注册尝试: %v", err)
	}
	if attemptedRole != "counselor" {
		t.Fatalf("注册尝试角色 = %q", attemptedRole)
	}

	_, err = s.RegisterStaff(ctx(), StaffRegisterInput{
		Code: plain, Role: "community", Username: "多角色丙", Password: "Passw0rd", Phone: "13955550004",
	}, "203.0.113.21", "unit-test", "")
	if !errors.Is(err, ErrCodeUnavailable) {
		t.Fatalf("次数用完后错误 = %v，期望 ErrCodeUnavailable", err)
	}
}

func TestRegisterStaffRejectsDisallowedOrMissingRole(t *testing.T) {
	s, _, admin := newTestStore(t)
	code, plain := mustCreateCode(t, s, admin, "teacher", 2)

	_, err := s.RegisterStaff(ctx(), StaffRegisterInput{
		Code: plain, Role: "community", Username: "越权老师", Password: "Passw0rd", Phone: "13955550005",
	}, "203.0.113.22", "unit-test", "")
	if err == nil || !strings.Contains(err.Error(), "不在该注册码允许范围内") {
		t.Fatalf("不允许角色错误 = %v", err)
	}
	if got := s.GetRegistrationCode(ctx(), code.ID).UsedCount; got != 0 {
		t.Fatalf("失败后次数 = %d，期望 0", got)
	}

	multi, multiPlain, err := s.CreateRegistrationCode(ctx(), admin, []string{"teacher", "counselor"}, 2, 0, 0, "", "")
	if err != nil {
		t.Fatalf("CreateRegistrationCode multi: %v", err)
	}
	_, err = s.RegisterStaff(ctx(), StaffRegisterInput{
		Code: multiPlain, Username: "未选角色", Password: "Passw0rd", Phone: "13955550006",
	}, "203.0.113.22", "unit-test", "")
	if err == nil || !strings.Contains(err.Error(), "请选择注册角色") {
		t.Fatalf("未选角色错误 = %v", err)
	}
	if got := s.GetRegistrationCode(ctx(), multi.ID).UsedCount; got != 0 {
		t.Fatalf("未选角色后次数 = %d，期望 0", got)
	}
}

func TestRegisterStaffRejectsExpiredDisabledAndNotYetValid(t *testing.T) {
	s, clock, admin := newTestStore(t)
	now := clock.Unix()

	expired, expiredPlain, err := s.CreateRegistrationCode(ctx(), admin, []string{"teacher"}, 5, now-86400, now-1, "", "")
	if err != nil {
		t.Fatalf("create expired: %v", err)
	}
	_, notYetPlain, err := s.CreateRegistrationCode(ctx(), admin, []string{"teacher"}, 5, now+3600, now+7200, "", "")
	if err != nil {
		t.Fatalf("create future: %v", err)
	}
	disabled, disabledPlain := mustCreateCode(t, s, admin, "teacher", 5)
	off := false
	if err := s.UpdateRegistrationCode(ctx(), admin, disabled.ID, 0, 0, &off, ""); err != nil {
		t.Fatalf("disable code: %v", err)
	}
	if got := s.GetRegistrationCode(ctx(), disabled.ID); got.Active {
		t.Fatal("停用后 Active 仍为 true")
	}

	cases := []struct {
		name  string
		code  string
		phone string
		user  string
	}{
		{name: "已过期", code: expiredPlain, phone: "13922220001", user: "过期老师"},
		{name: "未生效", code: notYetPlain, phone: "13922220002", user: "未来老师"},
		{name: "已停用", code: disabledPlain, phone: "13922220003", user: "停用老师"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.RegisterStaff(ctx(), StaffRegisterInput{
				Code: tc.code, Username: tc.user, Password: "Passw0rd", Phone: tc.phone,
			}, "203.0.113.11", "unit-test", "")
			if !errors.Is(err, ErrCodeUnavailable) {
				t.Fatalf("错误 = %v，期望 ErrCodeUnavailable", err)
			}
		})
	}
	if got := s.GetRegistrationCode(ctx(), expired.ID); got.UsedCount != 0 {
		t.Fatalf("过期码使用计数 = %d", got.UsedCount)
	}
}

func TestRegisterStaffFailureDoesNotConsumeUsage(t *testing.T) {
	s, _, admin := newTestStore(t)
	existing := addStaff(t, s, "teacher", "EX1", "13933330001")
	code, plain := mustCreateCode(t, s, admin, "teacher", 5)

	cases := []struct {
		name string
		in   StaffRegisterInput
	}{
		{
			name: "重复手机号",
			in:   StaffRegisterInput{Code: plain, Username: "新老师甲", Password: "Passw0rd", Phone: existing.Phone},
		},
		{
			name: "重复用户名",
			in:   StaffRegisterInput{Code: plain, Username: existing.Username, Password: "Passw0rd", Phone: "13933330002"},
		},
		{
			name: "非法用户名",
			in:   StaffRegisterInput{Code: plain, Username: "bad name", Password: "Passw0rd", Phone: "13933330003"},
		},
		{
			name: "非法密码",
			in:   StaffRegisterInput{Code: plain, Username: "新老师乙", Password: "123", Phone: "13933330004"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.RegisterStaff(ctx(), tc.in, "203.0.113.12", "unit-test", ""); err == nil {
				t.Fatal("期望注册失败")
			}
			if got := s.GetRegistrationCode(ctx(), code.ID); got.UsedCount != 0 {
				t.Fatalf("失败后使用计数 = %d，期望 0", got.UsedCount)
			}
		})
	}
}

func TestUpdateRegistrationCodeOnlyAdminAndBounds(t *testing.T) {
	s, clock, admin := newTestStore(t)
	teacher := addStaff(t, s, "teacher", "UP1", "13944440001")
	code, _ := mustCreateCode(t, s, admin, "teacher", 3)
	now := clock.Unix()

	if err := s.UpdateRegistrationCode(ctx(), teacher, code.ID, 5, 0, nil, ""); err == nil || !strings.Contains(err.Error(), "只有管理员") {
		t.Fatalf("非管理员修改错误 = %v", err)
	}
	if err := s.UpdateRegistrationCode(ctx(), admin, 999999, 5, 0, nil, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在注册码错误 = %v", err)
	}
	if err := s.UpdateRegistrationCode(ctx(), admin, code.ID, 0, 0, nil, ""); err != nil {
		t.Fatalf("maxUses=0 表示不修改: %v", err)
	}
	if err := s.UpdateRegistrationCode(ctx(), admin, code.ID, 501, 0, nil, ""); err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("maxUses 超限错误 = %v", err)
	}
	if err := s.UpdateRegistrationCode(ctx(), admin, code.ID, 0, now, nil, ""); err == nil || !strings.Contains(err.Error(), "晚于当前时间") {
		t.Fatalf("失效时间等于当前错误 = %v", err)
	}
	if err := s.UpdateRegistrationCode(ctx(), admin, code.ID, 0, now+90*86400+1, nil, ""); err == nil || !strings.Contains(err.Error(), "90 天") {
		t.Fatalf("失效时间超 90 天错误 = %v", err)
	}
	if err := s.UpdateRegistrationCode(ctx(), admin, code.ID, 4, now+86400, nil, "127.0.0.1"); err != nil {
		t.Fatalf("合法修改: %v", err)
	}
	got := s.GetRegistrationCode(ctx(), code.ID)
	if got.MaxUses != 4 || got.ValidUntil != now+86400 {
		t.Fatalf("修改后 = max %d until %d", got.MaxUses, got.ValidUntil)
	}

	off := false
	if err := s.UpdateRegistrationCode(ctx(), admin, code.ID, 0, 0, &off, ""); err != nil {
		t.Fatalf("停用: %v", err)
	}
	got = s.GetRegistrationCode(ctx(), code.ID)
	if got.Active || got.DisabledAt != now {
		t.Fatalf("停用后 active=%v disabledAt=%d", got.Active, got.DisabledAt)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM admin_audit_logs WHERE action='regcode.update'`); n < 3 {
		t.Fatalf("注册码修改审计行 = %d", n)
	}
}

func TestRegistrationCodeConcurrentLastUseOnlyOneWins(t *testing.T) {
	s, _, admin := newTestStore(t)
	code, plain := mustCreateCode(t, s, admin, "teacher", 1)

	type result struct {
		u   User
		err error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for i, in := range []StaffRegisterInput{
		{Code: plain, Username: "并发甲", Password: "Passw0rd", Phone: "13955550001"},
		{Code: plain, Username: "并发乙", Password: "Passw0rd", Phone: "13955550002"},
	} {
		wg.Add(1)
		go func(i int, in StaffRegisterInput) {
			defer wg.Done()
			u, err := s.RegisterStaff(ctx(), in, "203.0.113.2"+string(rune('0'+i)), "unit-test", "")
			results <- result{u: u, err: err}
		}(i, in)
	}
	wg.Wait()
	close(results)

	success, unavailable := 0, 0
	for r := range results {
		switch {
		case r.err == nil:
			success++
		case errors.Is(r.err, ErrCodeUnavailable):
			unavailable++
		default:
			t.Fatalf("并发注册出现意外错误: %v", r.err)
		}
	}
	if success != 1 || unavailable != 1 {
		t.Fatalf("并发成功=%d 不可用=%d，期望 1/1", success, unavailable)
	}
	if got := s.GetRegistrationCode(ctx(), code.ID); got.UsedCount != 1 {
		t.Fatalf("并发后使用计数 = %d，期望 1", got.UsedCount)
	}
}
