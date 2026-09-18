package orchestrator

import (
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/DMokong/data-platform/internal/source"
	"github.com/DMokong/data-platform/internal/source/beads"
)

// TaskQueue is the one Temporal task queue the worker polls and every workflow and activity of
// this package runs on.
const TaskQueue = "data-platform"

// Registered workflow and activity type names. Workflows and activities are registered, started
// and mocked by these names, so renaming a Go function never silently renames a type that running
// histories, schedules and the runbook refer to.
const (
	WorkflowMaterialiseSource = "MaterialiseSource"
	WorkflowMaterialiseWindow = "MaterialiseWindow"
	WorkflowBuildMarts        = "BuildMarts"
	ActivityFetchWindow       = "FetchWindow"
	ActivityWriteWindow       = "WriteWindow"
	ActivityDeleteSpool       = "DeleteSpool"
	ActivityDbtBuild          = "DbtBuild"
	ActivityRunTransform      = "RunTransform"
)

// Declarations returns every declared source, keyed by source name. It is static data built from
// each source package's declaration, so workflow code may read it without breaking replay. Each
// call returns a fresh map of fresh declarations; a caller that mutates its copy affects no one.
func Declarations() map[string]source.Source {
	b := beads.Declaration()
	return map[string]source.Source{b.Name: b}
}

// Register registers this package's workflows and a's activities on r under the explicit names
// above.
func Register(r worker.Registry, a *Activities) {
	r.RegisterWorkflowWithOptions(MaterialiseSource, workflow.RegisterOptions{Name: WorkflowMaterialiseSource})
	r.RegisterWorkflowWithOptions(MaterialiseWindow, workflow.RegisterOptions{Name: WorkflowMaterialiseWindow})
	r.RegisterWorkflowWithOptions(BuildMarts, workflow.RegisterOptions{Name: WorkflowBuildMarts})
	r.RegisterActivityWithOptions(a.FetchWindow, activity.RegisterOptions{Name: ActivityFetchWindow})
	r.RegisterActivityWithOptions(a.WriteWindow, activity.RegisterOptions{Name: ActivityWriteWindow})
	r.RegisterActivityWithOptions(a.DeleteSpool, activity.RegisterOptions{Name: ActivityDeleteSpool})
	r.RegisterActivityWithOptions(a.DbtBuild, activity.RegisterOptions{Name: ActivityDbtBuild})
	r.RegisterActivityWithOptions(a.RunTransform, activity.RegisterOptions{Name: ActivityRunTransform})
}
