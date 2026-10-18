package controller

import (
	"context"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	api "github.com/ainsel-project/ainsel/shared/api/api/v1alpha1"
)

// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;delete

// Initialize private CLI homes on a fresh PVC before starting either process.
func subscriptionCommand(command ...string) []string {
	return append([]string{"sh", "-c", `mkdir -p "$CODEX_HOME" "$CLAUDE_CONFIG_DIR" && exec "$@"`, "--"}, command...)
}

func nativeRuntime(agent *api.Agent) bool {
	return agent.Spec.Runtime.Engine == "codex" || agent.Spec.Runtime.Engine == "claude"
}

func (r *AgentReconciler) validateSubscription(ctx context.Context, agent *api.Agent) error {
	sub := agent.Spec.Runtime.Subscription
	if !nativeRuntime(agent) {
		if agent.Spec.Runtime.Engine != "" && agent.Spec.Runtime.Engine != "pi" {
			return fmt.Errorf("unsupported runtime engine %q", agent.Spec.Runtime.Engine)
		}
		if sub != nil {
			return fmt.Errorf("subscription requires runtime.engine codex or claude")
		}
		return nil
	}
	if sub == nil || sub.ClaimName == "" {
		return fmt.Errorf("native runtime requires runtime.subscription.claimName")
	}
	if agent.Spec.Runtime.SecurityHardened != nil && !*agent.Spec.Runtime.SecurityHardened {
		return fmt.Errorf("native runtime requires securityHardened")
	}
	var pvc corev1.PersistentVolumeClaim
	if err := r.Get(ctx, types.NamespacedName{Namespace: agent.Namespace, Name: sub.ClaimName}, &pvc); err != nil {
		return fmt.Errorf("subscription home PVC: %w", err)
	}
	if pvc.Spec.VolumeMode != nil && *pvc.Spec.VolumeMode != corev1.PersistentVolumeFilesystem {
		return fmt.Errorf("subscription home PVC must use Filesystem volume mode")
	}
	if len(pvc.Spec.AccessModes) != 1 || pvc.Spec.AccessModes[0] != corev1.ReadWriteOncePod {
		return fmt.Errorf("subscription home PVC must use ReadWriteOncePod for exclusive CLI authentication access")
	}
	return nil
}

func subscriptionEnv(agent *api.Agent) []corev1.EnvVar {
	return []corev1.EnvVar{
		{Name: "AINSEL_RUNTIME", Value: agent.Spec.Runtime.Engine},
		{Name: "HOME", Value: "/home/agent"},
		{Name: "CODEX_HOME", Value: "/home/agent/.codex"},
		{Name: "CLAUDE_CONFIG_DIR", Value: "/home/agent/.claude"},
	}
}

func configureSubscriptionPlan(agent *api.Agent, plan *podPlan) {
	// Native CLIs own their entire private home; Pi configuration is ephemeral
	// and has no place in it. Do not copy or inspect native auth files.
	volumes := plan.podVolumes[:0]
	for _, v := range plan.podVolumes {
		if v.Name != "pi-home" && v.Name != "pi-models" && v.Name != "agent-skills" {
			volumes = append(volumes, v)
		}
	}
	plan.podVolumes = append(volumes, corev1.Volume{Name: "subscription-home", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: agent.Spec.Runtime.Subscription.ClaimName}}})
	mounts := plan.agentVolumeMounts[:0]
	for _, m := range plan.agentVolumeMounts {
		if m.Name != "pi-home" {
			mounts = append(mounts, m)
		}
	}
	plan.agentVolumeMounts = append(mounts, corev1.VolumeMount{Name: "subscription-home", MountPath: "/home/agent"})
	plan.initContainers = nil
}

func subscriptionLoginName(agentName string) string {
	// Reuse the length-safe task naming helper, with a separate name prefix.
	return strings.TrimPrefix(taskJobName("login-"+agentName, 0), "task-")
}

// syncSubscriptionLogin drains Jobs before opening a login pod and waits for
// that pod to disappear before resuming tasks. RWOP is the final cross-agent
// exclusion guard, including while Kubernetes is terminating a pod.
func (r *AgentReconciler) syncSubscriptionLogin(ctx context.Context, agent *api.Agent, agentName string, deploy *appsv1.Deployment, active int32) (bool, error) {
	name := subscriptionLoginName(agent.Name)
	var pod corev1.Pod
	err := r.Get(ctx, types.NamespacedName{Namespace: agent.Namespace, Name: name}, &pod)
	if err != nil && !apierrors.IsNotFound(err) {
		return true, err
	}
	login := agent.Spec.Runtime.Subscription.Login
	if err == nil {
		if !metav1.IsControlledBy(&pod, agent) {
			return true, fmt.Errorf("subscription login pod %s is not owned by agent", name)
		}
		if !login && pod.DeletionTimestamp == nil {
			if err := r.Delete(ctx, &pod); err != nil && !apierrors.IsNotFound(err) {
				return true, err
			}
		}
		return true, nil
	}
	if !login {
		return false, nil
	}
	if active > 0 || deploy == nil {
		return true, nil
	}
	tpl := deploy.Spec.Template.DeepCopy()
	c := tpl.Spec.Containers[0]
	c.Name = "login"
	c.WorkingDir = "/home/agent"
	c.Command = subscriptionCommand("sleep", "infinity")
	c.Args = nil
	// Login only receives the CLI home configuration. It has no hub token,
	// provider keys, persona files, sidecars or service account credentials.
	c.Env = subscriptionEnv(agent)
	c.VolumeMounts = []corev1.VolumeMount{{Name: "subscription-home", MountPath: "/home/agent"}, {Name: "tmp", MountPath: "/tmp"}}
	pod = corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: agent.Namespace, Labels: map[string]string{AgentNameLabel: agent.Name, "ainsel.dev/subscription-login": "true"}}, Spec: corev1.PodSpec{
		Containers: []corev1.Container{c}, SecurityContext: tpl.Spec.SecurityContext,
		AutomountServiceAccountToken: ptr.To(false), RestartPolicy: corev1.RestartPolicyNever,
		Volumes: []corev1.Volume{
			{Name: "subscription-home", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: agent.Spec.Runtime.Subscription.ClaimName}}},
			{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		},
	}}
	if err := controllerutil.SetControllerReference(agent, &pod, r.Scheme); err != nil {
		return true, err
	}
	if err := r.Create(ctx, &pod); err != nil && !apierrors.IsAlreadyExists(err) {
		return true, err
	}
	return true, nil
}

// Preserve existing Pi behavior while isolating native subscription workers.
func subscriptionServiceAccountToken(agent *api.Agent) *bool {
	if nativeRuntime(agent) {
		return ptr.To(false)
	}
	return nil
}
