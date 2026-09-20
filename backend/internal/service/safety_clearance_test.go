package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/blueship581/port-mooring-window-safety/backend/internal/config"
	"github.com/blueship581/port-mooring-window-safety/backend/internal/constants"
	"github.com/blueship581/port-mooring-window-safety/backend/internal/dto"
	"github.com/blueship581/port-mooring-window-safety/backend/internal/model"
	"github.com/blueship581/port-mooring-window-safety/backend/internal/repository"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type clearanceFixture struct {
	db        *gorm.DB
	svc       SafetyClearanceService
	security  SecurityService
	clearance repository.SafetyClearanceRepository
	plans     repository.MooringPlanRepository
	windows   repository.WeatherWindowRepository
}

func newClearanceFixture(t *testing.T) clearanceFixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	// A shared in-memory SQLite database lives on a single connection; pin the
	// pool so concurrent goroutines observe the same schema and data. Writes
	// serialize, which still exercises the transaction/optimistic-lock logic.
	if sqlDB, dbErr := db.DB(); dbErr == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	if err := db.AutoMigrate(
		&model.SafetyClearance{}, &model.AuditLog{},
		&model.MooringPlan{}, &model.WeatherWindow{}, &model.User{},
	); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	clearanceRepository := repository.NewSafetyClearanceRepository(db)
	planRepository := repository.NewMooringPlanRepository(db)
	windowRepository := repository.NewWeatherWindowRepository(db)
	security := NewSecurityService(repository.NewSecurityRepository(db), config.Config{})
	tx := repository.NewTxManager(db)
	svc := NewSafetyClearanceService(clearanceRepository, planRepository, windowRepository, security, tx)
	return clearanceFixture{
		db: db, svc: svc, security: security,
		clearance: clearanceRepository, plans: planRepository, windows: windowRepository,
	}
}

func (f clearanceFixture) seedAggregate(t *testing.T) (model.MooringPlan, model.WeatherWindow) {
	t.Helper()
	plan := model.MooringPlan{
		BaseModel: model.BaseModel{Code: "MP-1", Name: "Approved plan", Status: constants.MooringPlanStatusApproved, Version: 1},
		Facility:  "Berth A", Owner: "operations", Category: "test", RiskLevel: "medium",
		EffectiveAt: time.Now().UTC(),
	}
	window := model.WeatherWindow{
		BaseModel: model.BaseModel{Code: "WW-1", Name: "Safe window", Status: constants.WeatherWindowStatusSafe, Version: 1},
		Facility:  "Berth A", Owner: "operations", Category: "test", RiskLevel: "low",
		EffectiveAt: time.Now().UTC(),
	}
	if err := f.plans.Create(context.Background(), &plan); err != nil {
		t.Fatalf("create plan: %v", err)
	}
	if err := f.windows.Create(context.Background(), &window); err != nil {
		t.Fatalf("create window: %v", err)
	}
	return plan, window
}

func (f clearanceFixture) seedClearance(t *testing.T, status string) model.SafetyClearance {
	t.Helper()
	item := model.SafetyClearance{
		BaseModel: model.BaseModel{Code: "SC-1", Name: "Test clearance", Status: status, Version: 1},
		Facility:  "Berth A", Owner: "operations", Category: "test", RiskLevel: "medium",
		EffectiveAt: time.Now().UTC(), Evidence: "checked",
		PlanCode: "MP-1", WindowCode: "WW-1", WindowVersion: 1,
	}
	if err := f.clearance.Create(context.Background(), &item); err != nil {
		t.Fatalf("create clearance: %v", err)
	}
	return item
}

func transitionClearance(status string, version uint, reason string) dto.TransitionRequest {
	return dto.TransitionRequest{Status: status, ExpectedVersion: version, Reason: reason}
}

