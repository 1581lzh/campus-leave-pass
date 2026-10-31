package secure

import (
	"regexp"
	"strings"
	"testing"
)

func TestPasswordHashAndVerify(t *testing.T) {
	hash, err := HashPassword("Passw0rd!")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if hash == "Passw0rd!" || !strings.HasPrefix(hash, "$2") {
		t.Fatalf("哈希格式异常: %q", hash)
	}
	if !CheckPassword(hash, "Passw0rd!") {
		t.Fatal("正确密码应通过")
	}
	if CheckPassword(hash, "wrong-password") {
		t.Fatal("错误密码不应通过")
	}
	if CheckPassword("not-a-bcrypt-hash", "Passw0rd!") {
		t.Fatal("非法哈希不应通过")
	}
	CheckDummy("anything") // 只要求不 panic
}

func TestHMACAndSHA(t *testing.T) {
	key := []byte("0123456789abcdef")
	a1 := HMACHex(key, "hello")
	a2 := HMACHex(key, "hello")
	if a1 != a2 {
		t.Fatal("同一 key/input 的 HMAC 应稳定")
	}
	if len(a1) != 64 {
		t.Fatalf("HMAC 十六进制长度 = %d", len(a1))
	}
	if b := HMACHex([]byte("different-key-xx"), "hello"); b == a1 {
		t.Fatal("不同 key 的 HMAC 不应相同")
	}
	if b := HMACHex(key, "hello2"); b == a1 {
		t.Fatal("不同输入的 HMAC 不应相同")
	}

	s1 := SHA256Hex("abc")
	if s1 != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("SHA256Hex(abc) = %q", s1)
	}
	if SHA256Hex("abc") != s1 {
		t.Fatal("SHA256Hex 应稳定")
	}
}

func TestEqual(t *testing.T) {
	if !Equal("abc", "abc") {
		t.Fatal("相同字符串应相等")
	}
	if Equal("abc", "abd") || Equal("abc", "ab") || Equal("", "a") {
		t.Fatal("不同字符串不应相等")
	}
}

var codeAlphabetRE = regexp.MustCompile(`^[ABCDEFGHJKMNPQRSTUVWXYZ23456789]{4}(-[ABCDEFGHJKMNPQRSTUVWXYZ23456789]{4}){2}$`)

func TestNewRegistrationCode(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		norm, display, err := NewRegistrationCode()
		if err != nil {
			t.Fatalf("NewRegistrationCode: %v", err)
		}
		if len(norm) != 12 {
			t.Fatalf("规范化码长度 = %d (%q)", len(norm), norm)
		}
		for _, bad := range []string{"0", "O", "1", "I", "l"} {
			if strings.Contains(norm, bad) {
				t.Fatalf("码 %q 含易混淆字符 %q", norm, bad)
			}
		}
		if !codeAlphabetRE.MatchString(display) {
			t.Fatalf("展示格式异常: %q", display)
		}
		if NormalizeCode(display) != norm {
			t.Fatalf("展示码规范化 = %q，期望 %q", NormalizeCode(display), norm)
		}
		if seen[norm] {
			t.Fatalf("重复注册码: %q", norm)
		}
		seen[norm] = true
	}
}

func TestFormatNormalizeHintCode(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"ABCDEFGHIJKL", "ABCD-EFGH-IJKL"},
		{"ABCD", "ABCD"},
		{"ABCDE", "ABCD-E"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := FormatCode(tc.in); got != tc.want {
			t.Fatalf("FormatCode(%q) = %q，期望 %q", tc.in, got, tc.want)
		}
	}

	if got := NormalizeCode(" abcd-efgh 2345 "); got != "ABCDEFGH2345" {
		t.Fatalf("NormalizeCode 空格/横线 = %q", got)
	}
	if got := NormalizeCode("AB—CD–EF　GH"); got != "ABCDEFGH" {
		t.Fatalf("NormalizeCode 全角/长横线 = %q", got)
	}
	if got := CodeHint("ABCDEFGH"); got != "ABCD" {
		t.Fatalf("CodeHint = %q", got)
	}
	if got := CodeHint("AB"); got != "AB" {
		t.Fatalf("CodeHint 短码 = %q", got)
	}
}

func TestRandomTokenAndResetCode(t *testing.T) {
	seen := make(map[string]bool)
	for _, n := range []int{8, 16, 24, 32} {
		tok, err := RandomToken(n)
		if err != nil {
			t.Fatalf("RandomToken(%d): %v", n, err)
		}
		if len(tok) == 0 {
			t.Fatalf("RandomToken(%d) 为空", n)
		}
		for _, r := range tok {
			if !(r == '-' || r == '_' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z') {
				t.Fatalf("RandomToken 含非 base64url 字符: %q", tok)
			}
		}
		if seen[tok] {
			t.Fatalf("重复 token: %q", tok)
		}
		seen[tok] = true
	}

	six := regexp.MustCompile(`^[0-9]{6}$`)
	for i := 0; i < 100; i++ {
		code, err := ResetCode()
		if err != nil {
			t.Fatalf("ResetCode: %v", err)
		}
		if !six.MatchString(code) {
			t.Fatalf("ResetCode 格式异常: %q", code)
		}
	}
}

func TestParseInt(t *testing.T) {
	ok := map[string]int64{"0": 0, "1": 1, "42": 42, "9007199254740991": 1<<53 - 1, "9007199254740992": 1 << 53}
	for in, want := range ok {
		got, err := ParseInt(in)
		if err != nil || got != want {
			t.Fatalf("ParseInt(%q) = (%d,%v)", in, got, err)
		}
	}
	for _, in := range []string{"", "-1", "1.0", "abc", " 1", "9007199254740993"} {
		if _, err := ParseInt(in); err == nil {
			t.Fatalf("ParseInt(%q) 应报错", in)
		}
	}
}
