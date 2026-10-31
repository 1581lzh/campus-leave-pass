// Package validate contains the single source of truth for input
// normalization and validation shared by handlers, services and tests.
package validate

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// NFKC normalizes width/compatibility forms and trims surrounding space.
func NFKC(s string) string {
	return strings.TrimSpace(norm.NFKC.String(s))
}

func halfWidth(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\u3000':
			b.WriteRune(' ')
		case r >= '\uff01' && r <= '\uff5e':
			b.WriteRune(r - 0xfee0)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// NormalizeUsername validates a staff display name (1–6 chars, Han, letters
// and digits) and returns the display form plus the case-folded login key.
func NormalizeUsername(raw string) (display, login string, err error) {
	display = NFKC(raw)
	n := len([]rune(display))
	if n < 1 || n > 6 {
		return "", "", errors.New("用户名需为 1–6 个字符")
	}
	for _, r := range display {
		if !(unicode.Is(unicode.Han, r) || unicode.IsLetter(r) || unicode.IsDigit(r)) {
			return "", "", errors.New("用户名只能包含中文、字母和数字")
		}
		if unicode.IsSpace(r) {
			return "", "", errors.New("用户名不能包含空格")
		}
	}
	login = strings.ToLower(display)
	return display, login, nil
}

// NormalizeName validates a person name used as student display name.
func NormalizeName(raw string) (string, error) {
	s := NFKC(raw)
	n := len([]rune(s))
	if n < 2 || n > 20 {
		return "", errors.New("姓名需为 2–20 个字符")
	}
	for _, r := range s {
		ok := unicode.Is(unicode.Han, r) || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			r == ' ' || r == '·' || r == '•' || unicode.IsLetter(r)
		if !ok {
			return "", errors.New("姓名只能包含中文、字母、空格和间隔号")
		}
	}
	return s, nil
}

// NormalizeStudentNo validates the globally unique student number.
func NormalizeStudentNo(raw string) (display, norm string, err error) {
	display = strings.ToUpper(NFKC(halfWidth(raw)))
	if len(display) < 4 || len(display) > 32 {
		return "", "", errors.New("学号需为 4–32 位字母、数字、- 或 _")
	}
	for _, r := range display {
		if !((r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			return "", "", errors.New("学号只能包含字母、数字、- 和 _")
		}
	}
	return display, display, nil
}

// NormalizePhone validates a mainland China mobile number.
func NormalizePhone(raw string) (string, error) {
	s := halfWidth(NFKC(raw))
	s = strings.NewReplacer(" ", "", "-", "", "\u00a0", "").Replace(s)
	if len(s) != 11 || s[0] != '1' || s[1] < '3' || s[1] > '9' {
		return "", errors.New("手机号需为 11 位中国大陆手机号")
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return "", errors.New("手机号需为 11 位中国大陆手机号")
		}
	}
	return s, nil
}

// MaskPhone renders 123******12.
func MaskPhone(phone string) string {
	if len(phone) != 11 {
		return "***********"
	}
	return phone[:3] + "******" + phone[9:]
}

// ValidatePassword enforces 5–19 ASCII graphic characters for normal users and
// 1–19 for administrators.
func ValidatePassword(pw string, admin bool) error {
	n := len([]rune(pw))
	min := 5
	if admin {
		min = 1
	}
	if n < min || n > 19 {
		return fmt.Errorf("密码需为 %d–19 个字符", min)
	}
	for _, r := range pw {
		if r < 0x21 || r > 0x7e {
			return errors.New("密码只能包含字母、数字和英文符号，不能有空格")
		}
	}
	return nil
}

// SplitDorm parses 楼栋-房间 (e.g. 21-601).
func SplitDorm(raw string) (building, room string, err error) {
	s := strings.TrimSpace(halfWidth(NFKC(raw)))
	i := strings.Index(s, "-")
	if i <= 0 || i == len(s)-1 || strings.Count(s, "-") != 1 {
		return "", "", errors.New("宿舍号格式应为 楼栋-房间，例如 21-601")
	}
	building, room = strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:])
	if err := validBuildingPart(building); err != nil {
		return "", "", err
	}
	if err := validRoomPart(room); err != nil {
		return "", "", err
	}
	return building, room, nil
}

