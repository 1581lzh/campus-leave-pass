package validate

import (
	"strings"
	"testing"
)

func TestNormalizeUsername(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		display string
		login   string
		wantErr bool
	}{
		{name: "中文", raw: "张三", display: "张三", login: "张三"},
		{name: "全角字母和数字", raw: "Ａｂ１", display: "Ab1", login: "ab1"},
		{name: "首尾空格", raw: "  李四  ", display: "李四", login: "李四"},
		{name: "允许混合", raw: "user1", display: "user1", login: "user1"},
		{name: "空", raw: "", wantErr: true},
		{name: "只有空格", raw: "   ", wantErr: true},
		{name: "超过 6 字", raw: "abcdefg", wantErr: true},
		{name: "内部空格", raw: "张 三", wantErr: true},
		{name: "符号", raw: "张三!", wantErr: true},
		{name: "控制字符", raw: "ab\ncd", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			display, login, err := NormalizeUsername(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("NormalizeUsername(%q) 期望错误，实际 display=%q login=%q", tc.raw, display, login)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeUsername(%q) 意外错误: %v", tc.raw, err)
			}
			if display != tc.display || login != tc.login {
				t.Fatalf("NormalizeUsername(%q) = (%q,%q)，期望 (%q,%q)", tc.raw, display, login, tc.display, tc.login)
			}
		})
	}
}

func TestNormalizeName(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "中文", raw: "张三", want: "张三"},
		{name: "全角加空格", raw: "　张三　", want: "张三"},
		{name: "英文空格", raw: "John Smith", want: "John Smith"},
		{name: "间隔号", raw: "阿依古丽·买买提", want: "阿依古丽·买买提"},
		{name: "太短", raw: "张", wantErr: true},
		{name: "超过 20 字", raw: strings.Repeat("张", 21), wantErr: true},
		{name: "数字", raw: "张三1", wantErr: true},
		{name: "标点", raw: "张三,", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeName(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("NormalizeName(%q) 期望错误，实际 %q", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeName(%q) 意外错误: %v", tc.raw, err)
			}
			if got != tc.want {
				t.Fatalf("NormalizeName(%q) = %q，期望 %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestNormalizeStudentNo(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "普通学号", raw: "20240001", want: "20240001"},
		{name: "大小写统一", raw: "abc-123_x", want: "ABC-123_X"},
		{name: "全角转半角", raw: "２０２４０００１", want: "20240001"},
		{name: "首尾空格", raw: "  2024-0001  ", want: "2024-0001"},
		{name: "3 位太短", raw: "abc", wantErr: true},
		{name: "超过 32 位", raw: strings.Repeat("A", 33), wantErr: true},
		{name: "32 位边界", raw: strings.Repeat("A", 32), want: strings.Repeat("A", 32)},
		{name: "中文", raw: "学号123", wantErr: true},
		{name: "内部空格", raw: "2024 0001", wantErr: true},
		{name: "点号", raw: "2024.0001", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			display, norm, err := NormalizeStudentNo(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("NormalizeStudentNo(%q) 期望错误，实际 display=%q norm=%q", tc.raw, display, norm)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeStudentNo(%q) 意外错误: %v", tc.raw, err)
			}
			if display != tc.want || norm != tc.want {
				t.Fatalf("NormalizeStudentNo(%q) = (%q,%q)，期望 %q", tc.raw, display, norm, tc.want)
			}
		})
	}
}

func TestNormalizePhoneAndMask(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "普通", raw: "13800000000", want: "13800000000"},
		{name: "空格和横线", raw: "138 0000-0000", want: "13800000000"},
		{name: "全角", raw: "１３８００００００００", want: "13800000000"},
		{name: "第二位 2", raw: "12800000000", wantErr: true},
		{name: "10 位", raw: "1380000000", wantErr: true},
		{name: "12 位", raw: "138000000000", wantErr: true},
		{name: "字母", raw: "1380000000a", wantErr: true},
		{name: "不以 1 开头", raw: "23800000000", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizePhone(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("NormalizePhone(%q) 期望错误，实际 %q", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizePhone(%q) 意外错误: %v", tc.raw, err)
			}
			if got != tc.want {
				t.Fatalf("NormalizePhone(%q) = %q，期望 %q", tc.raw, got, tc.want)
			}
		})
	}

	if got := MaskPhone("13800000000"); got != "138******00" {
		t.Fatalf("MaskPhone 正常号码 = %q", got)
	}
	if got := MaskPhone("123"); got != "***********" {
		t.Fatalf("MaskPhone 非法长度 = %q", got)
	}
}

