package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	ainselv1alpha1 "github.com/ainsel-project/ainsel/shared/api/api/v1alpha1"
	"github.com/ainsel-project/ainsel/shared/api/tasktoken"
)

// Per-task Jobs: every queued task runs as one Kubernetes Job — a pod starts,
// processes exactly one task, and exits. The contract that makes this safe is
// that a task row stays 'pending' until its worker acks it — there is no claim
// for a dead worker to strand. If a task pod dies mid-flight the Job starts a
// fresh pod which fetches the same task again; queue recovery is therefore a
// pod restart, not a walk with the claim reaper.
//
// The work list arrives on Agent status (status.pendingTaskIds), written by the
// hub's queue-signal publisher. The operator never talks to the hub: both
// existing channels keep their direction — the hub publishes facts about the
// queue, the operator turns them into workloads.

const (
	// TaskIDLabel carries the agent_tasks row id a Job was created for. It is
	// the dedup key: the operator must never run two live pods for one task.
	TaskIDLabel = "ainsel.dev/task-id"

	// AgentNameLabel carries the agent name on every task Job; it is the
	// selector the dispatch loop lists by.
	AgentNameLabel = "ainsel.dev/agent"

	// taskJobBackoffLimit is the in-Job pod retry budget. It mirrors the hub's
	// default max_attempts (10): a pod that fails nacks the task, and the Job
	// gives it a fresh pod until either the ack lands or the hub marks the row
	// failed — after which the next pod sees the terminal status and exits 0.
	taskJobBackoffLimit int32 = 9

	// taskJobTTLAfterFinished lets finished Jobs age out of the cluster. They
	// double as the recent history of what ran, but the task row in the hub is
	// the durable record, so an hour is enough for a human to notice.
	taskJobTTLAfterFinished int32 = 3600

	// taskJobGracePeriod is short on purpose: a task pod has nothing of its own
	// to drain. A kill mid-task leaves the row pending, and the Job retry picks
	// it up seconds later — which is the entire point of this mode.
	taskJobGracePeriod int64 = 30

	// taskRunVolumeName is the shared emptyDir that carries the handoff from the
	// runtime to the sidecars of a task pod (see taskRunDir).
	taskRunVolumeName = "task-run"

	// taskRunDir is where a task pod's containers agree to meet. The runtime
	// writes its own PID to <taskRunDir>/agent.pid as its first act; a sidecar
	// watches that PID and exits when it goes away. It has to be a shared
	// volume: the emptyDir kubelet creates is world-readable (fsGroup only adds
	// group bits), so the sidecar can read a file the runtime — a different
	// UID — wrote.
	taskRunDir = "/ainsel-task"

	// taskJobDispatchPoll rechecks the due-id list when work remains but no
	// slot was free (or the published list has not caught up yet).
	taskJobDispatchPoll = 10 * time.Second

	// maxK8sNameLen is the RFC 1123 length limit for object names.
	maxK8sNameLen = 63
)

//+kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=batch,resources=jobs/status,verbs=get