func TestClearanceInterlockSubmitReleaseFlow(t *testing.T) {
	f := newClearanceFixture(t)
	f.seedAggregate(t)
	item := f.seedClearance(t, "pending")

	// First person (operator) submits: pending remains, basis versions are
	// snapshotted from the re-read aggregates.
	submitted, err := f.svc.Transition(context.Background(), item.ID,
		transitionClearance("cleared", 1, "operator safety submission"), "operator", model.RoleOperator, "req-submit")
	if err != nil {
		t.Fatalf("first submission: %v", err)
	}
	if submitted.Status != "pending" || submitted.SubmittedBy != "operator" || submitted.PlanVersion != 1 || submitted.WindowVersion != 1 {
		t.Fatalf("unexpected submitted state: %+v", submitted)
	}
	if !submitted.InterlockBasisValid || submitted.InterlockInvalidReason != "" {
		t.Fatalf("submitted clearance should be interlock-valid: %+v", submitted)
	}

	// Submitter self-review is rejected.
	if _, err := f.svc.Transition(context.Background(), item.ID,
		transitionClearance("cleared", submitted.Version, "self review"), "operator", model.RoleReviewer, "req-self"); !errors.Is(err, ErrSelfApproval) {
		t.Fatalf("expected self approval error, got %v", err)
	}

	// A second operator cannot perform the independent review.
	if _, err := f.svc.Transition(context.Background(), item.ID,
		transitionClearance("cleared", submitted.Version, "operator two"), "operator-two", model.RoleOperator, "req-op2"); !errors.Is(err, ErrReviewerRequired) {
		t.Fatalf("expected reviewer required error, got %v", err)
	}

	// A different reviewer releases; audit must carry window version and the
	// plan/window basis in its detail.
	released, err := f.svc.Transition(context.Background(), item.ID,
		transitionClearance("cleared", submitted.Version, "independent release"), "reviewer", model.RoleReviewer, "req-release")
	if err != nil {
		t.Fatalf("independent release: %v", err)
	}
	if released.Status != "cleared" || released.ConfirmedBy != "reviewer" || released.ConfirmedAt == nil {
		t.Fatalf("unexpected released state: %+v", released)
	}

	logs, total, err := f.security.ListAudits(context.Background(), 1, 20, "")
	if err != nil {
		t.Fatalf("list audits: %v", err)
	}
	if total != 2 || len(logs) != 2 {
		t.Fatalf("expected two audit entries, total=%d", total)
	}
	var detail map[string]any
	for _, audit := range logs {
		if audit.Action != "clearance_confirm" {
			continue
		}
		if audit.WindowVersion != 1 || audit.RequestID != "req-release" || audit.Actor != "reviewer" {
			t.Fatalf("confirm audit lost context: %+v", audit)
		}
		if err := json.Unmarshal([]byte(audit.Detail), &detail); err != nil {
			t.Fatalf("audit detail is not JSON basis: %v", err)
		}
	}
	if detail["planCode"] != "MP-1" || detail["windowCode"] != "WW-1" ||
		uint(detail["planVersion"].(float64)) != 1 || uint(detail["windowVersion"].(float64)) != 1 {
		t.Fatalf("audit detail missing interlock basis: %+v", detail)
	}
}

