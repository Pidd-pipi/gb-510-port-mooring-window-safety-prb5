package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/blueship581/port-mooring-window-safety/backend/internal/constants"
	"github.com/blueship581/port-mooring-window-safety/backend/internal/dto"
	"github.com/blueship581/port-mooring-window-safety/backend/internal/model"
	"github.com/blueship581/port-mooring-window-safety/backend/internal/repository"
)

type SafetyClearanceService interface {
	List(context.Context, dto.PageQuery) (repository.Page[model.SafetyClearance], error)
	Get(context.Context, uint) (model.SafetyClearance, error)
	Create(context.Context, dto.CreateSafetyClearance, string, string) (model.SafetyClearance, error)
	Update(context.Context, uint, dto.UpdateSafetyClearance, string, string) (model.SafetyClearance, error)
	Transition(context.Context, uint, dto.TransitionRequest, string, string, string) (model.SafetyClearance, error)
	Delete(context.Context, uint, string, string) error
	StatusCounts(context.Context) (map[string]int64, error)
}

type safetyClearanceService struct {
	repository repository.SafetyClearanceRepository
	plans      repository.MooringPlanRepository
	windows    repository.WeatherWindowRepository
	security   SecurityService
	uow        repository.UnitOfWork
}

func NewSafetyClearanceService(
	repo repository.SafetyClearanceRepository,
	plans repository.MooringPlanRepository,
	windows repository.WeatherWindowRepository,
	security SecurityService,
	uow repository.UnitOfWork,
) SafetyClearanceService {
	return &safetyClearanceService{repository: repo, plans: plans, windows: windows, security: security, uow: uow}
}

func (s *safetyClearanceService) List(ctx context.Context, query dto.PageQuery) (repository.Page[model.SafetyClearance], error) {
	page, err := s.repository.List(ctx, query)
	if err != nil {
		return page, err
	}
	for i := range page.Items {
		page.Items[i].Interlock = s.interlockOf(ctx, &page.Items[i])
	}
	return page, nil
}

func (s *safetyClearanceService) Get(ctx context.Context, id uint) (model.SafetyClearance, error) {
	item, err := s.repository.Get(ctx, id)
	if err != nil {
		return item, err
	}
	item.Interlock = s.interlockOf(ctx, &item)
	return item, nil
}

func (s *safetyClearanceService) Create(ctx context.Context, input dto.CreateSafetyClearance, actor, requestID string) (model.SafetyClearance, error) {
	if err := validateSafetyClearanceBusinessFields(input.Code, input.Name, input.Facility, input.Owner); err != nil {
		return model.SafetyClearance{}, err
	}
	planCode := strings.ToUpper(strings.TrimSpace(input.PlanCode))
	windowCode := strings.ToUpper(strings.TrimSpace(input.WindowCode))
	if planCode == "" || windowCode == "" {
		return model.SafetyClearance{}, ErrInvalidInput
	}
	windowVersion := input.WindowVersion
	if windowVersion == 0 {
		windowVersion = 1
	}
	item := model.SafetyClearance{
		BaseModel: model.BaseModel{
			Code: strings.ToUpper(strings.TrimSpace(input.Code)), Name: strings.TrimSpace(input.Name),
			Status: model.SafetyClearanceInitialStatus, Version: 1, Description: strings.TrimSpace(input.Description),
		},
		Facility: strings.TrimSpace(input.Facility), Owner: strings.TrimSpace(input.Owner),
		Category: strings.TrimSpace(input.Category), RiskLevel: input.RiskLevel,
		MetricValue: input.MetricValue, MetricUnit: strings.TrimSpace(input.MetricUnit),
		EffectiveAt: input.EffectiveAt.UTC(), Evidence: strings.TrimSpace(input.Evidence),
		RelatedCode:   strings.ToUpper(strings.TrimSpace(input.RelatedCode)),
		PlanCode:      planCode,
		WindowCode:    windowCode,
		WindowVersion: windowVersion,
	}
	if err := s.repository.Create(ctx, &item); err != nil {
		return model.SafetyClearance{}, fmt.Errorf("create 安全许可: %w", err)
	}
	_ = s.security.Audit(ctx, actor, requestID, "create", "SafetyClearance", item.ID, "", item.Status, "created 安全许可")
	return s.Get(ctx, item.ID)
}