// taskJobName names the Job for one task. It is deterministic — the same task
// always maps to the same name — so "does this task already have a Job?" is an
// existence check, and concurrent reconciles cannot double-dispatch. Agent
// names are already RFC 1123 safe, so the common path is a plain join; the
// long-name path keeps the id (the part that must stay unique) and hashes the
// rest.
func taskJobName(agentName string, taskID int64) string {
	name := fmt.Sprintf("task-%s-%d", agentName, taskID)
	if len(name) <= maxK8sNameLen {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	suffix := hex.EncodeToString(sum[:])[:5]
	// "task-" + hash + "-" + id must fit; the id is the tail we refuse to drop.
	idStr := strconv.FormatInt(taskID, 10)
	head := maxK8sNameLen - len(suffix) - 1 - len(idStr) - len("-")
	if head < len("task-") {
		head = len("task-")
	}
	// A cut landing right after a hyphen would leave "task--<hash>-<id>".
	// Double hyphens are legal in a DNS-1123 subdomain, but they trip stricter
	// downstream validators and read as a bug — trim the seam.
	prefix := strings.TrimSuffix(name[:head], "-")
	return fmt.Sprintf("%s-%s-%s", prefix, suffix, idStr)
}

// buildTaskJob renders the Job for one task from the agent's Deployment pod
// template. Reusing the Deployment (which reconcileDeployment keeps current —
// image digests, env, sidecars, persona mounts) rather than rebuilding a
// template from the Agent spec is deliberate: the two workload kinds cannot
// drift apart. The Deployment itself stays pinned at zero replicas, so it is
// the template's home, not a second executor.
func buildTaskJob(agent *ainselv1alpha1.Agent, agentName string, deploy *appsv1.Deployment, taskID int64) *batchv1.Job {
	labels := map[string]string{}
	for k, v := range deploy.Labels {
		labels[k] = v
	}
	labels[TaskIDLabel] = strconv.FormatInt(taskID, 10)

	tpl := deploy.Spec.Template.DeepCopy()
	tpl.Labels = map[string]string{}
	for k, v := range deploy.Spec.Template.Labels {
		tpl.Labels[k] = v
	}
	tpl.Labels[TaskIDLabel] = strconv.FormatInt(taskID, 10)

	// Jobs own their pods outright: a failed task pod must not restart in place
	// (that is the Job retry's job, so the hub sees one nack per attempt), and
	// there is no long-running container to keep alive.
	tpl.Spec.RestartPolicy = corev1.RestartPolicyNever
	tpl.Spec.TerminationGracePeriodSeconds = ptr.To(taskJobGracePeriod)

	// The runtime container learns which task it owns; the chat sidecar and the
	// setup init do not care.
	env := corev1.EnvVar{Name: "AINSEL_TASK_ID", Value: strconv.FormatInt(taskID, 10)}
	if nativeRuntime(agent) {
		// Bound credential exposure to 24 hours, including retries. Login pods
		// and the reusable Deployment template carry no hub token.
		token := tasktoken.Sign(resolveHubInternalToken(), agent.Name, taskID, time.Now().Add(24*time.Hour))
		for i := range tpl.Spec.Containers {
			if tpl.Spec.Containers[i].Name == "agent" {
				tpl.Spec.Containers[i].Env = append(tpl.Spec.Containers[i].Env, corev1.EnvVar{Name: "HUB_INTERNAL_VALIDATE_SECRET", Value: token})
			}
		}
	}
	injected := false
	for i := range tpl.Spec.Containers {
		if tpl.Spec.Containers[i].Name == "agent" {
			tpl.Spec.Containers[i].Env = append(tpl.Spec.Containers[i].Env, env)
			injected = true
		}
	}
	if !injected && len(tpl.Spec.Containers) > 0 {
		tpl.Spec.Containers[0].Env = append(tpl.Spec.Containers[0].Env, env)
	}

	// A task pod exists to run one process: the runtime. A sidecar copied from
	// the Deployment template (the chat MCP server, or an AgentImage's own) is a
	// long-lived HTTP server with no task of its own, and it would outlive the
	// task: the pod stays Running, so the Job never gains a completion
	// condition, and its slot in the agent's concurrency ceiling is held by a
	// corpse — a nacked task cannot be re-dispatched either, because the
	// operator still counts the Job as in flight. So the pod shares its PID
	// namespace and hands the runtime's PID to every container: a sidecar that
	// follows the contract ends with the task and the pod finishes as a unit.
	tpl.Spec.ShareProcessNamespace = ptr.To(!nativeRuntime(agent))
	tpl.Spec.Volumes = append(tpl.Spec.Volumes, corev1.Volume{
		Name:         taskRunVolumeName,
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
	})
	for i := range tpl.Spec.Containers {
		c := &tpl.Spec.Containers[i]
		c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{
			Name:      taskRunVolumeName,
			MountPath: taskRunDir,
		})
		c.Env = append(c.Env, corev1.EnvVar{Name: "AINSEL_TASK_DIR", Value: taskRunDir})
	}

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      taskJobName(agent.Name, taskID),
			Namespace: agent.Namespace,
			Labels:    labels,
		},
		Spec: batchv1.JobSpec{
			Completions:             ptr.To(int32(1)),
			Parallelism:             ptr.To(int32(1)),
			BackoffLimit:            ptr.To(taskJobBackoffLimit),
			TTLSecondsAfterFinished: ptr.To(taskJobTTLAfterFinished),
			Template:                *tpl,
		},
	}
}