func validBuildingPart(s string) error {
	if s == "" || len([]rune(s)) > 20 {
		return errors.New("宿舍楼栋无效")
	}
	for _, r := range s {
		if !(unicode.Is(unicode.Han, r) || unicode.IsLetter(r) || unicode.IsDigit(r)) {
			return errors.New("宿舍楼栋只能包含中文、字母和数字")
		}
	}
	return nil
}

func validRoomPart(s string) error {
	if s == "" || len(s) > 20 {
		return errors.New("宿舍房间号无效")
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			return errors.New("宿舍房间号只能包含字母和数字")
		}
	}
	return nil
}

// DormString joins building and room.
func DormString(building, room string) string { return building + "-" + room }

// NormalizeBuilding normalizes a community building name.
func NormalizeBuilding(raw string) (string, error) {
	s := strings.ReplaceAll(halfWidth(NFKC(raw)), " ", "")
	if s == "" || len([]rune(s)) > 30 {
		return "", errors.New("楼栋名需为 1–30 个字符")
	}
	for _, r := range s {
		if !(unicode.Is(unicode.Han, r) || unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_') {
			return "", errors.New("楼栋名只能包含中文、字母、数字、- 和 _")
		}
	}
	return s, nil
}

// NormalizeClassName validates a class display name and returns the unique key.
func NormalizeClassName(raw string) (display, key string, err error) {
	display = NFKC(raw)
	n := len([]rune(display))
	if n < 2 || n > 50 {
		return "", "", errors.New("班级名称需为 2–50 个字符")
	}
	key = strings.ToLower(strings.Join(strings.Fields(display), " "))
	return display, key, nil
}

// NormalizeDepartment validates the department field.
func NormalizeDepartment(raw string) (string, error) {
	s := NFKC(raw)
	n := len([]rune(s))
	if n < 1 || n > 50 {
		return "", errors.New("专业系需为 1–50 个字符")
	}
	for _, r := range s {
		if !(unicode.Is(unicode.Han, r) || unicode.IsLetter(r) || unicode.IsDigit(r) ||
			unicode.IsSpace(r) || strings.ContainsRune("-&_()（）·", r)) {
			return "", errors.New("专业系只能包含中文、字母、数字、空格及 - & _ ( ) ·")
		}
	}
	return s, nil
}

// ValidateReason checks the free-text leave reason (5–500 chars).
func ValidateReason(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	n := len([]rune(s))
	if n < 5 || n > 500 {
		return "", errors.New("请假原因需为 5–500 个字符")
	}
	return s, nil
}

// ValidateOpinion checks optional approver opinions (≤500 chars).
func ValidateOpinion(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if len([]rune(s)) > 500 {
		return "", errors.New("意见不能超过 500 个字符")
	}
	return s, nil
}

// ValidateRejectReason checks optional reject/cancel reasons (≤200 chars).
func ValidateRejectReason(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if len([]rune(s)) > 200 {
		return "", errors.New("原因不能超过 200 个字符")
	}
	return s, nil
}

// NormalizeContactPhone validates the optional contact phone snapshot.
func NormalizeContactPhone(raw string) (string, error) {
	s := strings.TrimSpace(NFKC(raw))
	if s == "" {
		return "", nil
	}
	if len(s) < 5 || len(s) > 20 {
		return "", errors.New("联系电话需为 5–20 位")
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || r == '+' || r == '-' || r == ' ') {
			return "", errors.New("联系电话只能包含数字、+、- 和空格")
		}
	}
	return s, nil
}

// CSVSafe neutralizes spreadsheet formula injection.
func CSVSafe(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}

// UsernameSuggestions builds three alternatives for a taken username.
func UsernameSuggestions(base string) []string {
	rs := []rune(base)
	out := make([]string, 0, 3)
	for i := 1; i <= 3; i++ {
		suffix := fmt.Sprintf("%d", i)
		keep := rs
		if len(keep)+1 > 6 {
			keep = keep[:5]
		}
		out = append(out, string(keep)+suffix)
	}
	return out
}
