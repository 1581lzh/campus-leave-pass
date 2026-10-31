package app

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/xuri/excelize/v2"

	"leavepass/internal/secure"
	"leavepass/internal/validate"
)

// ImportError is one row/column validation failure.
type ImportError struct {
	Row     int
	Column  string
	Message string
}

func (e ImportError) String() string {
	return fmt.Sprintf("第 %d 行「%s」：%s", e.Row, e.Column, e.Message)
}

// ImportResult reports a fully successful import.
type ImportResult struct {
	BatchID int64
	Created int
}

type importRow struct {
	Row                                               int
	Name, StudentNo, Password, Dorm                   string
	ClassName, Department, TeacherName, CounselorName string
}

// ImportTemplate builds the XLSX template for a class.
func (s *Store) ImportTemplate(ctx context.Context, actor User, classID int64) ([]byte, error) {
	c, err := s.GetClass(ctx, classID)
	if err != nil {
		return nil, err
	}
	if !canImport(actor, c) {
		return nil, errors.New("无法下载模板：只有本班当前班主任或辅导员可以导入学生")
	}
	if c.TeacherUserID == 0 || c.CounselorUserID == 0 {
		return nil, errors.New("无法下载模板：本班尚未配齐班主任和辅导员")
	}
	f := excelize.NewFile()
	defer f.Close()
	const guide = "填写说明"
	const list = "学生名单"
	if err := f.SetSheetName("Sheet1", guide); err != nil {
		return nil, err
	}
	if _, err := f.NewSheet(list); err != nil {
		return nil, err
	}
	lines := []string{
		"校园请假通行系统 · 学生导入模板",
		"",
		"1. 只需填写第 1、2、3、8 列：学生姓名、学生学号、学生账号密码、学生宿舍号。",
		"2. 第 4–7 列（班级、专业系、班主任、辅导员）由系统按当前班级预填，请勿修改。",
		"3. 姓名 2–20 字；学号 4–32 位字母/数字/-/_（统一转大写后全局唯一）；密码 5–19 位；宿舍格式 楼栋-房间，例如 21-601。",
		"4. 姓名、学号、密码、宿舍四列全空的行视为空行自动跳过。",
		"5. 模板默认预填 100 行，最多可导入 500 行；导入为全有或全无，任一行错误则不会创建任何账号。",
		"6. 初始密码由老师通过安全渠道分发给学生，并提醒学生首次登录后尽快修改。",
		"7. 若班级班主任或辅导员发生变化，请重新下载模板，旧模板会因第 4–7 列不一致而失效。",
	}
	for i, line := range lines {
		cell := fmt.Sprintf("A%d", i+1)
		if err := f.SetCellValue(guide, cell, line); err != nil {
			return nil, err
		}
	}
	_ = f.SetColWidth(guide, "A", "A", 110)
	headers := []string{"学生姓名", "学生学号", "学生账号密码", "学生班级", "学生专业系", "学生班主任", "学生辅导员", "学生宿舍号"}
	for i, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		if err := f.SetCellValue(list, cell, h); err != nil {
			return nil, err
		}
	}
	prefill := []string{"", "", "", c.Name, c.Department, c.TeacherName, c.CounselorName, ""}
	for row := 2; row <= 101; row++ {
		for col := 4; col <= 7; col++ {
			cell, _ := excelize.CoordinatesToCellName(col, row)
			if err := f.SetCellValue(list, cell, prefill[col-1]); err != nil {
				return nil, err
			}
		}
	}
	_ = f.SetColWidth(list, "A", "A", 16)
	_ = f.SetColWidth(list, "B", "B", 16)
	_ = f.SetColWidth(list, "C", "C", 18)
	_ = f.SetColWidth(list, "D", "D", 20)
	_ = f.SetColWidth(list, "E", "E", 20)
	_ = f.SetColWidth(list, "F", "F", 14)
	_ = f.SetColWidth(list, "G", "G", 14)
	_ = f.SetColWidth(list, "H", "H", 14)
	buf, err := f.WriteToBuffer()
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func canImport(actor User, c Class) bool {
	if actor.Role == "teacher" && c.TeacherUserID == actor.ID {
		return true
	}
	if actor.Role == "counselor" && c.CounselorUserID == actor.ID {
		return true
	}
	return false
}