func TestClearanceSubmissionBlockedByPlanOrWindowState(t *testing.T) {
	f := newClearanceFixture(t)

	// Plan not approved -> submit blocked.
	draftPlan := model.MooringPlan{
		BaseModel: model.BaseModel{Code: "MP-DRAFT", Name: "Draft plan", Status: "draft", Version: 1},
		Facility:  "Berth A", Owner: "operations", EffectiveAt: time.Now().UTC(),
	}
	safeWindow := model.WeatherWindow{
		BaseModel: model.BaseModel{Code: "WW-SAFE", Name: "Safe window", Status: "safe", Version: 1},
		Facility:  "Berth A", Owner: "operations", EffectiveAt: time.Now().UTC(),
	}
	if err := f.plans.Create(context.Background(), &draftPlan); err != nil {
		t.Fatalf("create plan: %v", err)
	}
	if err := f.windows.Create(context.Background(), &safeWindow); err != nil {
		t.Fatalf("create window: %v", err)
	}
	blocked := model.SafetyClearance{
		BaseModel: model.BaseModel{Code: "SC-PLAN", Name: "Blocked", Status: "pending", Version: 1},
		Facility:  "Berth A", Owner: "operations", EffectiveAt: time.Now().UTC(),
		PlanCode: "MP-DRAFT", WindowCode: "WW-SAFE",
	}
	if err := f.clearance.Create(context.Background(), &blocked); err != nil {
		t.Fatalf("create clearance: %v", err)
	}
	if _, err := f.svc.Transition(context.Background(), blocked.ID,
		transitionClearance("cleared", 1, "attempt submit"), "operator", model.RoleOperator, "req-1"); !errors.Is(err, ErrInterlockInvalid) {
		t.Fatalf("expected interlock invalid for draft plan, got %v", err)
	}

	// Approved plan + restricted window -> still blocked.
	approved := model.MooringPlan{
		BaseModel: model.BaseModel{Code: "MP-OK", Name: "Approved", Status: "approved", Version: 1},
		Facility:  "Berth A", Owner: "operations", EffectiveAt: time.Now().UTC(),
	}
	restricted := model.WeatherWindow{
		BaseModel: model.BaseModel{Code: "WW-NO", Name: "Restricted", Status: "restricted", Version: 1},
		Facility:  "Berth A", Owner: "operations", EffectiveAt: time.Now().UTC(),
	}
	if err := f.plans.Create(context.Background(), &approved); err != nil {
		t.Fatalf("create approved plan: %v", err)
	}
	if err := f.windows.Create(context.Background(), &restricted); err != nil {
		t.Fatalf("create restricted window: %v", err)
	}
	blocked2 := model.SafetyClearance{
		BaseModel: model.BaseModel{Code: "SC-WIN", Name: "Blocked2", Status: "pending", Version: 1},
		Facility:  "Berth A", Owner: "operations", EffectiveAt: time.Now().UTC(),
		PlanCode: "MP-OK", WindowCode: "WW-NO",
	}
	if err := f.clearance.Create(context.Background(), &blocked2); err != nil {
		t.Fatalf("create clearance: %v", err)
	}
	if _, err := f.svc.Transition(context.Background(), blocked2.ID,
		transitionClearance("cleared", 1, "attempt submit"), "operator", model.RoleOperator, "req-2"); !errors.Is(err, ErrInterlockInvalid) {
		t.Fatalf("expected interlock invalid for restricted window, got %v", err)
	}

	// Missing aggregate surfaces interlock invalid.
	missing := model.SafetyClearance{
		BaseModel: model.BaseModel{Code: "SC-MISS", Name: "Missing", Status: "pending", Version: 1},
		Facility:  "Berth A", Owner: "operations", EffectiveAt: time.Now().UTC(),
		PlanCode: "MP-GHOST", WindowCode: "WW-SAFE",
	}
	if err := f.clearance.Create(context.Background(), &missing); err != nil {
		t.Fatalf("create clearance: %v", err)
	}
	if _, err := f.svc.Transition(context.Background(), missing.ID,
		transitionClearance("cleared", 1, "attempt submit"), "operator", model.RoleOperator, "req-3"); !errors.Is(err, ErrInterlockInvalid) {
		t.Fatalf("expected interlock invalid for missing plan, got %v", err)
	}
}

func TestClearanceInvalidatedByLaterWindowChange(t *testing.T) {
	f := newClearanceFixture(t)
	_, window := f.seedAggregate(t)
	item := f.seedClearance(t, "pending")

	submitted, err := f.svc.Transition(context.Background(), item.ID,
		transitionClearance("cleared", 1, "submit on v1"), "operator", model.RoleOperator, "req-submit")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	// The window is re-evaluated to "restricted" and edited afterwards; the
	// pending clearance must show the invalid reason and refuse release.
	window.Status = "restricted"
	window.Version = 2
	if err := f.windows.Update(context.Background(), window.ID, 1, &window); err != nil {
		t.Fatalf("change window: %v", err)
	}

	got, err := f.svc.Get(context.Background(), item.ID)
	if err != nil {
		t.Fatalf("get clearance: %v", err)
	}
	if got.InterlockBasisValid || got.InterlockInvalidReason == "" {
		t.Fatalf("expected invalid pending clearance, got valid=%v reason=%q", got.InterlockBasisValid, got.InterlockInvalidReason)
	}
	if got.CurrentWindowVersion != 2 {
		t.Fatalf("expected current window version 2, got %d", got.CurrentWindowVersion)
	}
	if _, err := f.svc.Transition(context.Background(), item.ID,
		transitionClearance("cleared", submitted.Version, "reviewer release attempt"), "reviewer", model.RoleReviewer, "req-blocked"); !errors.Is(err, ErrInterlockInvalid) {
		t.Fatalf("expected release blocked, got %v", err)
	}
}

