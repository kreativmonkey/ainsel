package controller

import (
	"context"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ainselv1alpha1 "github.com/ainsel-project/ainsel/shared/api/api/v1alpha1"
)

// envNames flattens an env list to "name=value" pairs so assertions read as
// the merge result rather than as struct comparisons.
func envPairs(in []ainselv1alpha1.AgentEnvVar) []string {
	out := make([]string, 0, len(in))
	for _, e := range in {
		out = append(out, e.Name+"="+e.Value)
	}
	return out
}

func TestEffectiveEnv(t *testing.T) {
	image := &ainselv1alpha1.AgentImage{
		Spec: ainselv1alpha1.AgentImageSpec{
			Env: []ainselv1alpha1.AgentImageEnvVar{
				{Name: "LOG_LEVEL", Value: "info"},
				{Name: "SHARED_ONLY", Value: "yes"},
			},
		},
	}
	agentWith := func(env ...ainselv1alpha1.AgentEnvVar) *ainselv1alpha1.Agent {
		return &ainselv1alpha1.Agent{Spec: ainselv1alpha1.AgentSpec{Env: env}}
	}

	t.Run("no overrides returns the image env unchanged", func(t *testing.T) {
		got := effectiveEnv(&ainselv1alpha1.Agent{}, image)
		if !slices.Equal(envPairs(got), []string{"LOG_LEVEL=info", "SHARED_ONLY=yes"}) {
			t.Errorf("got %v", envPairs(got))
		}
	})

	t.Run("matching name overrides the image value in place", func(t *testing.T) {
		got := effectiveEnv(agentWith(
			ainselv1alpha1.AgentEnvVar{Name: "LOG_LEVEL", Value: "debug"},
		), image)
		// Order follows the image so the rendered Secret stays stable.
		if !slices.Equal(envPairs(got), []string{"LOG_LEVEL=debug", "SHARED_ONLY=yes"}) {
			t.Errorf("got %v", envPairs(got))
		}
	})

	t.Run("agent-only names are appended in request order", func(t *testing.T) {
		got := effectiveEnv(agentWith(
			ainselv1alpha1.AgentEnvVar{Name: "AGENT_B", Value: "b"},
			ainselv1alpha1.AgentEnvVar{Name: "AGENT_A", Value: "a"},
		), image)
		if !slices.Equal(envPairs(got), []string{
			"LOG_LEVEL=info", "SHARED_ONLY=yes", "AGENT_B=b", "AGENT_A=a",
		}) {
			t.Errorf("got %v", envPairs(got))
		}
	})

	t.Run("override and addition combine", func(t *testing.T) {
		got := effectiveEnv(agentWith(
			ainselv1alpha1.AgentEnvVar{Name: "SHARED_ONLY", Value: "no"},
			ainselv1alpha1.AgentEnvVar{Name: "AGENT_ONLY", Value: "1"},
		), image)
		if !slices.Equal(envPairs(got), []string{
			"LOG_LEVEL=info", "SHARED_ONLY=no", "AGENT_ONLY=1",
		}) {
			t.Errorf("got %v", envPairs(got))
		}
	})

	t.Run("duplicate agent names keep the last value once", func(t *testing.T) {
		got := effectiveEnv(agentWith(
			ainselv1alpha1.AgentEnvVar{Name: "LOG_LEVEL", Value: "warn"},
			ainselv1alpha1.AgentEnvVar{Name: "LOG_LEVEL", Value: "debug"},
		), image)
		if !slices.Equal(envPairs(got), []string{"LOG_LEVEL=debug", "SHARED_ONLY=yes"}) {
			t.Errorf("got %v", envPairs(got))
		}
	})

	t.Run("the agent entry supplies the secret flag", func(t *testing.T) {
		got := effectiveEnv(agentWith(
			ainselv1alpha1.AgentEnvVar{Name: "LOG_LEVEL", Value: "s3cret", Secret: true},
		), image)
		if len(got) != 2 || !got[0].Secret {
			t.Fatalf("expected the override to carry Secret=true, got %+v", got)
		}
	})

	t.Run("agent env alone works with an image that defines none", func(t *testing.T) {
		empty := &ainselv1alpha1.AgentImage{}
		got := effectiveEnv(agentWith(
			ainselv1alpha1.AgentEnvVar{Name: "AGENT_ONLY", Value: "1"},
		), empty)
		if !slices.Equal(envPairs(got), []string{"AGENT_ONLY=1"}) {
			t.Errorf("got %v", envPairs(got))
		}
	})

	t.Run("neither side defining env yields none", func(t *testing.T) {
		if got := effectiveEnv(&ainselv1alpha1.Agent{}, &ainselv1alpha1.AgentImage{}); len(got) != 0 {
			t.Errorf("got %v, want empty", envPairs(got))
		}
	})
}

func TestAgentSecretKeyReference(t *testing.T) {
	t.Setenv("HUB_INTERNAL_VALIDATE_SECRET", "platform-token")
	agent := jobModeAgent(5, 1, 2)
	source := &ainselv1alpha1.AgentEnvVarSource{SecretKeyRef: corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: "github-token"}, Key: "token",
	}}
	agent.Spec.Env = []ainselv1alpha1.AgentEnvVar{{Name: "GH_TOKEN", ValueFrom: source}, {Name: "HUB_INTERNAL_VALIDATE_SECRET", ValueFrom: source}}
	agent.Spec.MCP = &ainselv1alpha1.AgentMCP{Servers: []ainselv1alpha1.AgentMCPServer{{Name: "github", URL: "http://github", TokenFromEnv: "GH_TOKEN"}}}
	image := &ainselv1alpha1.AgentImage{Spec: ainselv1alpha1.AgentImageSpec{Env: []ainselv1alpha1.AgentImageEnvVar{{Name: "GH_TOKEN", Value: "old-token"}}}}
	r := &AgentReconciler{Client: fake.NewClientBuilder().WithScheme(jobModeScheme()).Build(), Scheme: jobModeScheme()}
	if err := r.reconcileImageEnvSecret(context.Background(), agent, "agent-test", image); err != nil {
		t.Fatal(err)
	}
	secret := &corev1.Secret{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: agent.Namespace, Name: "agent-test-image-env"}, secret); err != nil {
		t.Fatal(err)
	}
	if _, ok := secret.Data["GH_TOKEN"]; ok {
		t.Fatal("external secret reference copied into managed secret")
	}
	plan := newPodPlan(agent, "agent-test", image)
	env, err := buildAgentEnv(context.Background(), agent, "agent-test", image, plan)
	if err != nil {
		t.Fatal(err)
	}
	got := findEnvVar(env, "GH_TOKEN")
	if got == nil || got.ValueFrom == nil || got.ValueFrom.SecretKeyRef.Name != "github-token" || got.ValueFrom.SecretKeyRef.Key != "token" {
		t.Fatalf("unexpected GH_TOKEN reference: %+v", got)
	}
	if hub := findEnvVar(env, "HUB_INTERNAL_VALIDATE_SECRET"); hub == nil || hub.ValueFrom != nil {
		t.Fatal("reference overrode platform-owned environment")
	}
	if len(plan.missingEnv) != 0 {
		t.Fatal("external secret reference unavailable to MCP token substitution")
	}
}
