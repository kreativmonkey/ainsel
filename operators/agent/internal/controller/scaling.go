package controller

import (
	"time"

	ainselv1alpha1 "github.com/ainsel-project/ainsel/shared/api/api/v1alpha1"
)

// ScaleReasonJobPerTask is the reason reported while work runs as per-task
// Jobs; the message carries the counts (active, waiting).
const ScaleReasonJobPerTask = "JobPerTask"

// ScaleReasonCapped is the reason reported when more tasks are due than the
// concurrency cap (spec.scaling.replicas) allows: at least one due task is
// waiting for a slot. Documented as a stable code on status.scaling.reason.
const ScaleReasonCapped = "Capped"

// ScaleDecision is the operator's answer to "how much of this agent's work is
// running right now", plus the reasoning that made it so. In the per-task Job
// model Replicas is the number of task Jobs in flight — the Deployment itself
// stays at zero and exists only as the pod template those Jobs are built from.
type ScaleDecision struct {
	Replicas int32
	Reason   string
	Message  string
	// RequeueAfter asks to be reconsidered sooner than the normal period. Zero
	// means the normal period is fine.
	RequeueAfter time.Duration
}

// concurrencyCap is the ceiling on simultaneously running task Jobs for an
// agent: spec.scaling.replicas, defaulting to one. Its meaning predates the Job
// model as "pods running at once" — one pod per task, bounded, is the same
// number. Zero pauses the agent: due tasks stay queued.
func concurrencyCap(agent *ainselv1alpha1.Agent) int32 {
	if agent.Spec.Scaling != nil && agent.Spec.Scaling.Replicas != nil {
		limit := *agent.Spec.Scaling.Replicas
		if subscriptionRuntime(agent) && limit > 1 {
			return 1
		}
		return limit
	}
	return 1
}