// ImportStudents validates and imports an XLSX file atomically.
func (s *Store) ImportStudents(ctx context.Context, actor User, classID int64, fileName string, data []byte, ip string) (ImportResult, []ImportError, error) {
	c, err := s.GetClass(ctx, classID)
	if err != nil {
		return ImportResult{}, nil, ErrNotFound
	}
	if !canImport(actor, c) {
		return ImportResult{}, nil, errors.New("无法导入：只有本班当前班主任或辅导员可以导入学生")
	}
	if c.TeacherUserID == 0 || c.CounselorUserID == 0 {
		return ImportResult{}, nil, errors.New("无法导入：本班尚未配齐班主任和辅导员")
	}
	rows, err := parseImportXLSX(data)
	if err != nil {
		return ImportResult{}, nil, err
	}
	var errs []ImportError
	seen := map[string]bool{}
	resolved := make([]importRow, 0, len(rows))
	for _, r := range rows {
		name := validate.NFKC(r.Name)
		noDisplay, noNorm, eNo := validate.NormalizeStudentNo(r.StudentNo)
		_, eName := validate.NormalizeName(name)
		ePw := validate.ValidatePassword(r.Password, false)
		building, room, eDorm := validate.SplitDorm(r.Dorm)
		var cellErrs []ImportError
		if eName != nil {
			cellErrs = append(cellErrs, ImportError{Row: r.Row, Column: "学生姓名", Message: eName.Error()})
		}
		if eNo != nil {
			cellErrs = append(cellErrs, ImportError{Row: r.Row, Column: "学生学号", Message: eNo.Error()})
		} else if seen[noNorm] {
			cellErrs = append(cellErrs, ImportError{Row: r.Row, Column: "学生学号", Message: "同一文件内学号重复"})
		}
		if ePw != nil {
			cellErrs = append(cellErrs, ImportError{Row: r.Row, Column: "学生账号密码", Message: ePw.Error()})
		}
		if eDorm != nil {
			cellErrs = append(cellErrs, ImportError{Row: r.Row, Column: "学生宿舍号", Message: eDorm.Error()})
		}
		if validate.NFKC(r.ClassName) != c.Name || validate.NFKC(r.Department) != c.Department ||
			validate.NFKC(r.TeacherName) != c.TeacherName || validate.NFKC(r.CounselorName) != c.CounselorName {
			cellErrs = append(cellErrs, ImportError{Row: r.Row, Column: "第 4–7 列", Message: "班级/专业系/班主任/辅导员与当前班级不一致，请重新下载模板"})
		}
		if len(cellErrs) > 0 {
			errs = append(errs, cellErrs...)
			continue
		}
		seen[noNorm] = true
		resolved = append(resolved, importRow{
			Row: r.Row, Name: name, StudentNo: noDisplay, Password: r.Password,
			Dorm: validate.DormString(building, room), ClassName: c.Name, Department: c.Department,
			TeacherName: c.TeacherName, CounselorName: c.CounselorName,
		})
	}
	if len(errs) > 0 {
		s.recordImportBatch(ctx, classID, actor.ID, fileName, len(rows), 0, len(errs), "failed", ip)
		return ImportResult{}, errs, nil
	}
	// Hash passwords concurrently before taking the write lock.
	type hashed struct {
		row  importRow
		hash string
		b, r string
	}
	hashes := make([]hashed, len(resolved))
	workers := 4
	if len(resolved) < workers {
		workers = len(resolved)
	}
	var wg sync.WaitGroup
	jobs := make(chan int)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				h, err := hashPassword(resolved[i].Password)
				if err != nil {
					continue
				}
				b, r, _ := validate.SplitDorm(resolved[i].Dorm)
				hashes[i] = hashed{row: resolved[i], hash: h, b: b, r: r}
			}
		}()
	}
	for i := range resolved {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	for _, h := range hashes {
		if h.hash == "" {
			return ImportResult{}, nil, errors.New("密码哈希失败，请重试")
		}
	}
	now := s.NowUnix()
	var batchID int64
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		var teacher, counselor int64
		var liveName, dept string
		err := tx.QueryRowContext(ctx, `SELECT COALESCE(teacher_user_id,0), COALESCE(counselor_user_id,0), name, department
			FROM classes WHERE id=?`, classID).Scan(&teacher, &counselor, &liveName, &dept)
		if err != nil {
			return errors.New("班级已解散或不存在")
		}
		live, err := scanUser(tx.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id=?`, actor.ID))
		if err != nil || live.Status != "active" {
			return errors.New("导入人账号不可用")
		}
		if (actor.Role == "teacher" && teacher != actor.ID) || (actor.Role == "counselor" && counselor != actor.ID) {
			return errors.New("班级班主任/辅导员已变化，请刷新后重试")
		}
		if teacher == 0 || counselor == 0 {
			return errors.New("本班尚未配齐班主任和辅导员")
		}
		if liveName != c.Name || dept != c.Department || c.TeacherUserID != teacher || c.CounselorUserID != counselor {
			return errors.New("班级信息已变化，请重新下载模板")
		}
		for _, h := range hashes {
			var exists int64
			if err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE student_no_norm=?`, h.row.StudentNo).Scan(&exists); err == nil {
				return fmt.Errorf("学号 %s 已存在（含已停用账号），请联系管理员清理", h.row.StudentNo)
			}
			res, err := tx.ExecContext(ctx, `INSERT INTO users (username,username_norm,student_no,student_no_norm,password_hash,
				role,status,password_is_initial,created_by,created_at,updated_at)
				VALUES (?,?,?,?,?, 'student', 'active', 1, ?,?,?)`,
				h.row.Name, strings.ToLower(h.row.Name), h.row.StudentNo, h.row.StudentNo, h.hash, actor.ID, now, now)
			if err != nil {
				if strings.Contains(err.Error(), "UNIQUE") {
					return fmt.Errorf("学号 %s 已存在，请刷新后重试", h.row.StudentNo)
				}
				return err
			}
			uid, _ := res.LastInsertId()
			if _, err := tx.ExecContext(ctx, `INSERT INTO student_classes
				(user_id,class_id,dorm_building,dorm_room,imported_by,imported_at,joined_at) VALUES (?,?,?,?,?,?,?)`,
				uid, classID, h.b, h.r, actor.ID, now, now); err != nil {
				return err
			}
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO student_import_batches
			(class_id,imported_by,file_name,total_rows,created_count,error_count,status,ip,created_at)
			VALUES (?,?,?,?,?,0,'success',?,?)`, classID, actor.ID, sanitizeFileName(fileName), len(rows), len(hashes), ip, now)
		if err != nil {
			return err
		}
		batchID, _ = res.LastInsertId()
		_ = s.Notify(ctx, tx, teacher, "students_imported", "学生导入成功", fmt.Sprintf("已导入 %d 名学生到 %s。", len(hashes), liveName), "/t/classes/"+itoa(classID)+"/students", 0)
		_ = s.Notify(ctx, tx, counselor, "students_imported", "学生导入成功", fmt.Sprintf("已导入 %d 名学生到 %s。", len(hashes), liveName), "/t/classes/"+itoa(classID)+"/students", 0)
		return nil
	})
	if err != nil {
		s.recordImportBatch(ctx, classID, actor.ID, fileName, len(rows), 0, len(rows), "failed", ip)
		return ImportResult{}, nil, err
	}
	return ImportResult{BatchID: batchID, Created: len(hashes)}, nil, nil
}

func hashPassword(pw string) (string, error) {
	return secure.HashPassword(pw)
}

func sanitizeFileName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if len(name) > 200 {
		name = name[:200]
	}
	return name
}

func (s *Store) recordImportBatch(ctx context.Context, classID, importer int64, fileName string, total, created, errCount int, status, ip string) {
	_, _ = s.DB.ExecContext(ctx, `INSERT INTO student_import_batches
		(class_id,imported_by,file_name,total_rows,created_count,error_count,status,ip,created_at)
		VALUES (?,?,?,?,?,?,?,?,?)`, nullableID(classID), nullableID(importer), sanitizeFileName(fileName), total, created, errCount, status, ip, s.NowUnix())
}

// parseImportXLSX reads the 学生名单 sheet, rejecting formulas and bad shapes.
func parseImportXLSX(data []byte) ([]importRow, error) {
	f, err := excelize.OpenReader(bytes.NewReader(data), excelize.Options{UnzipSizeLimit: 64 << 20, UnzipXMLSizeLimit: 16 << 20})
	// excelize can return a partially opened file together with an error (for
	// example a malformed calcChain). Close it in that case too, otherwise its
	// temporary extraction files remain on disk.
	if f != nil {
		defer f.Close()
	}
	if err != nil {
		return nil, errors.New("无法解析 XLSX 文件，请使用系统生成的模板")
	}
	idx, err := f.GetSheetIndex("学生名单")
	if err != nil || idx < 0 {
		return nil, errors.New("模板缺少“学生名单”工作表，请重新下载模板")
	}
	rows, err := f.GetRows("学生名单")
	if err != nil {
		return nil, errors.New("读取工作表失败")
	}
	if len(rows) > 501 {
		return nil, errors.New("单次最多导入 500 行")
	}
	var out []importRow
	for i := 1; i < len(rows); i++ {
		rowNo := i + 1
		get := func(col int) string {
			if i < len(rows) && col < len(rows[i]) {
				return strings.TrimSpace(rows[i][col])
			}
			return ""
		}
		name, no, pw, className := get(0), get(1), get(2), get(3)
		dept, teacher, counselor, dorm := get(4), get(5), get(6), get(7)
		if name == "" && no == "" && pw == "" && dorm == "" {
			continue
		}
		for col := 1; col <= 8; col++ {
			cell, _ := excelize.CoordinatesToCellName(col, rowNo)
			if formula, _ := f.GetCellFormula("学生名单", cell); strings.TrimSpace(formula) != "" {
				return nil, fmt.Errorf("第 %d 行第 %d 列包含公式单元格，导入不允许公式", rowNo, col)
			}
		}
		if name == "" || no == "" || pw == "" || dorm == "" || className == "" || dept == "" || teacher == "" || counselor == "" {
			return nil, fmt.Errorf("第 %d 行存在空单元格：8 列均为必填", rowNo)
		}
		out = append(out, importRow{Row: rowNo, Name: name, StudentNo: no, Password: pw, Dorm: dorm,
			ClassName: className, Department: dept, TeacherName: teacher, CounselorName: counselor})
	}
	if len(out) == 0 {
		return nil, errors.New("模板中没有可导入的学生数据")
	}
	if len(out) > 500 {
		return nil, errors.New("单次最多导入 500 行")
	}
	return out, nil
}

// ListImportBatchesForClass lists a class's import history.
func (s *Store) ListImportBatchesForClass(ctx context.Context, classID int64) ([]ImportBatch, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT b.id, COALESCE(b.class_id,0), COALESCE(b.imported_by,0), b.file_name,
		b.total_rows, b.created_count, b.error_count, b.status, b.ip, b.created_at, COALESCE(c.name,''), COALESCE(u.username,'')
		FROM student_import_batches b LEFT JOIN classes c ON c.id=b.class_id LEFT JOIN users u ON u.id=b.imported_by
		WHERE b.class_id=? ORDER BY b.created_at DESC`, classID)
	if err != nil {
		return nil, err
	}
	return scanBatches(rows)
}

// ListAllImportBatches lists every import batch for administrators.
func (s *Store) ListAllImportBatches(ctx context.Context, limit int) ([]ImportBatch, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT b.id, COALESCE(b.class_id,0), COALESCE(b.imported_by,0), b.file_name,
		b.total_rows, b.created_count, b.error_count, b.status, b.ip, b.created_at, COALESCE(c.name,''), COALESCE(u.username,'')
		FROM student_import_batches b LEFT JOIN classes c ON c.id=b.class_id LEFT JOIN users u ON u.id=b.imported_by
		ORDER BY b.created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	return scanBatches(rows)
}

func scanBatches(rows *sql.Rows) ([]ImportBatch, error) {
	defer rows.Close()
	var out []ImportBatch
	for rows.Next() {
		var b ImportBatch
		if err := rows.Scan(&b.ID, &b.ClassID, &b.ImportedBy, &b.FileName, &b.TotalRows, &b.CreatedCount,
			&b.ErrorCount, &b.Status, &b.IP, &b.CreatedAt, &b.ClassName, &b.ImporterName); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
