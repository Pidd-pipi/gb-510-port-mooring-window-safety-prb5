package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/blueship581/port-mooring-window-safety/backend/internal/config"
	"github.com/blueship581/port-mooring-window-safety/backend/internal/dto"
	"github.com/blueship581/port-mooring-window-safety/backend/internal/model"
	"github.com/blueship581/port-mooring-window-safety/backend/internal/repository"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type clearanceFixture struct {
	svc       SafetyClearanceService
	concrete  *safetyClearanceService
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
	if err := db.AutoMigrate(&model.SafetyClearance{}, &model.MooringPlan{}, &model.WeatherWindow{}, &model.AuditLog{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	fixture := clearanceFixture{
		clearance: repository.NewSafetyClearanceRepository(db),
		plans:     repository.NewMooringPlanRepository(db),
		windows:   repository.NewWeatherWindowRepository(db),
	}
	fixture.security = NewSecurityService(repository.NewSecurityRepository(db), config.Config{})
	fixture.concrete = NewSafetyClearanceService(
		fixture.clearance, fixture.plans, fixture.windows, fixture.security, repository.NewUnitOfWork(db),
	).(*safetyClearanceService)
	fixture.svc = fixture.concrete
	return fixture
}

func (f clearanceFixture) seedPlan(t *testing.T, code, status string) {
	t.Helper()
	plan := model.MooringPlan{
		BaseModel: model.BaseModel{Code: code, Name: "Plan " + code, Status: status, Version: 1},
		Facility:  "Berth A", Owner: "operations", Category: "test", RiskLevel: "medium",
		EffectiveAt: time.Now().UTC(),
	}
	if err := f.plans.Create(context.Background(), &plan); err != nil {
		t.Fatalf("create plan %s: %v", code, err)
	}
}

func (f clearanceFixture) seedWindow(t *testing.T, code, status string, version uint) {
	t.Helper()
	window := model.WeatherWindow{
		BaseModel: model.BaseModel{Code: code, Name: "Window " + code, Status: status, Version: version},
		Facility:  "Berth A", Owner: "operations", Category: "test", RiskLevel: "medium",
		EffectiveAt: time.Now().UTC(),
	}
	if err := f.windows.Create(context.Background(), &window); err != nil {
		t.Fatalf("create window %s: %v", code, err)
	}
}

func (f clearanceFixture) seedClearance(t *testing.T, code, planCode, windowCode string, windowVersion uint) model.SafetyClearance {
	t.Helper()
	item := model.SafetyClearance{
		BaseModel: model.BaseModel{Code: code, Name: "Clearance " + code, Status: model.SafetyClearanceInitialStatus, Version: 1},
		Facility:  "Berth A", Owner: "operations", Category: "test", RiskLevel: "medium",
		EffectiveAt: time.Now().UTC(), Evidence: "checked",
		PlanCode: planCode, WindowCode: windowCode, WindowVersion: windowVersion,
	}
	if err := f.clearance.Create(context.Background(), &item); err != nil {
		t.Fatalf("create clearance %s: %v", code, err)
	}
	return item
}

func (f clearanceFixture) auditCount(t *testing.T) int64 {
	t.Helper()
	_, total, err := f.security.ListAudits(context.Background(), 1, 100, "")
	if err != nil {
		t.Fatalf("list audits: %v", err)
	}
	return total
}

func TestSafetyClearanceRequiresIndependentReviewer(t *testing.T) {
	fixture := newClearanceFixture(t)
	fixture.seedPlan(t, "MP-T", "approved")
	fixture.seedWindow(t, "WW-T", "safe", 7)
	item := fixture.seedClearance(t, "SC-TEST", "MP-T", "WW-T", 7)

	first, err := fixture.svc.Transition(context.Background(), item.ID, dto.TransitionRequest{
		Status: "cleared", ExpectedVersion: 1, Reason: "operator safety submission", WindowVersion: 7,
	}, "operator", model.RoleOperator, "request-submit")
	if err != nil {
		t.Fatalf("first confirmation: %v", err)
	}
	if first.Status != "pending" || first.SubmittedBy != "operator" || first.ConfirmedBy != "" || first.WindowVersion != 7 {
		t.Fatalf("unexpected first confirmation state: %+v", first)
	}

	_, err = fixture.svc.Transition(context.Background(), item.ID, dto.TransitionRequest{
		Status: "cleared", ExpectedVersion: first.Version, Reason: "attempted self approval", WindowVersion: 7,
	}, "operator", model.RoleReviewer, "request-self")
	if !errors.Is(err, ErrSelfApproval) {
		t.Fatalf("expected self approval error, got %v", err)
	}

	_, err = fixture.svc.Transition(context.Background(), item.ID, dto.TransitionRequest{
		Status: "cleared", ExpectedVersion: first.Version, Reason: "second operator approval", WindowVersion: 7,
	}, "operator-two", model.RoleOperator, "request-operator")
	if !errors.Is(err, ErrReviewerRequired) {
		t.Fatalf("expected reviewer role error, got %v", err)
	}

	final, err := fixture.svc.Transition(context.Background(), item.ID, dto.TransitionRequest{
		Status: "cleared", ExpectedVersion: first.Version, Reason: "independent safety review", WindowVersion: 7,
	}, "reviewer", model.RoleReviewer, "request-review")
	if err != nil {
		t.Fatalf("independent confirmation: %v", err)
	}
	if final.Status != "cleared" || final.SubmittedBy != "operator" || final.ConfirmedBy != "reviewer" {
		t.Fatalf("unexpected final confirmation state: %+v", final)
	}

	logs, total, err := fixture.security.ListAudits(context.Background(), 1, 20, "")
	if err != nil {
		t.Fatalf("list audits: %v", err)
	}
	if total != 2 || len(logs) != 2 {
		t.Fatalf("expected two audit entries, total=%d len=%d", total, len(logs))
	}
	for _, audit := range logs {
		if audit.WindowVersion != 7 || audit.RequestID == "" || audit.Actor == "" {
			t.Fatalf("audit did not preserve confirmation context: %+v", audit)
		}
	}
}

func TestSafetyClearanceInterlockBlocksSubmitAndRelease(t *testing.T) {
	cases := []struct {
		name          string
		planStatus    string
		windowStatus  string
		windowVersion uint
		expectReason  string
	}{
		{name: "plan not approved", planStatus: "review", windowStatus: "safe", windowVersion: 3, expectReason: "未批准"},
		{name: "window not safe", planStatus: "approved", windowStatus: "restricted", windowVersion: 3, expectReason: "非安全状态"},
		{name: "window version drifted", planStatus: "approved", windowStatus: "safe", windowVersion: 9, expectReason: "版本已变更"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newClearanceFixture(t)
			fixture.seedPlan(t, "MP-T", tc.planStatus)
			fixture.seedWindow(t, "WW-T", tc.windowStatus, tc.windowVersion)
			item := fixture.seedClearance(t, "SC-INTERLOCK", "MP-T", "WW-T", 3)

			_, err := fixture.svc.Transition(context.Background(), item.ID, dto.TransitionRequest{
				Status: "cleared", ExpectedVersion: 1, Reason: "operator safety submission", WindowVersion: 3,
			}, "operator", model.RoleOperator, "request-submit")
			if !errors.Is(err, ErrClearanceInterlock) {
				t.Fatalf("expected interlock error, got %v", err)
			}
			if tc.expectReason != "" && !strings.Contains(err.Error(), tc.expectReason) {
				t.Fatalf("expected reason %q in error %q", tc.expectReason, err.Error())
			}
			// Failed submissions must roll back both state and audit.
			reloaded, getErr := fixture.clearance.Get(context.Background(), item.ID)
			if getErr != nil {
				t.Fatalf("reload clearance: %v", getErr)
			}
			if reloaded.SubmittedBy != "" || reloaded.Version != 1 {
				t.Fatalf("failed submission mutated state: %+v", reloaded)
			}
			if total := fixture.auditCount(t); total != 0 {
				t.Fatalf("failed submission left %d audit entries", total)
			}
		})
	}

	t.Run("missing plan or window", func(t *testing.T) {
		fixture := newClearanceFixture(t)
		item := fixture.seedClearance(t, "SC-MISSING", "MP-GONE", "WW-GONE", 1)
		_, err := fixture.svc.Transition(context.Background(), item.ID, dto.TransitionRequest{
			Status: "cleared", ExpectedVersion: 1, Reason: "operator safety submission", WindowVersion: 1,
		}, "operator", model.RoleOperator, "request-submit")
		if !errors.Is(err, ErrClearanceInterlock) {
			t.Fatalf("expected interlock error, got %v", err)
		}
	})

	t.Run("window change between submit and release blocks release", func(t *testing.T) {
		fixture := newClearanceFixture(t)
		fixture.seedPlan(t, "MP-T", "approved")
		fixture.seedWindow(t, "WW-T", "safe", 3)
		item := fixture.seedClearance(t, "SC-DRIFT", "MP-T", "WW-T", 3)

		first, err := fixture.svc.Transition(context.Background(), item.ID, dto.TransitionRequest{
			Status: "cleared", ExpectedVersion: 1, Reason: "operator safety submission", WindowVersion: 3,
		}, "operator", model.RoleOperator, "request-submit")
		if err != nil {
			t.Fatalf("submit: %v", err)
		}
		// The window turns restricted and bumps its version before the reviewer acts.
		window, err := fixture.windows.FindByCode(context.Background(), "WW-T")
		if err != nil {
			t.Fatalf("load window: %v", err)
		}
		window.Status = "restricted"
		window.Version = 4
		window.UpdatedAt = time.Now().UTC()
		if err := fixture.windows.Update(context.Background(), window.ID, 3, &window); err != nil {
			t.Fatalf("restrict window: %v", err)
		}

		_, err = fixture.svc.Transition(context.Background(), item.ID, dto.TransitionRequest{
			Status: "cleared", ExpectedVersion: first.Version, Reason: "independent safety review", WindowVersion: 3,
		}, "reviewer", model.RoleReviewer, "request-review")
		if !errors.Is(err, ErrClearanceInterlock) {
			t.Fatalf("expected interlock error on release, got %v", err)
		}
		reloaded, getErr := fixture.clearance.Get(context.Background(), item.ID)
		if getErr != nil {
			t.Fatalf("reload clearance: %v", getErr)
		}
		if reloaded.Status != "pending" || reloaded.ConfirmedBy != "" {
			t.Fatalf("blocked release mutated state: %+v", reloaded)
		}
		if total := fixture.auditCount(t); total != 1 {
			t.Fatalf("expected only the submission audit, got %d", total)
		}
	})
}

func TestSafetyClearanceDuplicateReviewIsAtomic(t *testing.T) {
	fixture := newClearanceFixture(t)
	fixture.seedPlan(t, "MP-T", "approved")
	fixture.seedWindow(t, "WW-T", "safe", 5)
	item := fixture.seedClearance(t, "SC-RACE", "MP-T", "WW-T", 5)

	if _, err := fixture.svc.Transition(context.Background(), item.ID, dto.TransitionRequest{
		Status: "cleared", ExpectedVersion: 1, Reason: "operator safety submission", WindowVersion: 5,
	}, "operator", model.RoleOperator, "request-submit"); err != nil {
		t.Fatalf("submit: %v", err)
	}

	// Two reviewers race on the same pre-release snapshot; exactly one may win.
	snapshot, err := fixture.clearance.Get(context.Background(), item.ID)
	if err != nil {
		t.Fatalf("load submitted clearance: %v", err)
	}
	release := dto.TransitionRequest{Status: "cleared", ExpectedVersion: snapshot.Version, Reason: "independent safety review", WindowVersion: 5}
	winner, err := fixture.concrete.confirmClearance(context.Background(), snapshot, release, "reviewer", model.RoleReviewer, "request-review-a")
	if err != nil {
		t.Fatalf("first release: %v", err)
	}
	if winner.Status != "cleared" {
		t.Fatalf("expected cleared state, got %+v", winner)
	}
	_, err = fixture.concrete.confirmClearance(context.Background(), snapshot, release, "admin", model.RoleAdmin, "request-review-b")
	if !errors.Is(err, repository.ErrVersionConflict) {
		t.Fatalf("expected version conflict on duplicate review, got %v", err)
	}

	reloaded, getErr := fixture.clearance.Get(context.Background(), item.ID)
	if getErr != nil {
		t.Fatalf("reload clearance: %v", getErr)
	}
	if reloaded.ConfirmedBy != "reviewer" || reloaded.Status != "cleared" {
		t.Fatalf("duplicate review corrupted state: %+v", reloaded)
	}
	if total := fixture.auditCount(t); total != 2 {
		t.Fatalf("expected submit+release audits only, got %d", total)
	}
}

func TestSafetyClearanceInterlockVisibility(t *testing.T) {
	fixture := newClearanceFixture(t)
	fixture.seedPlan(t, "MP-T", "approved")
	fixture.seedWindow(t, "WW-T", "safe", 2)
	pending := fixture.seedClearance(t, "SC-PENDING", "MP-T", "WW-T", 2)
	released := fixture.seedClearance(t, "SC-RELEASED", "MP-T", "WW-T", 2)
	released.Status = "cleared"
	released.ConfirmedBy = "reviewer"
	if err := fixture.clearance.Update(context.Background(), released.ID, 1, &released); err != nil {
		t.Fatalf("mark released: %v", err)
	}

	// The window degrades after both clearances were recorded.
	window, err := fixture.windows.FindByCode(context.Background(), "WW-T")
	if err != nil {
		t.Fatalf("load window: %v", err)
	}
	window.Status = "restricted"
	window.Version = 3
	window.UpdatedAt = time.Now().UTC()
	if err := fixture.windows.Update(context.Background(), window.ID, 2, &window); err != nil {
		t.Fatalf("restrict window: %v", err)
	}

	pendingView, err := fixture.svc.Get(context.Background(), pending.ID)
	if err != nil {
		t.Fatalf("get pending clearance: %v", err)
	}
	if pendingView.Interlock == nil || pendingView.Interlock.Satisfied || pendingView.Interlock.InvalidReason == "" {
		t.Fatalf("pending clearance must surface the invalid reason: %+v", pendingView.Interlock)
	}
	if pendingView.Interlock.WindowVersionMatch || pendingView.Interlock.CurrentWindowVersion != 3 {
		t.Fatalf("pending interlock must expose the version drift: %+v", pendingView.Interlock)
	}

	releasedView, err := fixture.svc.Get(context.Background(), released.ID)
	if err != nil {
		t.Fatalf("get released clearance: %v", err)
	}
	if releasedView.Interlock == nil || releasedView.Interlock.InvalidReason != "" {
		t.Fatalf("released history must stay unaffected: %+v", releasedView.Interlock)
	}

	page, err := fixture.svc.List(context.Background(), dto.PageQuery{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("list clearances: %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("expected two clearances, got %d", len(page.Items))
	}
	for _, item := range page.Items {
		if item.Interlock == nil || item.Interlock.PlanCode != "MP-T" || item.Interlock.WindowCode != "WW-T" {
			t.Fatalf("list must attach the interlock basis: %+v", item.Interlock)
		}
	}
}
