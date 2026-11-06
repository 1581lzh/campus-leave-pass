package app

import (
	"bytes"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"

	"leavepass/internal/secure"
)

var importHeaders = []string{"学生姓名", "学生学号", "学生账号密码", "学生班级", "学生专业系", "学生班主任", "学生辅导员", "学生宿舍号"}

func importRowFor(c Class, name, studentNo, password, dorm string) []string {
	return []string{name, studentNo, password, c.Name, c.Department, c.TeacherName, c.CounselorName, dorm}
}

// buildImportXLSX creates an in-memory workbook with the 学生名单 sheet.
func buildImportXLSX(t *testing.T, rows [][]string) []byte {
	t.Helper()
	f := excelize.NewFile()
	t.Cleanup(func() { _ = f.Close() })
	if err := f.SetSheetName("Sheet1", "学生名单"); err != nil {
		t.Fatalf("SetSheetName: %v", err)
	}
	writeRows(t, f, rows)
	buf, err := f.WriteToBuffer()
	if err != nil {
		t.Fatalf("WriteToBuffer: %v", err)
	}
	return buf.Bytes()
}

func writeRows(t *testing.T, f *excelize.File, rows [][]string) {
	t.Helper()
	for r, row := range rows {
		for c, value := range row {
			cell, err := excelize.CoordinatesToCellName(c+1, r+1)
			if err != nil {
				t.Fatalf("CoordinatesToCellName: %v", err)
			}
			if err := f.SetCellValue("学生名单", cell, value); err != nil {
				t.Fatalf("SetCellValue(%s): %v", cell, err)
			}
		}
	}
}

