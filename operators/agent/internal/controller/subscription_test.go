package controller

import (
	"context"
	"encoding/json"
	"github.com/ainsel-project/ainsel/shared/api/tasktoken"
	appsv1 "k8s.io/api/apps/v1"
	"os"
	"os/exec"
	"path/filepath"
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
	verifySubscriptionHomeStartup(t, tpl.Spec.Containers[0])
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
	verifySubscriptionHomeStartup(t, pods.Items[0].Spec.Containers[0])
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

// Execute the rendered startup command against an empty home, replacing only
// the long-running process with a probe that requires initialized CLI homes.
//
//nolint:gosec // Executable test fixtures and commands generated by the operator.
func verifySubscriptionHomeStartup(t *testing.T, container corev1.Container) {
	t.Helper()
	home := t.TempDir()
	bin := t.TempDir()
	probe := []byte("#!/bin/sh\ntest -d \"$CODEX_HOME\" && test -d \"$CLAUDE_CONFIG_DIR\"\n")
	for _, name := range []string{"node", "sleep"} {
		if err := os.WriteFile(filepath.Join(bin, name), probe, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	cmd := exec.Command(container.Command[0], container.Command[1:]...)
	cmd.Env = append(os.Environ(), "HOME="+home, "CODEX_HOME="+filepath.Join(home, ".codex"), "CLAUDE_CONFIG_DIR="+filepath.Join(home, ".claude"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fresh subscription home startup: %v: %s", err, output)
	}
}

func TestPiSubscriptionPreservesRuntimeAndPersistsAuth(t *testing.T) {
	a := subscriptionAgent()
	a.Spec.Runtime.Engine = "pi"
	a.Spec.LLM.Provider = "openai-codex"
	img := &api.AgentImage{}
	plan := newPodPlan(a, "agent-worker", img)
	initCount := len(plan.initContainers)
	configureSubscriptionPlan(a, plan)
	if initCount == 0 || len(plan.initContainers) != initCount {
		t.Fatal("Pi initialization was removed")
	}
	found := false
	for _, v := range plan.podVolumes {
		if v.Name == "pi-home" {
			found = v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName == "private-home"
		}
	}
	if !found {
		t.Fatal("Pi authentication directory is not persistent")
	}
	env := buildAgentEnv(context.Background(), a, "agent-worker", img, plan)
	tpl := (&AgentReconciler{}).renderPodTemplate(a, img, "pi:1", plan, env)
	if len(tpl.Spec.Containers[0].Command) != 0 {
		t.Fatal("Pi entrypoint replaced by native runner")
	}
	if e := findEnvVar(env, "PI_PROVIDER"); e == nil || e.Value != "openai-codex" {
		t.Fatal("Pi provider changed")
	}
	if concurrencyCap(a) != 1 {
		t.Fatal("Pi subscription concurrency must be one")
	}
	a.Spec.Runtime.Subscription.Login = true
	c := fake.NewClientBuilder().WithScheme(jobModeScheme()).Build()
	r := &AgentReconciler{Client: c, Scheme: jobModeScheme()}
	waiting, err := r.syncSubscriptionLogin(context.Background(), a, "agent-worker", &appsv1.Deployment{Spec: appsv1.DeploymentSpec{Template: tpl}}, 0)
	if err != nil || !waiting {
		t.Fatalf("login: %v", err)
	}
	var pod corev1.Pod
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: a.Namespace, Name: subscriptionLoginName(a.Name)}, &pod); err != nil {
		t.Fatal(err)
	}
	if pod.Spec.Containers[0].VolumeMounts[0].MountPath != "/home/agent/.pi/agent" {
		t.Fatal("login uses different auth directory")
	}
	if len(pod.Spec.Containers[0].Env) != 2 {
		t.Fatal("login receives task credentials")
	}
}

func TestPiSubscriptionModelsUseBuiltInProvider(t *testing.T) {
	a := subscriptionAgent()
	a.Spec.Runtime.Engine = "pi"
	a.Spec.LLM.Provider = "openai-codex"
	c := fake.NewClientBuilder().WithScheme(jobModeScheme()).Build()
	r := &AgentReconciler{Client: c, Scheme: jobModeScheme()}
	if err := r.reconcilePiModelsConfigMap(context.Background(), a, "agent-worker"); err != nil {
		t.Fatal(err)
	}
	var cm corev1.ConfigMap
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: a.Namespace, Name: "agent-worker-pi-models"}, &cm); err != nil {
		t.Fatal(err)
	}
	var models struct {
		Providers map[string]interface{} `json:"providers"`
	}
	if err := json.Unmarshal([]byte(cm.Data["models.json"]), &models); err != nil {
		t.Fatal(err)
	}
	if len(models.Providers) != 0 {
		t.Fatal("built-in OAuth provider was overridden")
	}
}

func TestPiSubscriptionRequiresExplicitModel(t *testing.T) {
	a := subscriptionAgent()
	a.Spec.Runtime.Engine = "pi"
	a.Spec.LLM.Provider = "openai-codex"
	a.Spec.LLM.Model = ""
	r := &AgentReconciler{Client: fake.NewClientBuilder().WithScheme(jobModeScheme()).Build()}
	if err := r.validateSubscription(context.Background(), a); err == nil {
		t.Fatal("Pi subscription accepted implicit model selection")
	}
}
