package app

import (
	"context"
	"errors"
	"fmt"

	"leavepass/internal/validate"
)

// BatchFailure describes one selected item that could not be processed.
// Batch operations commit each item independently so one failure does not
// silently roll back the others; the caller must show both counts.
type BatchFailure struct {
	ID    int64
	Label string
	Err   error
}

// BatchResult is the outcome of a batch action. Succeeded and Skipped hold
// item IDs; Failures hold the per-item reasons shown back to the operator.
type BatchResult struct {
	Succeeded []int64
	Skipped   []int64
	Failures  []BatchFailure
}

// SucceededCount returns how many selected items were changed.
func (r BatchResult) SucceededCount() int { return len(r.Succeeded) }

// SkippedCount returns how many selected items already had the target state.
func (r BatchResult) SkippedCount() int { return len(r.Skipped) }

// FailedCount returns how many selected items could not be changed.
func (r BatchResult) FailedCount() int { return len(r.Failures) }

func uniqueBatchIDs(ids []int64) []int64 {
	seen := make(map[int64]bool, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func batchLabel(prefix string, id int64) string {
	return fmt.Sprintf("%s #%d", prefix, id)
}

// BatchSoftDeleteUsers stops several accounts with the same reason. Each
// item reuses SoftDeleteUser, so self-deletion, the last-admin guard, session
// revocation, class consequences and auditing keep their single-item rules.
func (s *Store) BatchSoftDeleteUsers(ctx context.Context, actor User, ids []int64, reason, ip string) (BatchResult, error) {
	if actor.Role != "admin" {
		return BatchResult{}, ErrForbidden
	}
	if _, err := validate.ValidateRejectReason(reason); err != nil {
		return BatchResult{}, err
	}
	var res BatchResult
	for _, id := range uniqueBatchIDs(ids) {
		label := batchLabel("用户", id)
		if u, err := s.GetUserByID(ctx, id); err == nil {
			label = u.Username
		}
		if err := s.SoftDeleteUser(ctx, actor, id, reason, ip); err != nil {
			res.Failures = append(res.Failures, BatchFailure{ID: id, Label: label, Err: err})
			continue
		}
		res.Succeeded = append(res.Succeeded, id)
	}
	return res, nil
}

// BatchDisbandClasses disbands several classes. DisbandClass already cancels
// pending leaves, notifies students, keeps approved QR codes until they end,
// writes the audit before deleting the row, and leaves student accounts in
// place with no class.
func (s *Store) BatchDisbandClasses(ctx context.Context, actor User, ids []int64, ip string) (BatchResult, error) {
	if actor.Role != "admin" {
		return BatchResult{}, ErrForbidden
	}
	var res BatchResult
	for _, id := range uniqueBatchIDs(ids) {
		label := batchLabel("班级", id)
		if c, err := s.GetClass(ctx, id); err == nil {
			label = c.Name
		}
		if err := s.DisbandClass(ctx, actor, id, ip); err != nil {
			res.Failures = append(res.Failures, BatchFailure{ID: id, Label: label, Err: err})
			continue
		}
		res.Succeeded = append(res.Succeeded, id)
	}
	return res, nil
}

// BatchSetRegistrationCodeActive enables or disables several codes. Codes
// already in the requested state are skipped instead of rewriting disabled_at
// and adding duplicate audit rows.
func (s *Store) BatchSetRegistrationCodeActive(ctx context.Context, actor User, ids []int64, active bool, ip string) (BatchResult, error) {
	if actor.Role != "admin" {
		return BatchResult{}, errors.New("只有管理员可以修改注册码")
	}
	var res BatchResult
	for _, id := range uniqueBatchIDs(ids) {
		c := s.GetRegistrationCode(ctx, id)
		if c.ID == 0 {
			res.Failures = append(res.Failures, BatchFailure{ID: id, Label: batchLabel("注册码", id), Err: ErrNotFound})
			continue
		}
		if c.Active == active {
			res.Skipped = append(res.Skipped, id)
			continue
		}
		if err := s.UpdateRegistrationCode(ctx, actor, id, 0, 0, &active, ip); err != nil {
			res.Failures = append(res.Failures, BatchFailure{ID: id, Label: c.CodeHint, Err: err})
			continue
		}
		res.Succeeded = append(res.Succeeded, id)
	}
	return res, nil
}

// BatchDeleteRegistrationCodes permanently removes several codes. Each item
// reuses DeleteRegistrationCode, which keeps registered accounts and
// registration attempts through ON DELETE SET NULL.
func (s *Store) BatchDeleteRegistrationCodes(ctx context.Context, actor User, ids []int64, ip string) (BatchResult, error) {
	if actor.Role != "admin" {
		return BatchResult{}, errors.New("只有管理员可以删除注册码")
	}
	var res BatchResult
	for _, id := range uniqueBatchIDs(ids) {
		c := s.GetRegistrationCode(ctx, id)
		label := batchLabel("注册码", id)
		if c.ID != 0 {
			label = c.CodeHint
		}
		if err := s.DeleteRegistrationCode(ctx, actor, id, ip); err != nil {
			res.Failures = append(res.Failures, BatchFailure{ID: id, Label: label, Err: err})
			continue
		}
		res.Succeeded = append(res.Succeeded, id)
	}
	return res, nil
}