func TestClearanceResubmitSnapshotsNewBasis(t *testing.T) {
	f := newClearanceFixture(t)
	plan, window := f.seedAggregate(t)
	item := f.seedClearance(t, "pending")

	submitted, err := f.svc.Transition(context.Background(), item.ID,
		transitionClearance("cleared", 1, "submit v1"), "operator", model.RoleOperator, "req-submit")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	// Both aggregates change but remain approved/safe.
	plan.Version = 2
	if err := f.plans.Update(context.Background(), plan.ID, 1, &plan); err != nil {
		t.Fatalf("bump plan: %v", err)
	}
	window.Version = 2
	if err := f.windows.Update(context.Background(), window.ID, 1, &window); err != nil {
		t.Fatalf("bump window: %v", err)
	}

	// Reviewer is blocked because the basis drifted.
	if _, err := f.svc.Transition(context.Background(), item.ID,
		transitionClearance("cleared", submitted.Version, "review attempt"), "reviewer", model.RoleReviewer, "req-review"); !errors.Is(err, ErrInterlockInvalid) {
		t.Fatalf("expected drift to block reviewer, got %v", err)
	}

	// Original submitter re-submits against the fresh basis.
	resubmitted, err := f.svc.Transition(context.Background(), item.ID,
		transitionClearance("cleared", submitted.Version, "resubmit v2"), "operator", model.RoleOperator, "req-resubmit")
	if err != nil {
		t.Fatalf("resubmit: %v", err)
	}
	if resubmitted.PlanVersion != 2 || resubmitted.WindowVersion != 2 || resubmitted.SubmittedBy != "operator" {
		t.Fatalf("resubmit did not snapshot new basis: %+v", resubmitted)
	}

	// Reviewer can now release.
	released, err := f.svc.Transition(context.Background(), item.ID,
		transitionClearance("cleared", resubmitted.Version, "release v2"), "reviewer", model.RoleReviewer, "req-release")
	if err != nil {
		t.Fatalf("release after resubmit: %v", err)
	}
	if released.Status != "cleared" {
		t.Fatalf("expected cleared, got %s", released.Status)
	}
}