func (s *safetyClearanceService) Update(ctx context.Context, id uint, input dto.UpdateSafetyClearance, actor, requestID string) (model.SafetyClearance, error) {
	current, err := s.repository.Get(ctx, id)
	if err != nil {
		return model.SafetyClearance{}, err
	}
	if err := validateSafetyClearanceBusinessFields(current.Code, input.Name, input.Facility, input.Owner); err != nil {
		return model.SafetyClearance{}, err
	}
	current.Name = strings.TrimSpace(input.Name)
	current.Description = strings.TrimSpace(input.Description)
	current.Facility = strings.TrimSpace(input.Facility)
	current.Owner = strings.TrimSpace(input.Owner)
	current.Category = strings.TrimSpace(input.Category)
	current.RiskLevel = input.RiskLevel
	current.MetricValue = input.MetricValue
	current.MetricUnit = strings.TrimSpace(input.MetricUnit)
	current.EffectiveAt = input.EffectiveAt.UTC()
	current.Evidence = strings.TrimSpace(input.Evidence)
	current.RelatedCode = strings.ToUpper(strings.TrimSpace(input.RelatedCode))
	rebind := false
	if planCode := strings.ToUpper(strings.TrimSpace(input.PlanCode)); planCode != "" && planCode != current.PlanCode {
		current.PlanCode = planCode
		rebind = true
	}
	if windowCode := strings.ToUpper(strings.TrimSpace(input.WindowCode)); windowCode != "" && windowCode != current.WindowCode {
		current.WindowCode = windowCode
		rebind = true
	}
	if input.WindowVersion > 0 && input.WindowVersion != current.WindowVersion {
		current.WindowVersion = input.WindowVersion
		rebind = true
	}
	if rebind {
		// Rebinding the plan/window pair voids any in-flight two-person confirmation.
		current.SubmittedBy = ""
		current.SubmittedAt = nil
		current.ConfirmedBy = ""
		current.ConfirmedAt = nil
	}
	current.Version = input.ExpectedVersion + 1
	current.UpdatedAt = time.Now().UTC()
	if err := s.repository.Update(ctx, id, input.ExpectedVersion, &current); err != nil {
		return model.SafetyClearance{}, fmt.Errorf("update 安全许可: %w", err)
	}
	_ = s.security.Audit(ctx, actor, requestID, "update", "SafetyClearance", id, current.Status, current.Status, "updated business fields")
	return s.Get(ctx, id)
}

func (s *safetyClearanceService) Transition(ctx context.Context, id uint, input dto.TransitionRequest, actor, role, requestID string) (model.SafetyClearance, error) {
	current, err := s.repository.Get(ctx, id)
	if err != nil {
		return model.SafetyClearance{}, err
	}
	target := strings.TrimSpace(input.Status)
	if !constants.CanTransition(constants.SafetyClearanceTransitions, current.Status, target) {
		return model.SafetyClearance{}, fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, current.Status, target)
	}
	if current.Status == string(constants.ClearanceStatePending) && target == string(constants.ClearanceStateCleared) {
		return s.confirmClearance(ctx, current, input, actor, role, requestID)
	}
	if role != model.RoleReviewer && role != model.RoleAdmin {
		return model.SafetyClearance{}, ErrReviewerRequired
	}
	before := current.Status
	current.Status = target
	current.Version = input.ExpectedVersion + 1
	current.UpdatedAt = time.Now().UTC()
	if err := s.uow.WithinTransaction(ctx, func(tx repository.TxRepositories) error {
		if err := tx.SafetyClearance.Update(ctx, id, input.ExpectedVersion, &current); err != nil {
			return fmt.Errorf("transition 安全许可: %w", err)
		}
		if err := appendAudit(ctx, tx.Security, actor, requestID, "transition", "SafetyClearance", id, before, target, input.Reason, current.WindowVersion); err != nil {
			return fmt.Errorf("persist transition audit: %w", err)
		}
		return nil
	}); err != nil {
		return model.SafetyClearance{}, err
	}
	return s.Get(ctx, id)
}