func TestValidatePassword(t *testing.T) {
	tests := []struct {
		name    string
		pw      string
		admin   bool
		wantErr bool
	}{
		{name: "普通 4 位", pw: "1234", wantErr: true},
		{name: "普通 5 位", pw: "12345"},
		{name: "普通 19 位", pw: strings.Repeat("a", 19)},
		{name: "普通 20 位", pw: strings.Repeat("a", 20), wantErr: true},
		{name: "空格", pw: "123 45", wantErr: true},
		{name: "中文", pw: "密码12345", wantErr: true},
		{name: "英文符号", pw: "!@#$%"},
		{name: "管理员 1 位", pw: "A", admin: true},
		{name: "管理员空", pw: "", admin: true, wantErr: true},
		{name: "管理员 20 位", pw: strings.Repeat("A", 20), admin: true, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePassword(tc.pw, tc.admin)
			if tc.wantErr && err == nil {
				t.Fatalf("ValidatePassword(%q, admin=%v) 期望错误", tc.pw, tc.admin)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidatePassword(%q, admin=%v) 意外错误: %v", tc.pw, tc.admin, err)
			}
		})
	}
}

func TestSplitDorm(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		building string
		room     string
		wantErr  bool
	}{
		{name: "标准", raw: "21-601", building: "21", room: "601"},
		{name: "中文楼栋", raw: "东区-12A", building: "东区", room: "12A"},
		{name: "全角", raw: "２１－６０１", building: "21", room: "601"},
		{name: "两侧空格", raw: " 21 - 601 ", building: "21", room: "601"},
		{name: "缺横线", raw: "21 601", wantErr: true},
		{name: "缺房间", raw: "21-", wantErr: true},
		{name: "缺楼栋", raw: "-601", wantErr: true},
		{name: "多横线", raw: "21--601", wantErr: true},
		{name: "三横线", raw: "21-60-1", wantErr: true},
		{name: "房间含空格", raw: "21-60 1", wantErr: true},
		{name: "房间含符号", raw: "21-60#1", wantErr: true},
		{name: "空", raw: "", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, r, err := SplitDorm(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("SplitDorm(%q) 期望错误，实际 (%q,%q)", tc.raw, b, r)
				}
				return
			}
			if err != nil {
				t.Fatalf("SplitDorm(%q) 意外错误: %v", tc.raw, err)
			}
			if b != tc.building || r != tc.room {
				t.Fatalf("SplitDorm(%q) = (%q,%q)，期望 (%q,%q)", tc.raw, b, r, tc.building, tc.room)
			}
		})
	}
}

func TestNormalizeBuildingClassNameAndDepartment(t *testing.T) {
	if got, err := NormalizeBuilding(" 21 栋 "); err != nil || got != "21栋" {
		t.Fatalf("NormalizeBuilding = (%q,%v)，期望 21栋", got, err)
	}
	if _, err := NormalizeBuilding(""); err == nil {
		t.Fatal("NormalizeBuilding 空值应报错")
	}
	if _, err := NormalizeBuilding(strings.Repeat("楼", 31)); err == nil {
		t.Fatal("NormalizeBuilding 超长应报错")
	}
	if _, err := NormalizeBuilding("A-1_2"); err != nil {
		t.Fatalf("NormalizeBuilding 允许 - 和 _: %v", err)
	}
	if _, err := NormalizeBuilding("A.B"); err == nil {
		t.Fatal("NormalizeBuilding 点号应报错")
	}

	display, key, err := NormalizeClassName("  计算机  2401  ")
	if err != nil {
		t.Fatal(err)
	}
	if display != "计算机  2401" {
		t.Fatalf("班级 display = %q", display)
	}
	if key != "计算机 2401" {
		t.Fatalf("班级 key = %q，期望内部空白折叠", key)
	}
	if _, _, err := NormalizeClassName("班"); err == nil {
		t.Fatal("班级名过短应报错")
	}
	if _, _, err := NormalizeClassName(strings.Repeat("班", 51)); err == nil {
		t.Fatal("班级名超长应报错")
	}

	if got, err := NormalizeDepartment("  计算机系  "); err != nil || got != "计算机系" {
		t.Fatalf("NormalizeDepartment = (%q,%v)", got, err)
	}
	if _, err := NormalizeDepartment(""); err == nil {
		t.Fatal("专业系为空应报错")
	}
	if _, err := NormalizeDepartment(strings.Repeat("系", 51)); err == nil {
		t.Fatal("专业系超长应报错")
	}
}

