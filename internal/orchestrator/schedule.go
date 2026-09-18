package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"

	"github.com/DMokong/data-platform/internal/source"
)

// ScheduleCatchupWindow is how late a missed scheduled run may still start after the server was
// unavailable: 10 s, the server's minimum. The default of one year would replay a missed run the
// moment a dev server restarts, and under overlap policy Skip that run would then swallow the next
// manual trigger.
const ScheduleCatchupWindow = 10 * time.Second

// ScheduleID returns the id of the schedule that materialises sourceName: "materialise-<name>".
func ScheduleID(sourceName string) string { return "materialise-" + sourceName }

// sourceWorkflowID is the workflow id the schedule starts MaterialiseSource under. The server
// appends the scheduled time, so each run's id is unique.
func sourceWorkflowID(sourceName string) string { return "materialise-source-" + sourceName }

// scheduleSpec fires every src.Cadence.
func scheduleSpec(src source.Source) client.ScheduleSpec {
	return client.ScheduleSpec{Intervals: []client.ScheduleIntervalSpec{{Every: src.Cadence}}}
}

// scheduleAction starts MaterialiseSource for src on TaskQueue.
func scheduleAction(src source.Source) *client.ScheduleWorkflowAction {
	return &client.ScheduleWorkflowAction{
		ID:        sourceWorkflowID(src.Name),
		Workflow:  WorkflowMaterialiseSource,
		Args:      []interface{}{MaterialiseSourceInput{Source: src.Name}},
		TaskQueue: TaskQueue,
	}
}

// ApplySchedule upserts the schedule that materialises src: id materialise-<name>, one action
// every src.Cadence, overlap policy Skip (a tick is dropped while the previous run is still
// going), catch-up window ScheduleCatchupWindow. The action starts workflow MaterialiseSource
// with MaterialiseSourceInput{Source: src.Name} on TaskQueue under workflow id
// materialise-source-<name>. If the schedule already exists, its spec, action and policies are
// replaced and its state (paused or not, its note) is kept, so calling ApplySchedule again is
// safe.
func ApplySchedule(ctx context.Context, c client.Client, src source.Source) error {
	if src.Name == "" {
		return fmt.Errorf("orchestrator: ApplySchedule: source has no name")
	}
	if src.Cadence <= 0 {
		return fmt.Errorf("orchestrator: ApplySchedule: source %q has cadence %v, want > 0", src.Name, src.Cadence)
	}
	id := ScheduleID(src.Name)
	spec := scheduleSpec(src)
	_, err := c.ScheduleClient().Create(ctx, client.ScheduleOptions{
		ID:            id,
		Spec:          spec,
		Action:        scheduleAction(src),
		Overlap:       enumspb.SCHEDULE_OVERLAP_POLICY_SKIP,
		CatchupWindow: ScheduleCatchupWindow,
	})
	if err == nil {
		return nil
	}
	if !errors.Is(err, temporal.ErrScheduleAlreadyRunning) {
		return fmt.Errorf("orchestrator: create schedule %s: %w", id, err)
	}

	h := c.ScheduleClient().GetHandle(ctx, id)
	err = h.Update(ctx, client.ScheduleUpdateOptions{
		DoUpdate: func(in client.ScheduleUpdateInput) (*client.ScheduleUpdate, error) {
			return &client.ScheduleUpdate{Schedule: desiredSchedule(src, in.Description.Schedule.State)}, nil
		},
	})
	if err != nil {
		return fmt.Errorf("orchestrator: update schedule %s: %w", id, err)
	}
	return nil
}

// desiredSchedule is the full schedule ApplySchedule wants for src, carrying over state from the
// existing schedule (a fresh, unpaused state if there is none).
func desiredSchedule(src source.Source, state *client.ScheduleState) *client.Schedule {
	if state == nil {
		state = &client.ScheduleState{}
	}
	spec := scheduleSpec(src)
	return &client.Schedule{
		Action: scheduleAction(src),
		Spec:   &spec,
		Policy: &client.SchedulePolicies{
			Overlap:       enumspb.SCHEDULE_OVERLAP_POLICY_SKIP,
			CatchupWindow: ScheduleCatchupWindow,
		},
		State: state,
	}
}
