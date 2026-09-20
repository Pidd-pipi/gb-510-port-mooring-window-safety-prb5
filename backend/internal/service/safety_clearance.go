package service

import (
	"context"
	"encoding/json"
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
	tx         repository.TxManager
	interlock  *interlockEvaluator
}

func NewSafetyClearanceService(
	repo repository.SafetyClearanceRepository,
	plans repository.MooringPlanRepository,
	windows repository.WeatherWindowRepository,
	security SecurityService,
	tx repository.TxManager,
) SafetyClearanceService {
	return &safetyClearanceService{
		repository: repo, plans: plans, windows: windows, security: security, tx: tx,
		interlock: newInterlockEvaluator(plans, windows),
	}
}

func (s *safetyClearanceService) List(ctx context.Context, query dto.PageQuery) (repository.Page[model.SafetyClearance], error) {
	page, err := s.repository.List(ctx, query)
	if err != nil {
		return page, err
	}
	if err := s.enrich(ctx, page.Items); err != nil {
		return page, err
	}
	return page, nil
}

func (s *safetyClearanceService) Get(ctx context.Context, id uint) (model.SafetyClearance, error) {
	item, err := s.repository.Get(ctx, id)
	if err != nil {
		return model.SafetyClearance{}, err
	}
	enriched := []model.SafetyClearance{item}
	if err := s.enrich(ctx, enriched); err != nil {
		return model.SafetyClearance{}, err
	}
	return enriched[0], nil
}