func TestTextLengthBoundaries(t *testing.T) {
	t.Run("请假原因", func(t *testing.T) {
		if _, err := ValidateReason("四个字符"); err == nil {
			t.Fatal("4 字原因应报错")
		}
		if got, err := ValidateReason("  回家处理事  "); err != nil || got != "回家处理事" {
			t.Fatalf("5 字原因应通过并去空格: (%q,%v)", got, err)
		}
		if _, err := ValidateReason(strings.Repeat("理", 500)); err != nil {
			t.Fatalf("500 字原因应通过: %v", err)
		}
		if _, err := ValidateReason(strings.Repeat("理", 501)); err == nil {
			t.Fatal("501 字原因应报错")
		}
	})
	t.Run("审批意见", func(t *testing.T) {
		if _, err := ValidateOpinion(strings.Repeat("意", 500)); err != nil {
			t.Fatalf("500 字意见应通过: %v", err)
		}
		if _, err := ValidateOpinion(strings.Repeat("意", 501)); err == nil {
			t.Fatal("501 字意见应报错")
		}
	})
	t.Run("驳回原因", func(t *testing.T) {
		if _, err := ValidateRejectReason(strings.Repeat("因", 200)); err != nil {
			t.Fatalf("200 字驳回原因应通过: %v", err)
		}
		if _, err := ValidateRejectReason(strings.Repeat("因", 201)); err == nil {
			t.Fatal("201 字驳回原因应报错")
		}
	})
}

func TestNormalizeContactPhone(t *testing.T) {
	if got, err := NormalizeContactPhone(""); err != nil || got != "" {
		t.Fatalf("空联系电话 = (%q,%v)", got, err)
	}
	if got, err := NormalizeContactPhone(" 010-12345678 "); err != nil || got != "010-12345678" {
		t.Fatalf("联系电话 = (%q,%v)", got, err)
	}
	if _, err := NormalizeContactPhone("1234"); err == nil {
		t.Fatal("4 位联系电话应报错")
	}
	if _, err := NormalizeContactPhone("123456789012345678901"); err == nil {
		t.Fatal("21 位联系电话应报错")
	}
	if _, err := NormalizeContactPhone("010#123"); err == nil {
		t.Fatal("联系电话非法字符应报错")
	}
}

func TestCSVSafe(t *testing.T) {
	tests := map[string]string{
		"":     "",
		"普通文本": "普通文本",
		"=1+1": "'=1+1",
		"+x":   "'+x",
		"-x":   "'-x",
		"@x":   "'@x",
		"\tx":  "'\tx",
		"\rx":  "'\rx",
		"a=b":  "a=b",
	}
	for in, want := range tests {
		if got := CSVSafe(in); got != want {
			t.Fatalf("CSVSafe(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestUsernameSuggestions(t *testing.T) {
	got := UsernameSuggestions("张三")
	if len(got) != 3 {
		t.Fatalf("建议数量 = %d", len(got))
	}
	for i, v := range got {
		if want := "张三" + string(rune('1'+i)); v != want {
			t.Fatalf("建议[%d] = %q，期望 %q", i, v, want)
		}
	}
	long := UsernameSuggestions("abcdef")
	for _, v := range long {
		if len([]rune(v)) != 6 {
			t.Fatalf("长用户名建议 %q 长度 = %d", v, len([]rune(v)))
		}
	}
}