func TestImportStudentsSuccess(t *testing.T) {
	s, clock, admin := newTestStore(t)
	teacher := addStaff(t, s, "teacher", "T1", "13960000001")
	counselor := addStaff(t, s, "counselor", "C1", "13960000002")
	c := makeClass(t, s, admin, teacher, counselor, "计算机2401班")

	data := buildImportXLSX(t, [][]string{
		importHeaders,
		importRowFor(c, "张三", "20240001", "Passw0rd", "21-601"),
		importRowFor(c, "李四", "20240002", "Passw0rd", "21-602"),
		// 四个必填列全空的行应跳过。
		{"", "", "", c.Name, c.Department, c.TeacherName, c.CounselorName, ""},
	})
	res, errs, err := s.ImportStudents(ctx(), teacher, c.ID, "students.xlsx", data, "127.0.0.1")
	if err != nil {
		t.Fatalf("ImportStudents: %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("意外行错误: %v", errs)
	}
	if res.Created != 2 || res.BatchID == 0 {
		t.Fatalf("导入结果 = %+v", res)
	}

	u, err := s.GetUserByStudentNo(ctx(), "20240001")
	if err != nil {
		t.Fatalf("GetUserByStudentNo: %v", err)
	}
	if u.Role != "student" || u.Status != "active" || !u.PasswordIsInitial {
		t.Fatalf("学生字段 = %+v", u)
	}
	if !secure.CheckPassword(u.PasswordHash, "Passw0rd") {
		t.Fatal("学生初始密码未正确哈希")
	}
	st, err := s.GetStudent(ctx(), u.ID)
	if err != nil {
		t.Fatalf("GetStudent: %v", err)
	}
	if st.ClassID != c.ID || st.DormBuilding != "21" || st.DormRoom != "601" {
		t.Fatalf("学生班级/宿舍 = %+v", st)
	}
	if st.ImportedAt != clock.Unix() {
		t.Fatalf("导入时间 = %d，期望 %d", st.ImportedAt, clock.Unix())
	}
	// 姓名重复允许共存（学号唯一）。
	if _, err := s.GetUserByStudentNo(ctx(), "20240002"); err != nil {
		t.Fatalf("第二个学生: %v", err)
	}

	batches, err := s.ListImportBatchesForClass(ctx(), c.ID)
	if err != nil {
		t.Fatalf("ListImportBatchesForClass: %v", err)
	}
	if len(batches) != 1 {
		t.Fatalf("批次数量 = %d", len(batches))
	}
	b := batches[0]
	if b.Status != "success" || b.CreatedCount != 2 || b.ErrorCount != 0 || b.TotalRows != 2 {
		t.Fatalf("批次 = %+v", b)
	}
	if countNotifications(t, s, teacher.ID, "students_imported") != 1 ||
		countNotifications(t, s, counselor.ID, "students_imported") != 1 {
		t.Fatal("导入成功后应通知班主任和辅导员")
	}
}

func TestImportStudentsAllOrNothingOnRowError(t *testing.T) {
	s, _, admin := newTestStore(t)
	teacher := addStaff(t, s, "teacher", "T2", "13960000011")
	counselor := addStaff(t, s, "counselor", "C2", "13960000012")
	c := makeClass(t, s, admin, teacher, counselor, "计算机2402班")

	data := buildImportXLSX(t, [][]string{
		importHeaders,
		importRowFor(c, "王五", "20240011", "Passw0rd", "21-701"),
		importRowFor(c, "赵六", "20240012", "Passw0rd", "21702"), // 缺横线
	})
	res, errs, err := s.ImportStudents(ctx(), teacher, c.ID, "bad.xlsx", data, "127.0.0.1")
	if err != nil {
		t.Fatalf("行错误应通过 errs 返回而不是 err: %v", err)
	}
	if res.Created != 0 {
		t.Fatalf("全有或全无失败，创建了 %d 条", res.Created)
	}
	if len(errs) == 0 {
		t.Fatal("应返回行错误")
	}
	if _, err := s.GetUserByStudentNo(ctx(), "20240011"); err != ErrNotFound {
		t.Fatalf("错误行导致整批应回滚，合法行却已创建: %v", err)
	}
	batches, err := s.ListImportBatchesForClass(ctx(), c.ID)
	if err != nil {
		t.Fatalf("ListImportBatchesForClass: %v", err)
	}
	if len(batches) != 1 {
		t.Fatalf("批次数量 = %d", len(batches))
	}
	if batches[0].Status != "failed" || batches[0].CreatedCount != 0 || batches[0].ErrorCount == 0 {
		t.Fatalf("失败批次 = %+v", batches[0])
	}
}

func TestImportStudentsDuplicateStudentNoInSameFile(t *testing.T) {
	s, _, admin := newTestStore(t)
	teacher := addStaff(t, s, "teacher", "T3", "13960000021")
	counselor := addStaff(t, s, "counselor", "C3", "13960000022")
	c := makeClass(t, s, admin, teacher, counselor, "计算机2403班")

	data := buildImportXLSX(t, [][]string{
		importHeaders,
		importRowFor(c, "学生甲", "20240021", "Passw0rd", "21-801"),
		importRowFor(c, "学生乙", "20240021", "Passw0rd", "21-802"),
	})
	res, errs, err := s.ImportStudents(ctx(), teacher, c.ID, "dup.xlsx", data, "127.0.0.1")
	if err != nil {
		t.Fatalf("ImportStudents: %v", err)
	}
	if res.Created != 0 || len(errs) == 0 {
		t.Fatalf("同文件学号重复应整批拒绝: res=%+v errs=%v", res, errs)
	}
	if !strings.Contains(errs[0].Message, "重复") {
		t.Fatalf("错误信息 = %+v", errs)
	}
}

func TestImportStudentsRejectsBadFiles(t *testing.T) {
	s, _, admin := newTestStore(t)
	teacher := addStaff(t, s, "teacher", "T4", "13960000031")
	counselor := addStaff(t, s, "counselor", "C4", "13960000032")
	c := makeClass(t, s, admin, teacher, counselor, "计算机2404班")

	t.Run("非 XLSX 字节", func(t *testing.T) {
		if _, _, err := s.ImportStudents(ctx(), teacher, c.ID, "x.xlsx", []byte("not an xlsx"), "127.0.0.1"); err == nil ||
			!strings.Contains(err.Error(), "无法解析") {
			t.Fatalf("错误 = %v", err)
		}
	})
	t.Run("缺少学生名单工作表", func(t *testing.T) {
		f := excelize.NewFile()
		defer f.Close()
		_ = f.SetCellValue("Sheet1", "A1", "x")
		buf, err := f.WriteToBuffer()
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.ImportStudents(ctx(), teacher, c.ID, "x.xlsx", buf.Bytes(), "127.0.0.1"); err == nil ||
			!strings.Contains(err.Error(), "学生名单") {
			t.Fatalf("错误 = %v", err)
		}
	})
	t.Run("公式单元格", func(t *testing.T) {
		f := excelize.NewFile()
		defer f.Close()
		_ = f.SetSheetName("Sheet1", "学生名单")
		writeRows(t, f, [][]string{importHeaders, importRowFor(c, "公式学生", "20240041", "Passw0rd", "21-901")})
		if err := f.SetCellFormula("学生名单", "B2", "1+1"); err != nil {
			t.Fatalf("SetCellFormula: %v", err)
		}
		buf, err := f.WriteToBuffer()
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = s.ImportStudents(ctx(), teacher, c.ID, "formula.xlsx", buf.Bytes(), "127.0.0.1")
		if err == nil || !strings.Contains(err.Error(), "公式") {
			t.Fatalf("错误 = %v", err)
		}
	})
	t.Run("超过 500 行", func(t *testing.T) {
		rows := make([][]string, 0, 502)
		rows = append(rows, importHeaders)
		for i := 0; i < 501; i++ {
			no := "D" + strings.Repeat("0", 7) + string(rune('A'+i%26)) + string(rune('A'+(i/26)%26))
			rows = append(rows, importRowFor(c, "批量学生", no, "Passw0rd", "21-1001"))
		}
		// 上面可能产生重复学号，但解析层会先触发 500 行上限。
		data := buildImportXLSX(t, rows)
		if _, _, err := s.ImportStudents(ctx(), teacher, c.ID, "big.xlsx", data, "127.0.0.1"); err == nil ||
			!strings.Contains(err.Error(), "500") {
			t.Fatalf("错误 = %v", err)
		}
		if n := countRows(t, s, `SELECT COUNT(*) FROM student_import_batches WHERE class_id=?`, c.ID); n != 0 {
			t.Fatalf("解析失败不应写入批次记录: %d", n)
		}
	})
	t.Run("没有数据行", func(t *testing.T) {
		data := buildImportXLSX(t, [][]string{importHeaders})
		if _, _, err := s.ImportStudents(ctx(), teacher, c.ID, "empty.xlsx", data, "127.0.0.1"); err == nil ||
			!strings.Contains(err.Error(), "没有可导入") {
			t.Fatalf("错误 = %v", err)
		}
	})
}

func TestImportStudentsPermissionAndTemplateAvailability(t *testing.T) {
	s, _, admin := newTestStore(t)
	teacher := addStaff(t, s, "teacher", "T5", "13960000041")
	counselor := addStaff(t, s, "counselor", "C5", "13960000042")
	other := addStaff(t, s, "teacher", "T6", "13960000043")
	c := makeClass(t, s, admin, teacher, counselor, "计算机2405班")

	data := buildImportXLSX(t, [][]string{importHeaders, importRowFor(c, "学生丙", "20240051", "Passw0rd", "21-1101")})
	if _, _, err := s.ImportStudents(ctx(), other, c.ID, "x.xlsx", data, "127.0.0.1"); err == nil ||
		!strings.Contains(err.Error(), "本班当前班主任或辅导员") {
		t.Fatalf("非本班导入错误 = %v", err)
	}
	if _, err := s.ImportTemplate(ctx(), other, c.ID); err == nil ||
		!strings.Contains(err.Error(), "本班当前班主任或辅导员") {
		t.Fatalf("非本班模板错误 = %v", err)
	}

	tpl, err := s.ImportTemplate(ctx(), teacher, c.ID)
	if err != nil {
		t.Fatalf("ImportTemplate: %v", err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(tpl), excelize.Options{})
	if err != nil {
		t.Fatalf("打开模板: %v", err)
	}
	defer f.Close()
	if idx, err := f.GetSheetIndex("学生名单"); err != nil || idx < 0 {
		t.Fatalf("模板缺少学生名单工作表: idx=%d err=%v", idx, err)
	}
	rows, err := f.GetRows("学生名单")
	if err != nil {
		t.Fatalf("读取模板: %v", err)
	}
	if len(rows) < 2 || len(rows[0]) != 8 {
		t.Fatalf("模板表头/预填行异常: %d 行，首行 %d 列", len(rows), len(rows[0]))
	}
	guideRows, err := f.GetRows("填写说明")
	if err != nil {
		t.Fatalf("读取填写说明: %v", err)
	}
	var guide strings.Builder
	for _, row := range guideRows {
		guide.WriteString(strings.Join(row, ""))
		guide.WriteString("\n")
	}
	for _, want := range []string{"工作表标签", "学生名单", "第 2 行开始", "不要在本「填写说明」页填写"} {
		if !strings.Contains(guide.String(), want) {
			t.Fatalf("填写说明缺少 %q:\n%s", want, guide.String())
		}
	}

	// 缺辅导员时模板和导入都必须明确拒绝并说明原因。换一位教师拥有该班，
	// 避免触发“一个班主任只能有一个班”的换班确认。
	teacher7 := addStaff(t, s, "teacher", "T7", "13960000044")
	onlyTeacherClass := makeClass(t, s, admin, teacher7, User{}, "计算机2406班")
	if _, err := s.ImportTemplate(ctx(), teacher7, onlyTeacherClass.ID); err == nil ||
		!strings.Contains(err.Error(), "尚未配齐") {
		t.Fatalf("缺辅导员模板错误 = %v", err)
	}
	if _, _, err := s.ImportStudents(ctx(), teacher7, onlyTeacherClass.ID, "x.xlsx", data, "127.0.0.1"); err == nil ||
		!strings.Contains(err.Error(), "尚未配齐") {
		t.Fatalf("缺辅导员导入错误 = %v", err)
	}
}
