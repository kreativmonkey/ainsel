package controller

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ainselv1alpha1 "github.com/ainsel-project/ainsel/shared/api/api/v1alpha1"
)

// These tests drive reconcileDeployment so the assertion is about the object
// that would be written to the cluster: whatever the queue says, the Deployment
// is the pod template the per-task Jobs are built from, and it must stay at
// zero replicas. A queue-driven pod count would mean two executors for one
// queue.
//
// The image reference is digest-pinned on purpose: a mutable tag makes
// resolveImageDigest reach for a registry, which has no business in a unit test.

const pinnedImage = "local.test/ainsel-pi@sha256:0000000000000000000000000000000000000000000000000000000000000000"

func scalingFixtures() *runtime.Scheme {
	s := runtime.NewScheme()
	utilruntime.Must(corev1.AddToScheme(s))
	utilruntime.Must(appsv1.AddToScheme(s))
	utilruntime.Must(batchv1.AddToScheme(s))
	utilruntime.Must(ainselv1alpha1.AddToScheme(s))
	return s
}

func imageObject() *ainselv1alpha1.AgentImage {
	img := &ainselv1alpha1.AgentImage{}
	img.Name = "img"
	img.Namespace = "ns"
	img.Spec = ainselv1alpha1.AgentImageSpec{
		DisplayName: "Test",
		ImageURL:    pinnedImage,
		Tools:       []ainselv1alpha1.AgentImageTool{{Name: "git"}},
	}
	return img
}

func scalingAgent(scaling *ainselv1alpha1.AgentScaling) *ainselv1alpha1.Agent {
	a := &ainselv1alpha1.Agent{}
	a.Name = "worker"
	a.Namespace = "ns"
	a.Spec = ainselv1alpha1.AgentSpec{
		DisplayName:  "Worker",
		ImageRef:     ainselv1alpha1.AgentImageRef{Name: "img"},
		Runtime:      ainselv1alpha1.AgentRuntime{},
		LLM:          ainselv1alpha1.AgentLLM{Model: "glm-5.1:cloud", Provider: ainselv1alpha1.AgentLLMProviderOllamaCloud},
		Persona:      ainselv1alpha1.AgentPersona{ID: "01hxtestpersona00000000000"},
		EnabledTools: []string{"git"},
		Scaling:      scaling,
	}
	return a
}

// deploymentFor runs the real Deployment reconciliation and returns the object it
// produced, so assertions are about what would be written to the cluster.
func deploymentFor(t *testing.T, agent *ainselv1alpha1.Agent, existing *appsv1.Deployment) *appsv1.Deployment {
	t.Helper()
	scheme := scalingFixtures()

	objs := []runtime.Object{imageObject(), agent}
	if existing != nil {
		objs = append(objs, existing)
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(objs...).
		WithStatusSubresource(&ainselv1alpha1.Agent{}).
		Build()

	r := &AgentReconciler{Client: c, Scheme: scheme}
	img := &ainselv1alpha1.AgentImage{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "ns", Name: "img"}, img); err != nil {
		t.Fatalf("load agent image: %v", err)
	}

	deploy, _, err := r.reconcileDeployment(context.Background(), agent, "agent-worker", img, img.Spec.ImageURL)
	if err != nil {
		t.Fatalf("reconcileDeployment: %v", err)
	}
	if deploy == nil {
		t.Fatal("reconcileDeployment returned no Deployment")
	}
	return deploy
}

func existingDeployment(replicas int32) *appsv1.Deployment {
	d := &appsv1.Deployment{}
	d.Name = "agent-worker"
	d.Namespace = "ns"
	d.Spec = appsv1.DeploymentSpec{
		Replicas: ptr.To(replicas),
		Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "agent-worker"}},
		Template: corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "agent", Image: pinnedImage}}},
		},
	}
	return d
}

// The Deployment is pinned at zero even with a queue full of work: the work
// runs as per-task Jobs, not as Deployment pods.
func TestReconcileDeploymentPinnedAtZeroWithWork(t *testing.T) {
	a := scalingAgent(&ainselv1alpha1.AgentScaling{Replicas: ptr.To(int32(4))})
	a.Status.PendingTasks = 3
	a.Status.ActiveTasks = 1

	deploy := deploymentFor(t, a, existingDeployment(2))
	if deploy.Spec.Replicas == nil || *deploy.Spec.Replicas != 0 {
		t.Errorf("Deployment replicas = %v, want 0 regardless of queue depth",
			deploy.Spec.Replicas)
	}
}

// The same, with an existing Deployment scaled up (e.g. left over from before
// the conversion): reconciliation must bring it back down to zero.
func TestReconcileDeploymentBringsStrayPodsDown(t *testing.T) {
	a := scalingAgent(nil)

	deploy := deploymentFor(t, a, existingDeployment(3))
	if deploy.Spec.Replicas == nil || *deploy.Spec.Replicas != 0 {
		t.Errorf("Deployment replicas = %v, want 0 even for an agent with no scaling block",
			deploy.Spec.Replicas)
	}
}

// A brand-new agent also starts at zero: its first task gets a Job, not a
// warm pod.
func TestReconcileDeploymentCreatesAgentAtZero(t *testing.T) {
	a := scalingAgent(&ainselv1alpha1.AgentScaling{Replicas: ptr.To(int32(2))})

	deploy := deploymentFor(t, a, nil)
	if deploy.Spec.Replicas == nil || *deploy.Spec.Replicas != 0 {
		t.Errorf("new agent Deployment replicas = %v, want 0", deploy.Spec.Replicas)
	}
}