// confirmClearance runs the two-person safety confirmation. Both the first
// submission and the independent release re-read the bound plan and window
// inside one transaction: the plan must stay approved, the window must stay
// safe and the window version must match the clearance expectation. The
// clearance update and its audit entry commit or roll back together.
func (s *safetyClearanceService) confirmClearance(ctx context.Context, current model.SafetyClearance, input dto.TransitionRequest, actor, role, requestID string) (model.SafetyClearance, error) {
	if input.WindowVersion == 0 {
		return model.SafetyClearance{}, ErrWindowVersion
	}
	now := time.Now().UTC()
	if current.SubmittedBy == "" {
		current.WindowVersion = input.WindowVersion
		current.SubmittedBy = actor
		current.SubmittedAt = &now
		current.Version = input.ExpectedVersion + 1
		current.UpdatedAt = now
		if err := s.uow.WithinTransaction(ctx, func(tx repository.TxRepositories) error {
			if err := enforceClearanceInterlock(ctx, tx, current); err != nil {
				return err
			}
			if err := tx.SafetyClearance.Update(ctx, current.ID, input.ExpectedVersion, &current); err != nil {
				return fmt.Errorf("submit safety confirmation: %w", err)
			}
			if err := appendAudit(ctx, tx.Security, actor, requestID, "clearance_submit", "SafetyClearance", current.ID, current.Status, current.Status, input.Reason, current.WindowVersion); err != nil {
				return fmt.Errorf("persist safety submission audit: %w", err)
			}
			return nil
		}); err != nil {
			return model.SafetyClearance{}, err
		}
		return s.Get(ctx, current.ID)
	}
	if current.WindowVersion != input.WindowVersion {
		return model.SafetyClearance{}, ErrWindowVersion
	}
	if current.SubmittedBy == actor {
		return model.SafetyClearance{}, ErrSelfApproval
	}
	if role != model.RoleReviewer && role != model.RoleAdmin {
		return model.SafetyClearance{}, ErrReviewerRequired
	}
	before := current.Status
	current.Status = string(constants.ClearanceStateCleared)
	current.ConfirmedBy = actor
	current.ConfirmedAt = &now
	current.Version = input.ExpectedVersion + 1
	current.UpdatedAt = now
	if err := s.uow.WithinTransaction(ctx, func(tx repository.TxRepositories) error {
		if err := enforceClearanceInterlock(ctx, tx, current); err != nil {
			return err
		}
		if err := tx.SafetyClearance.Update(ctx, current.ID, input.ExpectedVersion, &current); err != nil {
			return fmt.Errorf("confirm safety clearance: %w", err)
		}
		if err := appendAudit(ctx, tx.Security, actor, requestID, "clearance_confirm", "SafetyClearance", current.ID, before, current.Status, input.Reason, current.WindowVersion); err != nil {
			return fmt.Errorf("persist safety confirmation audit: %w", err)
		}
		return nil
	}); err != nil {
		return model.SafetyClearance{}, err
	}
	return s.Get(ctx, current.ID)
}

func (s *safetyClearanceService) Delete(ctx context.Context, id uint, actor, requestID string) error {
	current, err := s.repository.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := s.repository.Delete(ctx, id); err != nil {
		return err
	}
	return s.security.Audit(ctx, actor, requestID, "delete", "SafetyClearance", id, current.Status, "deleted", "soft deleted 安全许可")
}

