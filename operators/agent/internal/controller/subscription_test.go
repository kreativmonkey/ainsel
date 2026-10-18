package controller

import (
	"context"
	"github.com/ainsel-project/ainsel/shared/api/tasktoken"
	appsv1 "k8s.io/api/apps/v1"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/ainsel-project/ainsel/shared/api/api/v1alpha1"
)

func subscriptionAgent() *api.Agent {
	a := jobModeAgent(5, 1, 2)
	a.UID = types.UID("agent-uid")
	a.Spec.Runtime.Engine = "codex"
	a.Spec.Runtime.Subscription = &api.AgentSubscription{ClaimName: "private-home"}
	return a
}

func TestSubscriptionRequiresExclusivePVC(t *testing.T) {
	for _, mode := range []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce, corev1.ReadWriteOncePod} {
		t.Run(string(mode), func(t *testing.T) {
			a := subscriptionAgent()
			pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "private-home", Namespace: a.Namespace}, Spec: corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{mode}}}
			r := &AgentReconciler{Client: fake.NewClientBuilder().WithScheme(jobModeScheme()).WithObjects(pvc).Build()}
			err := r.validateSubscription(context.Background(), a)
			if (err == nil) != (mode == corev1.ReadWriteOncePod) {
				t.Fatalf("mode %s: %v", mode, err)
			}
		})
	}
}

func TestSubscriptionTemplateIsolation(t *testing.T) {
	t.Setenv("HUB_INTERNAL_VALIDATE_SECRET", "cluster-secret")
	a := subscriptionAgent()
	img := &api.AgentImage{Spec: api.AgentImageSpec{Env: []api.AgentImageEnvVar{
		{Name: "ANTHROPIC_API_KEY", Value: "must-not-be-injected"},
		{Name: "HOME", Value: "/override"},
	}}}
	plan := newPodPlan(a, "agent-worker", img)
	configureSubscriptionPlan(a, plan)
	env := buildAgentEnv(context.Background(), a, "agent-worker", img, plan)
	tpl := (&AgentReconciler{}).renderPodTemplate(a, img, "native:1", plan, env)
	if len(tpl.Spec.InitContainers) != 0 {
		t.Fatal("native runtime initialized Pi")
	}
	if tpl.Spec.AutomountServiceAccountToken == nil || *tpl.Spec.AutomountServiceAccountToken {
		t.Fatal("native worker mounts service account")
	}
	if tpl.Spec.Containers[0].WorkingDir != "/workspace" {
		t.Fatal("native CLI lacks writable work directory")
	}
	for _, e := range tpl.Spec.Containers[0].Env {
		if e.Name == "HOME" && e.Value != "/home/agent" {
			t.Fatal("profile overrode private home")
		}
		if e.Name == "HUB_INTERNAL_VALIDATE_SECRET" || e.Name == "OLLAMA_API_KEY" || e.Name == "ANTHROPIC_API_KEY" {
			t.Fatal("native worker received API key")
		}
	}
	job := buildTaskJob(a, "agent-worker", &appsv1.Deployment{Spec: appsv1.DeploymentSpec{Template: tpl}}, 1)
	token := findEnvVar(job.Spec.Template.Spec.Containers[0].Env, "HUB_INTERNAL_VALIDATE_SECRET")
	if token == nil || token.Value == "cluster-secret" {
		t.Fatal("native Job lacks scoped token")
	}
	claims, err := tasktoken.Verify("cluster-secret", token.Value, time.Now())
	if err != nil || claims.Agent != a.Name || claims.Task != 1 {
		t.Fatalf("Job token scope: %+v, %v", claims, err)
	}
	for _, e := range tpl.Spec.Containers[0].Env {
		if e.Name == "HUB_INTERNAL_VALIDATE_SECRET" {
			t.Fatal("Job mutated template token")
		}
	}

	if *job.Spec.Template.Spec.ShareProcessNamespace {
		t.Fatal("private CLI home exposed through shared PID namespace")
	}
	if concurrencyCap(a) != 1 {
		t.Fatal("subscription concurrency must be one")
	}
	a.Spec.Scaling.Replicas = ptr.To(int32(0))
	if concurrencyCap(a) != 0 {
		t.Fatal("explicit pause ignored")
	}
}

func TestSubscriptionLoginDrainsAndWithholdsDispatch(t *testing.T) {
	ctx := context.Background()
	a := subscriptionAgent()
	a.Spec.Runtime.Subscription.Login = true
	scheme := jobModeScheme()
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &AgentReconciler{Client: c, Scheme: scheme}
	deploy := jobModeDeployment()
	deploy.Spec.Template.Spec.Containers[0].Env = []corev1.EnvVar{{Name: "HUB_INTERNAL_VALIDATE_SECRET", Value: "must-not-leak"}}
	waiting, err := r.syncSubscriptionLogin(ctx, a, "agent-worker", deploy, 1)
	if err != nil || !waiting {
		t.Fatalf("draining: %v %v", waiting, err)
	}
	var pods corev1.PodList
	if err := c.List(ctx, &pods); err != nil {
		t.Fatal(err)
	}
	if len(pods.Items) != 0 {
		t.Fatal("login overlaps active job")
	}
	waiting, err = r.syncSubscriptionLogin(ctx, a, "agent-worker", deploy, 0)
	if err != nil || !waiting {
		t.Fatalf("login: %v %v", waiting, err)
	}
	if err := c.List(ctx, &pods); err != nil {
		t.Fatal(err)
	}
	if len(pods.Items) != 1 {
		t.Fatal("login pod missing")
	}
	for _, e := range pods.Items[0].Spec.Containers[0].Env {
		if e.Name == "HUB_INTERNAL_VALIDATE_SECRET" {
			t.Fatal("login receives hub secret")
		}
	}
	a.Spec.Runtime.Subscription.Login = false
	waiting, err = r.syncSubscriptionLogin(ctx, a, "agent-worker", deploy, 0)
	if err != nil || !waiting {
		t.Fatalf("deletion must withhold jobs: %v %v", waiting, err)
	}
	waiting, err = r.syncSubscriptionLogin(ctx, a, "agent-worker", deploy, 0)
	if err != nil || waiting {
		t.Fatalf("resume: %v %v", waiting, err)
	}
}