func (s *safetyClearanceService) Create(ctx context.Context, input dto.CreateSafetyClearance, actor, requestID string) (model.SafetyClearance, error) {
	if err := validateSafetyClearanceBusinessFields(input.Code, input.Name, input.Facility, input.Owner); err != nil {
		return model.SafetyClearance{}, err
	}
	planCode := strings.ToUpper(strings.TrimSpace(input.PlanCode))
	windowCode := strings.ToUpper(strings.TrimSpace(input.WindowCode))
	if planCode == "" || windowCode == "" {
		return model.SafetyClearance{}, ErrInterlockBasisMissing
	}
	// Both bound aggregates must exist when the clearance is recorded. States
	// are not enforced here: a clearance can be prepared while the plan is
	// still in draft, but no submission is accepted until it is approved and
	// the window is safe.
	if _, err := s.interlock.lookupBasis(ctx, planCode, windowCode); err != nil {
		return model.SafetyClearance{}, err
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
		PlanCode: planCode, WindowCode: windowCode,
		RelatedCode:   strings.ToUpper(strings.TrimSpace(input.RelatedCode)),
		WindowVersion: 1,
	}
	if err := s.repository.Create(ctx, &item); err != nil {
		return model.SafetyClearance{}, fmt.Errorf("create 安全许可: %w", err)
	}
	_ = s.security.Audit(ctx, actor, requestID, "create", "SafetyClearance", item.ID, "", item.Status,
		fmt.Sprintf("created 安全许可 bound to plan %s and window %s", planCode, windowCode))
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
	nextPlanCode := strings.ToUpper(strings.TrimSpace(input.PlanCode))
	nextWindowCode := strings.ToUpper(strings.TrimSpace(input.WindowCode))
	if nextPlanCode == "" || nextWindowCode == "" {
		return model.SafetyClearance{}, ErrInterlockBasisMissing
	}
	// A released clearance is a frozen historical decision: its basis can no
	// longer be edited, while already-released history stays readable.
	basisChanged := nextPlanCode != current.PlanCode || nextWindowCode != current.WindowCode
	if current.Status == string(constants.ClearanceStateCleared) && basisChanged {
		return model.SafetyClearance{}, ErrReleasedBasisLocked
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
	if basisChanged {
		// Rebinding resets the two-person workflow: nobody has attested to the
		// new plan/window pair yet. Both aggregates must currently exist; their
		// states are re-validated again on the next submit/release.
		if _, err := s.interlock.lookupBasis(ctx, nextPlanCode, nextWindowCode); err != nil {
			return model.SafetyClearance{}, err
		}
		current.PlanCode = nextPlanCode
		current.WindowCode = nextWindowCode
		current.PlanVersion = 0
		current.WindowVersion = 1
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
	_ = s.security.Audit(ctx, actor, requestID, "update", "SafetyClearance", id, current.Status, current.Status,
		fmt.Sprintf("updated business fields; plan=%s window=%s", current.PlanCode, current.WindowCode))
	return s.Get(ctx, id)
}

func (s *safetyClearanceService) Transition(ctx context.Context, id uint, input dto.TransitionRequest, actor, role, requestID string) (model.SafetyClearance, error) {
	target := strings.TrimSpace(input.Status)
	var result model.SafetyClearance
	err := s.tx.WithinTx(ctx, func(txCtx context.Context) error {
		current, err := s.repository.GetForUpdate(txCtx, id)
		if err != nil {
			return err
		}
		if !constants.CanTransition(constants.SafetyClearanceTransitions, current.Status, target) {
			return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, current.Status, target)
		}
		if current.Status == string(constants.ClearanceStatePending) && target == string(constants.ClearanceStateCleared) {
			updated, err := s.confirmClearance(txCtx, current, input, actor, role, requestID)
			if err != nil {
				return err
			}
			result = updated
			return nil
		}
		if role != model.RoleReviewer && role != model.RoleAdmin {
			return ErrReviewerRequired
		}
		// Any transition that ends in cleared is a release and must re-read
		// the plan/window interlock, not only the pending two-person path.
		if target == string(constants.ClearanceStateCleared) {
			if _, err := s.interlock.evaluate(txCtx, &current); err != nil {
				return err
			}
		}
		before := current.Status
		current.Status = target
		current.Version = input.ExpectedVersion + 1
		current.UpdatedAt = time.Now().UTC()
		if err := s.repository.Update(txCtx, id, input.ExpectedVersion, &current); err != nil {
			return fmt.Errorf("transition 安全许可: %w", err)
		}
		if err := s.appendInterlockAudit(txCtx, actor, requestID, "transition", current, before, target, input.Reason); err != nil {
			return err
		}
		reread, err := s.repository.Get(txCtx, id)
		if err != nil {
			return err
		}
		result = reread
		return nil
	})
	if err != nil {
		return model.SafetyClearance{}, err
	}
	return s.Get(ctx, result.ID)
}

// confirmClearance implements the two-person flow. The first call by an
// operator snapshots the currently approved/safe aggregate versions; the
// second call by a different reviewer re-reads everything and only releases
// when plan, window and all versions still match. The whole check, row update
// and audit entry share one transaction.
func (s *safetyClearanceService) confirmClearance(ctx context.Context, current model.SafetyClearance, input dto.TransitionRequest, actor, role, requestID string) (model.SafetyClearance, error) {
	if role != model.RoleOperator && role != model.RoleReviewer && role != model.RoleAdmin {
		return model.SafetyClearance{}, ErrReviewerRequired
	}
	// Re-read both aggregates inside the transaction. States are always
	// enforced; the recorded versions are only defended once a basis exists.
	basis, err := s.interlock.load(ctx, &current)
	if err != nil {
		return model.SafetyClearance{}, err
	}
	if reason := checkStates(basis); reason != "" {
		return model.SafetyClearance{}, fmt.Errorf("%w: %s", ErrInterlockInvalid, reason)
	}
	now := time.Now().UTC()

	// First-person submission. The original submitter may also re-baseline a
	// drifted submission (still approved/safe, but versions moved); an
	// independent reviewer who arrives on a stale basis is rejected below.
	isResubmit := current.SubmittedBy != "" && current.SubmittedBy == actor && basisChanged(&current, basis)
	if current.SubmittedBy == "" || isResubmit {
		action := "clearance_submit"
		if isResubmit {
			action = "clearance_resubmit"
		}
		current.PlanVersion = basis.planCurrent
		current.WindowVersion = basis.windowCurrent
		current.SubmittedBy = actor
		current.SubmittedAt = &now
		current.ConfirmedBy = ""
		current.ConfirmedAt = nil
		current.Version = input.ExpectedVersion + 1
		current.UpdatedAt = now
		if err := s.repository.Update(ctx, current.ID, input.ExpectedVersion, &current); err != nil {
			return model.SafetyClearance{}, fmt.Errorf("submit safety confirmation: %w", err)
		}
		if err := s.appendInterlockAudit(ctx, actor, requestID, action, current, current.Status, current.Status, input.Reason); err != nil {
			return model.SafetyClearance{}, err
		}
		return s.repository.Get(ctx, current.ID)
	}

	// Someone already submitted: the independent second confirmation is now
	// the only way forward. The submitter themselves cannot release.
	if current.SubmittedBy == actor {
		return model.SafetyClearance{}, ErrSelfApproval
	}
	// Independent reviewer: the recorded basis must be exactly the one being
	// released. Any concurrent plan/window change blocks the release here.
	if reason := checkVersions(&current, basis); reason != "" {
		return model.SafetyClearance{}, fmt.Errorf("%w: %s", ErrInterlockInvalid, reason)
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
	if err := s.repository.Update(ctx, current.ID, input.ExpectedVersion, &current); err != nil {
		return model.SafetyClearance{}, fmt.Errorf("confirm safety clearance: %w", err)
	}
	if err := s.appendInterlockAudit(ctx, actor, requestID, "clearance_confirm", current, before, current.Status, input.Reason); err != nil {
		return model.SafetyClearance{}, err
	}
	return s.repository.Get(ctx, current.ID)
}

func basisChanged(current *model.SafetyClearance, basis interlockBasis) bool {
	return current.PlanVersion != basis.planCurrent || current.WindowVersion != basis.windowCurrent
}

// appendInterlockAudit freezes the release/submission basis - window version
// (dedicated column) plus plan and window codes and versions in the detail -
// together with actor and request ID, inside the same transaction as the
// clearance row change.
func (s *safetyClearanceService) appendInterlockAudit(ctx context.Context, actor, requestID, action string, current model.SafetyClearance, before, after, reason string) error {
	payload, err := json.Marshal(map[string]any{
		"reason":        reason,
		"planCode":      current.PlanCode,
		"planVersion":   current.PlanVersion,
		"windowCode":    current.WindowCode,
		"windowVersion": current.WindowVersion,
	})
	if err != nil {
		return fmt.Errorf("encode interlock audit detail: %w", err)
	}
	if err := s.security.AuditWithWindowVersion(ctx, actor, requestID, action, "SafetyClearance", current.ID, before, after, string(payload), current.WindowVersion); err != nil {
		return fmt.Errorf("persist clearance audit: %w", err)
	}
	return nil
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

// enrich re-reads the bound plans/windows for a page of clearances in bulk and
// fills the computed interlock fields. Pending/working clearances report
// live validity and a Chinese invalid reason; released/historical clearances
// always stay valid so their history is unaffected by later aggregate edits.
func (s *safetyClearanceService) enrich(ctx context.Context, items []model.SafetyClearance) error {
	if len(items) == 0 {
		return nil
	}
	planCodes := make(map[string]struct{})
	windowCodes := make(map[string]struct{})
	for _, item := range items {
		if item.PlanCode != "" {
			planCodes[item.PlanCode] = struct{}{}
		}
		if item.WindowCode != "" {
			windowCodes[item.WindowCode] = struct{}{}
		}
	}
	plans, err := s.plans.ListByCodes(ctx, keys(planCodes))
	if err != nil {
		return fmt.Errorf("enrich clearance plans: %w", err)
	}
	windows, err := s.windows.ListByCodes(ctx, keys(windowCodes))
	if err != nil {
		return fmt.Errorf("enrich clearance windows: %w", err)
	}
	planByCode := make(map[string]model.MooringPlan, len(plans))
	for _, plan := range plans {
		planByCode[plan.Code] = plan
	}
	windowByCode := make(map[string]model.WeatherWindow, len(windows))
	for _, window := range windows {
		windowByCode[window.Code] = window
	}
	for i := range items {
		s.decorate(&items[i], planByCode, windowByCode)
	}
	return nil
}

func (s *safetyClearanceService) decorate(item *model.SafetyClearance, plans map[string]model.MooringPlan, windows map[string]model.WeatherWindow) {
	// Released and expired decisions are frozen history: later plan/window
	// changes must not flag them as invalid.
	if item.Status == string(constants.ClearanceStateCleared) || item.Status == string(constants.ClearanceStateExpired) {
		item.InterlockBasisValid = true
		if plan, ok := plans[item.PlanCode]; ok {
			item.PlanStatus = plan.Status
			item.CurrentPlanVersion = plan.Version
		}
		if window, ok := windows[item.WindowCode]; ok {
			item.WindowStatus = window.Status
			item.CurrentWindowVersion = window.Version
		}
		return
	}
	if item.PlanCode == "" || item.WindowCode == "" {
		item.InterlockInvalidReason = invalidReason(ErrInterlockBasisMissing)
		return
	}
	plan, planOK := plans[item.PlanCode]
	window, windowOK := windows[item.WindowCode]
	if !planOK || !windowOK {
		if !planOK {
			item.InterlockInvalidReason = fmt.Sprintf("系泊方案 %s 已不存在", item.PlanCode)
		} else {
			item.InterlockInvalidReason = fmt.Sprintf("风浪窗口 %s 已不存在", item.WindowCode)
		}
		return
	}
	item.PlanStatus = plan.Status
	item.CurrentPlanVersion = plan.Version
	item.WindowStatus = window.Status
	item.CurrentWindowVersion = window.Version
	basis := interlockBasis{
		plan: &plan, window: &window, planCurrent: plan.Version, windowCurrent: window.Version,
		planStatus: plan.Status, windowStatus: window.Status,
	}
	if reason := checkBasis(item, basis); reason != "" {
		item.InterlockInvalidReason = reason
		return
	}
	item.InterlockBasisValid = true
}

func keys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	return out
}

func validateSafetyClearanceBusinessFields(code, name, facility, owner string) error {
	if strings.TrimSpace(code) == "" || strings.TrimSpace(name) == "" || strings.TrimSpace(facility) == "" || strings.TrimSpace(owner) == "" {
		return ErrInvalidInput
	}
	return nil
}