func (s *safetyClearanceService) StatusCounts(ctx context.Context) (map[string]int64, error) {
	return s.repository.CountByStatus(ctx)
}

// interlockOf resolves the live plan/window basis for one clearance. Pending
// clearances surface the invalid reason; released history keeps the basis
// snapshot but is never invalidated retroactively.
func (s *safetyClearanceService) interlockOf(ctx context.Context, item *model.SafetyClearance) *model.ClearanceInterlock {
	plan, planErr := s.plans.FindByCode(ctx, item.PlanCode)
	window, windowErr := s.windows.FindByCode(ctx, item.WindowCode)
	interlock := evaluateClearanceInterlock(*item, plan, planErr == nil, window, windowErr == nil)
	if item.Status != string(constants.ClearanceStatePending) {
		interlock.InvalidReason = ""
	}
	return &interlock
}

// enforceClearanceInterlock re-reads the bound plan and window with row locks
// inside the confirmation transaction and rejects the write when the basis no
// longer holds.
func enforceClearanceInterlock(ctx context.Context, tx repository.TxRepositories, item model.SafetyClearance) error {
	plan, planErr := tx.MooringPlan.LockByCode(ctx, item.PlanCode)
	window, windowErr := tx.WeatherWindow.LockByCode(ctx, item.WindowCode)
	interlock := evaluateClearanceInterlock(item, plan, planErr == nil, window, windowErr == nil)
	if interlock.Satisfied {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrClearanceInterlock, interlock.InvalidReason)
}

// evaluateClearanceInterlock is the pure rule shared by the read path and the
// transactional gate: plan approved, window safe, window version unchanged.
func evaluateClearanceInterlock(item model.SafetyClearance, plan model.MooringPlan, planFound bool, window model.WeatherWindow, windowFound bool) model.ClearanceInterlock {
	interlock := model.ClearanceInterlock{
		PlanCode: item.PlanCode, WindowCode: item.WindowCode,
		ExpectedWindowVersion: item.WindowVersion,
	}
	reasons := make([]string, 0, 4)
	switch {
	case !planFound:
		reasons = append(reasons, fmt.Sprintf("系泊方案 %s 不存在", item.PlanCode))
	case plan.Status != constants.MooringPlanStatusApproved:
		reasons = append(reasons, fmt.Sprintf("系泊方案 %s 未批准（当前状态 %s）", plan.Code, plan.Status))
	}
	if planFound {
		interlock.PlanStatus = plan.Status
		interlock.PlanApproved = plan.Status == constants.MooringPlanStatusApproved
	}
	switch {
	case !windowFound:
		reasons = append(reasons, fmt.Sprintf("风浪窗口 %s 不存在", item.WindowCode))
	default:
		interlock.WindowStatus = window.Status
		interlock.WindowSafe = window.Status == constants.WeatherWindowStatusSafe
		interlock.CurrentWindowVersion = window.Version
		interlock.WindowVersionMatch = window.Version == item.WindowVersion
		if !interlock.WindowSafe {
			reasons = append(reasons, fmt.Sprintf("风浪窗口 %s 非安全状态（当前状态 %s）", window.Code, window.Status))
		}
		if !interlock.WindowVersionMatch {
			reasons = append(reasons, fmt.Sprintf("风浪窗口版本已变更（当前 v%d，许可预期 v%d）", window.Version, item.WindowVersion))
		}
	}
	interlock.Satisfied = len(reasons) == 0
	interlock.InvalidReason = strings.Join(reasons, "；")
	return interlock
}

func validateSafetyClearanceBusinessFields(code, name, facility, owner string) error {
	if strings.TrimSpace(code) == "" || strings.TrimSpace(name) == "" || strings.TrimSpace(facility) == "" || strings.TrimSpace(owner) == "" {
		return ErrInvalidInput
	}
	return nil
}