// jobIsFinished reports whether a Job has reached a terminal condition, so its
// pod slot is free even before the TTL collector deletes it.
func jobIsFinished(j *batchv1.Job) bool {
	for _, c := range j.Status.Conditions {
		if (c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed) && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// syncPerTaskJobs fans the hub's published due-task ids out into Jobs and
// reports the running picture for status. The concurrency ceiling is
// spec.scaling.replicas: "how many runs at once", one pod per task.
func (r *AgentReconciler) syncPerTaskJobs(ctx context.Context, agent *ainselv1alpha1.Agent, agentName string, deploy *appsv1.Deployment) (ScaleDecision, error) {
	log := logf.FromContext(ctx)
	limit := concurrencyCap(agent)

	var jobList batchv1.JobList
	if err := r.List(ctx, &jobList,
		client.InNamespace(agent.Namespace),
		client.MatchingLabels{AgentNameLabel: agent.Name},
	); err != nil {
		return ScaleDecision{}, fmt.Errorf("listing task jobs: %w", err)
	}

	// The hub's due list is the work order: an id on it means the task is
	// pending and its retry backoff (if any) has elapsed.
	pending := make(map[int64]bool, len(agent.Status.PendingTaskIDs))
	for _, id := range agent.Status.PendingTaskIDs {
		pending[id] = true
	}

	active := int32(0)
	have := make(map[int64]bool, len(jobList.Items))
	retired := 0
	for i := range jobList.Items {
		j := &jobList.Items[i]
		id, err := strconv.ParseInt(j.Labels[TaskIDLabel], 10, 64)
		if err != nil {
			if !jobIsFinished(j) {
				active++
			}
			continue
		}
		if !jobIsFinished(j) {
			have[id] = true
			active++
			continue
		}
		// A finished Job whose task the hub still lists as due is a dead
		// attempt: the pod ended without an ack (never started, image pull
		// failure, or the Job's backoffLimit ran out while the row stayed
		// pending). Keeping it would strand the task — the deterministic name
		// makes a re-dispatch collide with the corpse until the TTL collector
		// deletes it an hour later. Retire it now; the next pass dispatches a
		// fresh attempt. The task row is the durable record, the Job is only
		// history.
		if pending[id] && limit > 0 {
			// Cascade explicitly. A Job delete with no propagation policy defaults
			// to Orphan — batch/v1 is the one resource that does this, and the API
			// server only says so as a warning to kubectl — which strips the pod's
			// ownerRef and leaves the finished pod behind with nothing owning it.
			// Nothing collects those: the TTL belongs to the Job, and the Job is
			// gone. This branch runs on every re-dispatch, so the leak was one pod
			// per retried task, and they only ever stop appearing when the operator
			// stops retiring Jobs.
			if err := r.Delete(ctx, j, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil && !apierrors.IsNotFound(err) {
				return ScaleDecision{}, fmt.Errorf("retiring finished task job for still-pending task %d: %w", id, err)
			}
			retired++
			log.Info("retired finished task job for still-pending task", "agent", agent.Name, "task", id, "job", j.Name)
			continue
		}
		// Finished and no longer due: keep as history until the TTL. No have[]
		// entry is needed — a task only reaches the dispatch loop by being due,
		// and if it becomes due again this branch retires the Job first.
	}

	decision := ScaleDecision{
		Replicas: active,
		Reason:   ScaleReasonJobPerTask,
	}

	if nativeRuntime(agent) {
		waiting, err := r.syncSubscriptionLogin(ctx, agent, agentName, deploy, active)
		if err != nil {
			return decision, err
		}
		if waiting {
			decision.Message = "subscription login: task dispatch withheld"
			decision.RequeueAfter = taskJobDispatchPoll
			return decision, nil
		}
	}

	if limit == 0 {
		// spec.scaling.replicas: 0 — an explicit "none": nothing is
		// dispatched, existing Jobs run out.
		decision.Message = "agent paused (replicas: 0)"
		return decision, nil
	}

	if deploy == nil {
		decision.Message = fmt.Sprintf("%d task job(s) active; no pod template yet", active)
		return decision, nil
	}

	unscheduled := 0
	for _, taskID := range agent.Status.PendingTaskIDs {
		if have[taskID] {
			continue // a live Job already owns this task
		}
		// The cap is best-effort against the informer cache: a Job created by a
		// previous pass that has not landed in the cache yet is not counted in
		// active, so fast back-to-back reconciles can briefly exceed the ceiling
		// by a job or two. Bounded (one pass per reconcile) and self-correcting
		// (the next List sees everything), which is an acceptable trade for not
		// serialising dispatch on a live API read.
		if active >= limit {
			unscheduled++
			continue
		}
		job := buildTaskJob(agent, agentName, deploy, taskID)
		if err := controllerutil.SetControllerReference(agent, job, r.Scheme); err != nil {
			return decision, fmt.Errorf("setting owner reference on task job: %w", err)
		}
		if err := r.Create(ctx, job); err != nil {
			if apierrors.IsAlreadyExists(err) {
				// Another reconcile (or a racing hub signal) got here first —
				// the deterministic name made that race harmless.
				have[taskID] = true
				continue
			}
			return decision, fmt.Errorf("creating task job for task %d: %w", taskID, err)
		}
		active++
		log.Info("dispatched task job", "agent", agent.Name, "task", taskID, "job", job.Name)
	}

	if unscheduled > 0 || retired > 0 {
		// unscheduled: work waits for a slot or for the hub's list to catch up.
		// retired: the replacement dispatch may have raced the deletion of the
		// finished Job (AlreadyExists is swallowed below); the delete event
		// usually re-triggers this reconcile anyway, the requeue is the net.
		if unscheduled > 0 {
			decision.Reason = ScaleReasonCapped
			decision.Message = fmt.Sprintf("%d/%d task jobs active, %d waiting for a slot", active, limit, unscheduled)
		}
		decision.RequeueAfter = taskJobDispatchPoll
	} else {
		decision.Message = fmt.Sprintf("%d task job(s) active", active)
		// Work the hub may not have told us about yet (published lists can lag
		// the queue by a sweep) still needs a look, but nothing urgent.
		decision.RequeueAfter = 0
	}
	// Report what this pass actually left running, not the count from before it
	// dispatched: status.scaling.desired should not read 0 on the very pass that
	// started the first Jobs.
	decision.Replicas = active
	return decision, nil
}
