package orchestrator_test

// Tests for ApplySchedule (AC-35) against the SDK's own client mocks: the options it creates the
// schedule with, the upsert path when the schedule already exists, and error propagation. The
// live script (scripts/live-materialise.sh) proves the same against a real dev server.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/mocks"
	"go.temporal.io/sdk/temporal"

	"github.com/DMokong/data-platform/internal/orchestrator"
	"github.com/DMokong/data-platform/internal/source/beads"
)

// checkAction asserts that a schedule action starts MaterialiseSource for beads on the task queue
// under workflow id materialise-source-beads.
func checkAction(t *testing.T, a client.ScheduleAction) {
	t.Helper()
	wa, ok := a.(*client.ScheduleWorkflowAction)
	if !ok {
		t.Fatalf("action is %T, want *client.ScheduleWorkflowAction", a)
	}
	if wa.Workflow != orchestrator.WorkflowMaterialiseSource {
		t.Errorf("action workflow = %v, want %q", wa.Workflow, orchestrator.WorkflowMaterialiseSource)
	}
	if wa.ID != "materialise-source-beads" {
		t.Errorf("action workflow id = %q, want %q", wa.ID, "materialise-source-beads")
	}
	if wa.TaskQueue != orchestrator.TaskQueue {
		t.Errorf("action task queue = %q, want %q", wa.TaskQueue, orchestrator.TaskQueue)
	}
	if len(wa.Args) != 1 {
		t.Fatalf("action args = %v, want exactly one MaterialiseSourceInput", wa.Args)
	}
	if in, ok := wa.Args[0].(orchestrator.MaterialiseSourceInput); !ok || in.Source != "beads" {
		t.Errorf("action arg = %#v, want MaterialiseSourceInput{Source: \"beads\"}", wa.Args[0])
	}
}

func checkIntervals(t *testing.T, spec *client.ScheduleSpec) {
	t.Helper()
	if spec == nil || len(spec.Intervals) != 1 || spec.Intervals[0].Every != 15*time.Minute || spec.Intervals[0].Offset != 0 {
		t.Errorf("spec = %+v, want exactly one 15m interval", spec)
	}
	if len(spec.Calendars) != 0 || len(spec.CronExpressions) != 0 {
		t.Errorf("spec has calendars %v / crons %v, want none", spec.Calendars, spec.CronExpressions)
	}
}

// AC-35: a first ApplySchedule creates schedule materialise-beads at the declared 15 m cadence,
// overlap Skip, catch-up window 10 s, starting MaterialiseSource.
func TestApplySchedule_CreatesSchedule_AC35(t *testing.T) {
	c := mocks.NewClient(t)
	sc := mocks.NewScheduleClient(t)
	c.On("ScheduleClient").Return(sc)

	var got client.ScheduleOptions
	sc.On("Create", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		got = args.Get(1).(client.ScheduleOptions)
	}).Return(mocks.NewScheduleHandle(t), nil).Once()

	if err := orchestrator.ApplySchedule(context.Background(), c, beads.Declaration()); err != nil {
		t.Fatalf("ApplySchedule: %v", err)
	}
	if got.ID != "materialise-beads" {
		t.Errorf("schedule id = %q, want %q", got.ID, "materialise-beads")
	}
	checkIntervals(t, &got.Spec)
	if got.Overlap != enumspb.SCHEDULE_OVERLAP_POLICY_SKIP {
		t.Errorf("overlap = %v, want SKIP", got.Overlap)
	}
	if got.CatchupWindow != 10*time.Second {
		t.Errorf("catch-up window = %v, want 10s", got.CatchupWindow)
	}
	if got.Paused {
		t.Errorf("schedule created paused")
	}
	checkAction(t, got.Action)
}

// AC-35: when the schedule already exists, ApplySchedule updates it in place to the same spec,
// action and policies, keeping its paused state and note, and succeeds.
func TestApplySchedule_UpdatesExistingSchedule_AC35(t *testing.T) {
	c := mocks.NewClient(t)
	sc := mocks.NewScheduleClient(t)
	h := mocks.NewScheduleHandle(t)
	c.On("ScheduleClient").Return(sc)
	sc.On("Create", mock.Anything, mock.Anything).Return(nil, temporal.ErrScheduleAlreadyRunning).Once()
	sc.On("GetHandle", mock.Anything, "materialise-beads").Return(h).Once()

	existing := client.ScheduleDescription{Schedule: client.Schedule{
		Action: &client.ScheduleWorkflowAction{ID: "old", Workflow: "Old", TaskQueue: "old-queue"},
		Spec:   &client.ScheduleSpec{Intervals: []client.ScheduleIntervalSpec{{Every: time.Hour}}},
		Policy: &client.SchedulePolicies{Overlap: enumspb.SCHEDULE_OVERLAP_POLICY_ALLOW_ALL, CatchupWindow: 365 * 24 * time.Hour},
		State:  &client.ScheduleState{Paused: true, Note: "paused by hand"},
	}}
	var updated *client.ScheduleUpdate
	h.On("Update", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		opts := args.Get(1).(client.ScheduleUpdateOptions)
		var err error
		updated, err = opts.DoUpdate(client.ScheduleUpdateInput{Description: existing})
		if err != nil {
			t.Errorf("DoUpdate: %v", err)
		}
	}).Return(nil).Once()

	if err := orchestrator.ApplySchedule(context.Background(), c, beads.Declaration()); err != nil {
		t.Fatalf("ApplySchedule on an existing schedule: %v", err)
	}
	if updated == nil || updated.Schedule == nil {
		t.Fatalf("DoUpdate returned no schedule")
	}
	s := updated.Schedule
	checkIntervals(t, s.Spec)
	checkAction(t, s.Action)
	if s.Policy == nil || s.Policy.Overlap != enumspb.SCHEDULE_OVERLAP_POLICY_SKIP || s.Policy.CatchupWindow != 10*time.Second {
		t.Errorf("policy = %+v, want overlap SKIP and catch-up window 10s", s.Policy)
	}
	if s.State == nil || !s.State.Paused || s.State.Note != "paused by hand" {
		t.Errorf("state = %+v, want the existing state kept", s.State)
	}
}

// ApplySchedule surfaces a create error other than "already exists" instead of updating.
func TestApplySchedule_CreateErrorIsReturned(t *testing.T) {
	c := mocks.NewClient(t)
	sc := mocks.NewScheduleClient(t)
	c.On("ScheduleClient").Return(sc)
	sc.On("Create", mock.Anything, mock.Anything).Return(nil, errors.New("unavailable")).Once()

	err := orchestrator.ApplySchedule(context.Background(), c, beads.Declaration())
	if err == nil || !strings.Contains(err.Error(), "unavailable") || !strings.Contains(err.Error(), "materialise-beads") {
		t.Fatalf("ApplySchedule error = %v, want one naming materialise-beads and the cause", err)
	}
}

// ApplySchedule rejects a source without a positive cadence before calling the server.
func TestApplySchedule_RejectsZeroCadence(t *testing.T) {
	src := beads.Declaration()
	src.Cadence = 0
	if err := orchestrator.ApplySchedule(context.Background(), mocks.NewClient(t), src); err == nil {
		t.Fatalf("ApplySchedule accepted a zero cadence")
	}
}
