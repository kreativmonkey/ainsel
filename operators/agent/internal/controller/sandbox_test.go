package controller

import (
	"context"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/types"
	"testing"

	api "github.com/ainsel-project/ainsel/shared/api/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCodexSandboxProfile(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		a := subscriptionAgent()
		if enabled {
			a.Spec.Runtime.SandboxProfile = "codex"
		}
		img := &api.AgentImage{}
		plan := newPodPlan(a, "worker", img)
		configureSubscriptionPlan(a, plan)
		tpl := (&AgentReconciler{}).renderPodTemplate(a, img, "native:1", plan, nil)
		deploy := &appsv1.Deployment{Spec: appsv1.DeploymentSpec{Template: tpl}}
		job := buildTaskJob(a, "worker", deploy, 42)
		checkSandboxPod(t, &job.Spec.Template.Spec, enabled)
		a.Spec.Runtime.Subscription.Login = true
		scheme := jobModeScheme()
		c := fake.NewClientBuilder().WithScheme(scheme).Build()
		r := &AgentReconciler{Client: c, Scheme: scheme}
		if _, err := r.syncSubscriptionLogin(context.Background(), a, "worker", deploy, 0); err != nil {
			t.Fatal(err)
		}
		var pods corev1.PodList
		if err := c.List(context.Background(), &pods); err != nil {
			t.Fatal(err)
		}
		if len(pods.Items) != 1 {
			t.Fatalf("got %d login pods", len(pods.Items))
		}
		checkSandboxPod(t, &pods.Items[0].Spec, enabled)
	}
}

func checkSandboxPod(t *testing.T, spec *corev1.PodSpec, enabled bool) {
	t.Helper()
	sec := spec.SecurityContext
	if !*sec.RunAsNonRoot || *sec.RunAsUser != 1000 {
		t.Fatal("lost non-root hardening")
	}
	c := spec.Containers[0].SecurityContext
	if *c.AllowPrivilegeEscalation || !*c.ReadOnlyRootFilesystem || len(c.Capabilities.Drop) != 1 || c.Capabilities.Drop[0] != "ALL" {
		t.Fatal("lost container hardening")
	}
	if spec.AutomountServiceAccountToken == nil || *spec.AutomountServiceAccountToken {
		t.Fatal("service account token mounted")
	}
	if !enabled {
		if len(spec.NodeSelector) != 0 || len(spec.Tolerations) != 0 || sec.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
			t.Fatal("default scheduling or seccomp changed")
		}
		return
	}
	if len(spec.NodeSelector) != 1 || spec.NodeSelector["ainsel.dev.node-restriction.kubernetes.io/codex-sandbox"] != "true" {
		t.Fatal("missing protected worker selection")
	}
	if len(spec.Tolerations) != 1 || spec.Tolerations[0] != (corev1.Toleration{Key: "ainsel.dev/sandbox", Operator: corev1.TolerationOpEqual, Value: "codex", Effect: corev1.TaintEffectNoSchedule}) {
		t.Fatal("unexpected toleration")
	}
	if sec.SeccompProfile.Type != corev1.SeccompProfileTypeLocalhost || sec.SeccompProfile.LocalhostProfile == nil || *sec.SeccompProfile.LocalhostProfile != "profiles/ainsel-codex.json" {
		t.Fatal("wrong sandbox seccomp profile")
	}
}

func TestSandboxProfileRejectsUnsafeRuntime(t *testing.T) {
	for _, tc := range []struct {
		profile, engine string
		hardening       *bool
	}{
		{"unconfined", "codex", nil}, {"codex", "pi", nil}, {"codex", "claude", nil}, {"codex", "codex", ptr.To(false)},
	} {
		a := subscriptionAgent()
		a.Spec.Runtime.SandboxProfile, a.Spec.Runtime.Engine, a.Spec.Runtime.SecurityHardened = tc.profile, tc.engine, tc.hardening
		if err := (&AgentReconciler{}).validateSubscription(context.Background(), a); err == nil {
			t.Fatalf("accepted unsafe runtime: %+v", tc)
		}
	}
}

var _ = Describe("Sandbox profile admission", func() {
	It("requires codex engine and hardening", func() {
		ctx := context.Background()
		ensureAgentFixtures(ctx)
		defer cleanupAgentFixtures(ctx)
		a := &api.Agent{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: fixtureAgentName, Namespace: "default"}, a)).To(Succeed())
		a.Spec.Runtime.SandboxProfile = "codex"
		Expect(k8sClient.Update(ctx, a)).NotTo(Succeed())
		a.Spec.Runtime.Engine = "codex"
		a.Spec.Runtime.SecurityHardened = ptr.To(false)
		Expect(k8sClient.Update(ctx, a)).NotTo(Succeed())
		a.Spec.Runtime.SecurityHardened = ptr.To(true)
		Expect(k8sClient.Update(ctx, a)).To(Succeed())
	})
})
