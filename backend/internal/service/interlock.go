package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/blueship581/port-mooring-window-safety/backend/internal/constants"
	"github.com/blueship581/port-mooring-window-safety/backend/internal/model"
	"github.com/blueship581/port-mooring-window-safety/backend/internal/repository"
	"gorm.io/gorm"
)

// interlockBasis is the server-side re-read result for the plan and window a
// clearance points at. Versions are the versions currently held in the
// database; they are compared against the versions the clearance recorded.
type interlockBasis struct {
	plan          *model.MooringPlan
	window        *model.WeatherWindow
	planCurrent   uint
	windowCurrent uint
	planStatus    string
	windowStatus  string
}

// interlockEvaluator centralizes the window/plan interlock rules so both the
// write path (submit/release gate) and the read model (page enrichment) share
// one definition of "still valid".
type interlockEvaluator struct {
	plans   repository.MooringPlanRepository
	windows repository.WeatherWindowRepository
}

func newInterlockEvaluator(plans repository.MooringPlanRepository, windows repository.WeatherWindowRepository) *interlockEvaluator {
	return &interlockEvaluator{plans: plans, windows: windows}
}

// lookupBasis re-reads two aggregates by code without requiring a clearance
// record. Create/update use it to reject bindings to non-existent aggregates;
// it does not check states, since a clearance may be prepared before its plan
// is approved.
func (e *interlockEvaluator) lookupBasis(ctx context.Context, planCode, windowCode string) (interlockBasis, error) {
	probe := model.SafetyClearance{PlanCode: planCode, WindowCode: windowCode}
	return e.load(ctx, &probe)
}

// load re-reads both aggregates by their recorded codes. Missing aggregates
// are reported as ErrInterlockInvalid so a pending clearance shows the cause.
func (e *interlockEvaluator) load(ctx context.Context, clearance *model.SafetyClearance) (interlockBasis, error) {
	basis := interlockBasis{}
	if clearance.PlanCode == "" || clearance.WindowCode == "" {
		return basis, fmt.Errorf("%w: %s", ErrInterlockInvalid, invalidReason(ErrInterlockBasisMissing))
	}
	plan, err := e.plans.GetByCode(ctx, clearance.PlanCode)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return basis, fmt.Errorf("%w: 系泊方案 %s 已不存在", ErrInterlockInvalid, clearance.PlanCode)
		}
		return basis, fmt.Errorf("re-read mooring plan %s: %w", clearance.PlanCode, err)
	}
	window, err := e.windows.GetByCode(ctx, clearance.WindowCode)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return basis, fmt.Errorf("%w: 风浪窗口 %s 已不存在", ErrInterlockInvalid, clearance.WindowCode)
		}
		return basis, fmt.Errorf("re-read weather window %s: %w", clearance.WindowCode, err)
	}
	basis.plan = &plan
	basis.window = &window
	basis.planCurrent = plan.Version
	basis.windowCurrent = window.Version
	basis.planStatus = plan.Status
	basis.windowStatus = window.Status
	return basis, nil
}

// checkStates enforces the hard operational gate: the plan must currently be
// approved and the window currently safe. It deliberately ignores versions so
// the original submitter can re-baseline a drifted, but still safe, basis.
func checkStates(basis interlockBasis) string {
	if basis.planStatus != constants.MooringPlanStatusApproved {
		return fmt.Sprintf("系泊方案 %s 当前状态为 %s，须为 approved 才可放行", basis.plan.Code, basis.planStatus)
	}
	if basis.windowStatus != constants.WeatherWindowStatusSafe {
		return fmt.Sprintf("风浪窗口 %s 当前状态为 %s，须为 safe 才可放行", basis.window.Code, basis.windowStatus)
	}
	return ""
}

// checkVersions defends the recorded snapshot: once a first person submitted,
// the independent reviewer only releases when neither aggregate changed.
func checkVersions(clearance *model.SafetyClearance, basis interlockBasis) string {
	if clearance.SubmittedBy == "" {
		return ""
	}
	if basis.planCurrent != clearance.PlanVersion {
		return fmt.Sprintf("系泊方案 %s 已变更：许可依据 v%d，当前 v%d，请重新提交许可", clearance.PlanCode, clearance.PlanVersion, basis.planCurrent)
	}
	if basis.windowCurrent != clearance.WindowVersion {
		return fmt.Sprintf("风浪窗口 %s 已变更：许可依据 v%d，当前 v%d，请重新提交许可", clearance.WindowCode, clearance.WindowVersion, basis.windowCurrent)
	}
	return ""
}

// checkBasis is the full read-side rule used when enriching pending
// clearances: states and recorded versions must both hold.
func checkBasis(clearance *model.SafetyClearance, basis interlockBasis) string {
	if reason := checkStates(basis); reason != "" {
		return reason
	}
	return checkVersions(clearance, basis)
}

// evaluate is the strict write-side gate used by reviewer-driven releases:
// re-read, require approved/safe states and, when a basis was recorded, exact
// version match.
func (e *interlockEvaluator) evaluate(ctx context.Context, clearance *model.SafetyClearance) (interlockBasis, error) {
	basis, err := e.load(ctx, clearance)
	if err != nil {
		return basis, err
	}
	if reason := checkBasis(clearance, basis); reason != "" {
		return basis, fmt.Errorf("%w: %s", ErrInterlockInvalid, reason)
	}
	return basis, nil
}

// invalidReason extracts the Chinese rule description from a re-read error for
// display on a pending clearance.
func invalidReason(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, ErrInterlockBasisMissing):
		return "许可未同时记录系泊方案编码与风浪窗口编码，无法建立联锁依据"
	case errors.Is(err, ErrInterlockInvalid):
		// The wrapped message already contains the concrete explanation; strip
		// the sentinel prefix when present.
		text := err.Error()
		prefix := ErrInterlockInvalid.Error() + ": "
		if len(text) > len(prefix) && text[:len(prefix)] == prefix {
			return text[len(prefix):]
		}
		return text
	default:
		return "联锁依据暂不可评估"
	}
}