func TestReleasedHistoryUnaffectedByLaterChanges(t *testing.T) {
	f := newClearanceFixture(t)
	plan, window := f.seedAggregate(t)
	item := f.seedClearance(t, "pending")

	submitted, err := f.svc.Transition(context.Background(), item.ID,
		transitionClearance("cleared", 1, "submit"), "operator", model.RoleOperator, "req-s")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	released, err := f.svc.Transition(context.Background(), item.ID,
		transitionClearance("cleared", submitted.Version, "release"), "reviewer", model.RoleReviewer, "req-r")
	if err != nil {
		t.Fatalf("release: %v", err)
	}

	// Window drifts to a bad state after release.
	window.Status = "restricted"
	window.Version = 3
	if err := f.windows.Update(context.Background(), window.ID, 1, &window); err != nil {
		t.Fatalf("drift window: %v", err)
	}
	plan.Status = "superseded"
	plan.Version = 3
	if err := f.plans.Update(context.Background(), plan.ID, 1, &plan); err != nil {
		t.Fatalf("drift plan: %v", err)
	}

	got, err := f.svc.Get(context.Background(), released.ID)
	if err != nil {
		t.Fatalf("get released: %v", err)
	}
	if got.Status != "cleared" || !got.InterlockBasisValid || got.InterlockInvalidReason != "" {
		t.Fatalf("released history must remain valid: %+v", got)
	}
	// The page endpoint agrees.
	page, err := f.svc.List(context.Background(), dto.PageQuery{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(page.Items) != 1 || !page.Items[0].InterlockBasisValid {
		t.Fatalf("listed released clearance must stay valid: %+v", page.Items)
	}

	// Rebinding a released clearance to another basis is rejected.
	if _, err := f.svc.Update(context.Background(), released.ID, dto.UpdateSafetyClearance{
		ExpectedVersion: got.Version, Name: got.Name, Facility: got.Facility, Owner: got.Owner,
		Category: got.Category, RiskLevel: got.RiskLevel, EffectiveAt: got.EffectiveAt,
		PlanCode: "MP-OTHER", WindowCode: "WW-OTHER",
	}, "admin", "req-edit"); !errors.Is(err, ErrReleasedBasisLocked) {
		t.Fatalf("expected released basis locked, got %v", err)
	}
}

// failingAuditService makes the audit write fail so tests can prove the
// clearance row update rolls back together with the audit.
type failingAuditService struct {
	SecurityService
}

func (failingAuditService) AuditWithWindowVersion(context.Context, string, string, string, string, uint, string, string, string, uint) error {
	return fmt.Errorf("audit storage unavailable")
}

func TestSubmitAndAuditCommitOrRollbackTogether(t *testing.T) {
	f := newClearanceFixture(t)
	f.seedAggregate(t)
	item := f.seedClearance(t, "pending")

	// Rebuild the service with an audit backend that always fails.
	tx := repository.NewTxManager(f.db)
	svc := NewSafetyClearanceService(f.clearance, f.plans, f.windows, failingAuditService{f.security}, tx)
	_, err := svc.Transition(context.Background(), item.ID,
		transitionClearance("cleared", 1, "submit with broken audit"), "operator", model.RoleOperator, "req-fail")
	if err == nil {
		t.Fatal("expected transition to fail when audit fails")
	}

	// The clearance row must not have been mutated: still unsubmitted at v1.
	got, err := f.svc.Get(context.Background(), item.ID)
	if err != nil {
		t.Fatalf("get clearance: %v", err)
	}
	if got.SubmittedBy != "" || got.Version != 1 || got.PlanVersion != 0 {
		t.Fatalf("clearance row was not rolled back: %+v", got)
	}
}

func TestDuplicateConcurrentReviewIsAtomic(t *testing.T) {
	f := newClearanceFixture(t)
	f.seedAggregate(t)
	item := f.seedClearance(t, "pending")

	submitted, err := f.svc.Transition(context.Background(), item.ID,
		transitionClearance("cleared", 1, "submit"), "operator", model.RoleOperator, "req-submit")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	// Two reviewers release concurrently against the same expected version.
	// Exactly one must win; the loser gets a version conflict and no second
	// confirm audit exists.
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			reviewer := fmt.Sprintf("reviewer-%d", index+1)
			<-start
			_, errs[index] = f.svc.Transition(context.Background(), item.ID,
				transitionClearance("cleared", submitted.Version, "concurrent review"), reviewer, model.RoleReviewer, fmt.Sprintf("req-%d", index))
		}(i)
	}
	close(start)
	wg.Wait()

	successes, rejects := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, repository.ErrVersionConflict), errors.Is(err, ErrInvalidTransition):
			// Under FOR UPDATE the loser re-reads the already-cleared row
			// (invalid transition); under optimistic locking alone it loses
			// the version check. Both prove the second release was rejected.
			rejects++
		default:
			t.Fatalf("unexpected concurrent result: %v", err)
		}
	}
	if successes != 1 || rejects != 1 {
		t.Fatalf("expected one success and one rejection, got successes=%d rejects=%d (%v)", successes, rejects, errs)
	}

	got, err := f.svc.Get(context.Background(), item.ID)
	if err != nil {
		t.Fatalf("get clearance: %v", err)
	}
	if got.Status != "cleared" {
		t.Fatalf("expected clearance released, got %s", got.Status)
	}
	logs, total, err := f.security.ListAudits(context.Background(), 1, 20, "")
	if err != nil {
		t.Fatalf("list audits: %v", err)
	}
	confirms := 0
	for _, log := range logs {
		if log.Action == "clearance_confirm" {
			confirms++
		}
	}
	if confirms != 1 {
		t.Fatalf("expected exactly one confirm audit, got %d (total %d)", confirms, total)
	}
}
