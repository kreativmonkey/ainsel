package controller

import (
	"context"
	"encoding/json"
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ainselv1alpha1 "github.com/ainsel-project/ainsel/shared/api/api/v1alpha1"
)

var _ = Describe("Agent Controller", func() {
	Context("When composing the agent container environment", func() {
		const (
			resourceName  = fixtureAgentName
			testImageName = fixtureImageName
			testImageURL  = fixtureImageURL
		)

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}

		BeforeEach(func() {
			ensureAgentFixtures(ctx)
		})

		AfterEach(func() {
			cleanupAgentFixtures(ctx)
		})

		It("validates external Secret references without requiring a literal value", func() {
			agent := &ainselv1alpha1.Agent{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, agent)).To(Succeed())
			agent.Spec.Env = []ainselv1alpha1.AgentEnvVar{{Name: "GH_TOKEN", ValueFrom: &ainselv1alpha1.AgentEnvVarSource{
				SecretKeyRef: corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "github-token"}, Key: "token"},
			}}}
			Expect(k8sClient.Update(ctx, agent)).To(Succeed())
			agent.Spec.Env[0].Value = "conflicting-literal"
			Expect(k8sClient.Update(ctx, agent)).NotTo(Succeed())
			agent.Spec.Env[0].Value = ""
			agent.Spec.Env[0].ValueFrom.SecretKeyRef.Name = ""
			Expect(k8sClient.Update(ctx, agent)).NotTo(Succeed())
		})

		It("should build MCP_SERVER_TOKENS from AgentImage tokenFromEnv entries", func() {
			By("Updating the AgentImage with an MCP server that has tokenFromEnv")
			img := &ainselv1alpha1.AgentImage{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: testImageName, Namespace: "default"}, img)).To(Succeed())
			img.Spec.Env = []ainselv1alpha1.AgentImageEnvVar{
				{Name: "FORGEJO_PAT", Value: "secret-token-123", Secret: true},
			}
			img.Spec.MCPServers = []ainselv1alpha1.AgentImageMCPServer{
				{Name: "forgejo-mcp-server", URL: "http://forgejo-mcp.workloads.svc.cluster.local:8080/mcp", TokenFromEnv: "FORGEJO_PAT"},
			}
			Expect(k8sClient.Update(ctx, img)).To(Succeed())

			By("Reconciling")
			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the image env Secret contains FORGEJO_PAT")
			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "agent-" + resourceName + "-image-env",
				Namespace: "default",
			}, secret)).To(Succeed())
			Expect(secret.Data).To(HaveKeyWithValue("FORGEJO_PAT", []byte("secret-token-123")))

			By("Verifying the Deployment exposes FORGEJO_PAT via secretKeyRef")
			deploy := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "agent-" + resourceName,
				Namespace: "default",
			}, deploy)).To(Succeed())
			container := deploy.Spec.Template.Spec.Containers[0]
			forgejoPat := findEnvVar(container.Env, "FORGEJO_PAT")
			Expect(forgejoPat).NotTo(BeNil())
			Expect(forgejoPat.ValueFrom).NotTo(BeNil())
			Expect(forgejoPat.ValueFrom.SecretKeyRef).NotTo(BeNil())
			Expect(forgejoPat.ValueFrom.SecretKeyRef.Key).To(Equal("FORGEJO_PAT"))

			By("Verifying MCP_SERVER_TOKENS includes the forgejo-mcp-server entry with $(FORGEJO_PAT)")
			tokensEnv := findEnvVar(container.Env, "MCP_SERVER_TOKENS")
			Expect(tokensEnv).NotTo(BeNil())
			Expect(tokensEnv.Value).To(ContainSubstring("forgejo-mcp-server=$(FORGEJO_PAT)"))
		})

		It("should skip MCP servers whose tokenFromEnv references a missing env var", func() {
			By("Updating the AgentImage with an MCP server whose tokenFromEnv is not defined in env")
			img := &ainselv1alpha1.AgentImage{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: testImageName, Namespace: "default"}, img)).To(Succeed())
			img.Spec.Env = nil
			img.Spec.MCPServers = []ainselv1alpha1.AgentImageMCPServer{
				{Name: "forgejo-mcp-server", URL: "http://x", TokenFromEnv: "MISSING_TOKEN"},
			}
			Expect(k8sClient.Update(ctx, img)).To(Succeed())

			By("Reconciling")
			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying MCP_SERVER_TOKENS is not set (all entries skipped)")
			deploy := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "agent-" + resourceName,
				Namespace: "default",
			}, deploy)).To(Succeed())
			tokensEnv := findEnvVar(deploy.Spec.Template.Spec.Containers[0].Env, "MCP_SERVER_TOKENS")
			Expect(tokensEnv).To(BeNil())
		})

		It("should embed an ainsel.temperature block in models.json when spec.llm.temperature is set", func() {
			By("Setting spec.llm.temperature to 0.3")
			agent := &ainselv1alpha1.Agent{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, agent)).To(Succeed())
			agent.Spec.LLM.Temperature = ptr.To[float64](0.3)
			Expect(k8sClient.Update(ctx, agent)).To(Succeed())

			By("Reconciling")
			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the pi-models ConfigMap embeds ainsel.temperature")
			cm := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "agent-" + resourceName + "-pi-models",
				Namespace: "default",
			}, cm)).To(Succeed())
			modelsJSON := cm.Data["models.json"]
			Expect(modelsJSON).To(ContainSubstring(`"ainsel"`))
			Expect(modelsJSON).To(ContainSubstring(`"temperature": 0.3`))

			By("Verifying models.json is still valid JSON after the injection")
			var parsed struct {
				Providers map[string]any `json:"providers"`
				Ainsel    struct {
					Temperature float64 `json:"temperature"`
				} `json:"ainsel"`
			}
			Expect(json.Unmarshal([]byte(modelsJSON), &parsed)).To(Succeed())
			Expect(parsed.Ainsel.Temperature).To(Equal(0.3))
			Expect(parsed.Providers).NotTo(BeEmpty())

			By("Verifying no PI_TEMPERATURE env var leaks into the pod spec")
			deploy := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "agent-" + resourceName,
				Namespace: "default",
			}, deploy)).To(Succeed())
			Expect(findEnvVar(deploy.Spec.Template.Spec.Containers[0].Env, "PI_TEMPERATURE")).To(BeNil())
		})

		It("should omit the ainsel block from models.json when spec.llm.temperature is unset", func() {
			By("Reconciling without touching spec.llm.temperature")
			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the pi-models ConfigMap has no ainsel block")
			cm := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "agent-" + resourceName + "-pi-models",
				Namespace: "default",
			}, cm)).To(Succeed())
			Expect(cm.Data["models.json"]).NotTo(ContainSubstring(`"ainsel"`))

			By("Verifying models.json is valid JSON")
			var parsed map[string]any
			Expect(json.Unmarshal([]byte(cm.Data["models.json"]), &parsed)).To(Succeed())
		})

		// Pi defaults a missing cost block to all-zero, which makes usage.cost —
		// and every hub-side cost figure derived from it — exactly 0 for the
		// agent. spec.llm.cost must therefore reach models.json verbatim
		// (issue #281).
		It("should embed the cost block in models.json when spec.llm.cost is set", func() {
			By("Setting spec.llm.cost")
			agent := &ainselv1alpha1.Agent{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, agent)).To(Succeed())
			agent.Spec.LLM.Cost = &ainselv1alpha1.AgentLLMCost{
				Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75,
			}
			Expect(k8sClient.Update(ctx, agent)).To(Succeed())

			By("Reconciling")
			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the pi-models ConfigMap carries the cost rates")
			cm := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "agent-" + resourceName + "-pi-models",
				Namespace: "default",
			}, cm)).To(Succeed())
			providers := parsePiModelsJSON(cm.Data["models.json"])
			Expect(providers).NotTo(BeEmpty())
			for _, p := range providers {
				Expect(p.Models).To(HaveLen(1))
				cost := p.Models[0].Cost
				Expect(cost).NotTo(BeNil())
				Expect(cost.Input).To(Equal(3.0))
				Expect(cost.Output).To(Equal(15.0))
				Expect(cost.CacheRead).To(Equal(0.3))
				Expect(cost.CacheWrite).To(Equal(3.75))
			}
		})

		It("should omit the cost block from models.json when spec.llm.cost is unset", func() {
			By("Clearing spec.llm.cost")
			agent := &ainselv1alpha1.Agent{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, agent)).To(Succeed())
			agent.Spec.LLM.Cost = nil
			Expect(k8sClient.Update(ctx, agent)).To(Succeed())

			By("Reconciling")
			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the pi-models ConfigMap has no cost block")
			cm := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "agent-" + resourceName + "-pi-models",
				Namespace: "default",
			}, cm)).To(Succeed())
			Expect(cm.Data["models.json"]).NotTo(ContainSubstring(`"cost"`))
			providers := parsePiModelsJSON(cm.Data["models.json"])
			for _, p := range providers {
				Expect(p.Models[0].Cost).To(BeNil())
			}
		})

		It("should advertise image input in models.json when spec.llm.vision is true", func() {
			By("Setting spec.llm.vision to true")
			agent := &ainselv1alpha1.Agent{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, agent)).To(Succeed())
			agent.Spec.LLM.Vision = ptr.To(true)
			Expect(k8sClient.Update(ctx, agent)).To(Succeed())

			By("Reconciling")
			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the model entry lists text and image input")
			cm := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "agent-" + resourceName + "-pi-models",
				Namespace: "default",
			}, cm)).To(Succeed())
			modelsJSON := cm.Data["models.json"]
			Expect(modelsJSON).To(ContainSubstring(`"input": ["text", "image"]`))

			By("Verifying models.json still parses and the input array holds both media types")
			parsed := parsePiModelsJSON(modelsJSON)
			Expect(parsed).To(HaveLen(1))
			for _, provider := range parsed {
				Expect(provider.Models).To(HaveLen(1))
				Expect(provider.Models[0].Input).To(ConsistOf("text", "image"))
			}
		})

		It("should keep models.json text-only when spec.llm.vision is set back to false", func() {
			By("Setting spec.llm.vision to false")
			agent := &ainselv1alpha1.Agent{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, agent)).To(Succeed())
			agent.Spec.LLM.Vision = ptr.To(false)
			Expect(k8sClient.Update(ctx, agent)).To(Succeed())

			By("Reconciling")
			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the model entry advertises text input only")
			cm := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "agent-" + resourceName + "-pi-models",
				Namespace: "default",
			}, cm)).To(Succeed())
			modelsJSON := cm.Data["models.json"]
			Expect(modelsJSON).To(ContainSubstring(`"input": ["text"]`))
			Expect(modelsJSON).NotTo(ContainSubstring(`"image"`))

			By("Verifying models.json still parses and the input array holds text only")
			parsed := parsePiModelsJSON(modelsJSON)
			Expect(parsed).To(HaveLen(1))
			for _, provider := range parsed {
				Expect(provider.Models).To(HaveLen(1))
				Expect(provider.Models[0].Input).To(ConsistOf("text"))
			}
		})

		It("should default the models.json context window to 131072 for the default provider", func() {
			// The fixture leaves spec.llm unset, so pi would serve the default
			// provider (ollama-cloud). 131072 is issued for every provider now:
			// it is a single biased-low fallback, not a per-provider guess
			// (the old per-provider default handed ollama-cloud 202752, which
			// exceeded the 131072-token ceiling of the default gpt-oss model and
			// therefore silently disabled every compaction guard).
			By("Reconciling the default-provider fixture resource")
			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying models.json declares the uniform 131072 default")
			cm := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "agent-" + resourceName + "-pi-models",
				Namespace: "default",
			}, cm)).To(Succeed())
			modelsJSON := cm.Data["models.json"]
			Expect(modelsJSON).To(ContainSubstring(`"contextWindow": 131072`))

			By("Verifying models.json still parses and carries the window on the model entry")
			parsed := parsePiModelsJSON(modelsJSON)
			Expect(parsed).To(HaveLen(1))
			for _, provider := range parsed {
				Expect(provider.Models).To(HaveLen(1))
				Expect(provider.Models[0].ContextWindow).To(Equal(131072))
			}
		})

		It("should also default the models.json context window to 131072 for custom providers", func() {
			By("Setting the provider to a custom (self-hosted) endpoint")
			agent := &ainselv1alpha1.Agent{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, agent)).To(Succeed())
			agent.Spec.LLM.Provider = ainselv1alpha1.AgentLLMProviderCustom
			agent.Spec.CustomProvider = &ainselv1alpha1.AgentCustomProvider{
				URL: "http://ollama.internal:11434/v1",
			}
			Expect(k8sClient.Update(ctx, agent)).To(Succeed())

			By("Reconciling")
			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying models.json declares the self-hosted context window")
			cm := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "agent-" + resourceName + "-pi-models",
				Namespace: "default",
			}, cm)).To(Succeed())
			modelsJSON := cm.Data["models.json"]
			Expect(modelsJSON).To(ContainSubstring(`"contextWindow": 131072`))

			By("Verifying models.json still parses and carries the window on the model entry")
			parsed := parsePiModelsJSON(modelsJSON)
			Expect(parsed).To(HaveLen(1))
			for _, provider := range parsed {
				Expect(provider.Models).To(HaveLen(1))
				Expect(provider.Models[0].ContextWindow).To(Equal(131072))
			}
		})

		It("should use spec.llm.contextWindow as the models.json context window when set", func() {
			By("Setting spec.llm.contextWindow to 262144")
			agent := &ainselv1alpha1.Agent{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, agent)).To(Succeed())
			agent.Spec.LLM.ContextWindow = ptr.To(262144)
			Expect(k8sClient.Update(ctx, agent)).To(Succeed())

			By("Reconciling")
			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying models.json declares the spec override")
			cm := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "agent-" + resourceName + "-pi-models",
				Namespace: "default",
			}, cm)).To(Succeed())
			modelsJSON := cm.Data["models.json"]
			Expect(modelsJSON).To(ContainSubstring(`"contextWindow": 262144`))

			By("Verifying models.json still parses and carries the window on the model entry")
			parsed := parsePiModelsJSON(modelsJSON)
			Expect(parsed).To(HaveLen(1))
			for _, provider := range parsed {
				Expect(provider.Models).To(HaveLen(1))
				Expect(provider.Models[0].ContextWindow).To(Equal(262144))
			}
		})

		It("should embed provider and model compat blocks in models.json for alibaba-cloud", func() {
			By("Setting the provider to alibaba-cloud")
			agent := &ainselv1alpha1.Agent{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, agent)).To(Succeed())
			agent.Spec.LLM.Provider = ainselv1alpha1.AgentLLMProviderAlibabaCloud
			agent.Spec.LLM.Model = "qwen3.8-max-preview"
			Expect(k8sClient.Update(ctx, agent)).To(Succeed())

			By("Reconciling")
			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the pi-models ConfigMap contains compat fields")
			cm := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "agent-" + resourceName + "-pi-models",
				Namespace: "default",
			}, cm)).To(Succeed())
			modelsJSON := cm.Data["models.json"]

			By("Verifying models.json is valid JSON")
			var parsed struct {
				Providers map[string]struct {
					API     string `json:"api"`
					BaseURL string `json:"baseUrl"`
					Compat  struct {
						SupportsDeveloperRole    bool   `json:"supportsDeveloperRole"`
						SupportsUsageInStreaming bool   `json:"supportsUsageInStreaming"`
						MaxTokensField           string `json:"maxTokensField"`
						SupportsReasoningEffort  bool   `json:"supportsReasoningEffort"`
					} `json:"compat"`
					Models []struct {
						ID               string             `json:"id"`
						Reasoning        bool               `json:"reasoning"`
						ThinkingLevelMap map[string]*string `json:"thinkingLevelMap"`
						Compat           struct {
							ThinkingFormat string `json:"thinkingFormat"`
						} `json:"compat"`
					} `json:"models"`
				} `json:"providers"`
			}
			Expect(json.Unmarshal([]byte(modelsJSON), &parsed)).To(Succeed())

			provider, ok := parsed.Providers["alibaba-cloud"]
			Expect(ok).To(BeTrue(), "expected alibaba-cloud provider in models.json")
			Expect(provider.BaseURL).To(ContainSubstring("aliyuncs.com"))

			By("Verifying provider-level compat")
			Expect(provider.Compat.SupportsDeveloperRole).To(BeFalse())
			Expect(provider.Compat.SupportsUsageInStreaming).To(BeFalse())
			Expect(provider.Compat.MaxTokensField).To(Equal("max_tokens"))
			Expect(provider.Compat.SupportsReasoningEffort).To(BeFalse())

			By("Verifying model-level compat and thinkingLevelMap")
			Expect(provider.Models).To(HaveLen(1))
			model := provider.Models[0]
			Expect(model.ID).To(Equal("qwen3.8-max-preview"))
			Expect(model.Reasoning).To(BeTrue())
			Expect(model.Compat.ThinkingFormat).To(Equal("qwen"))
			Expect(model.ThinkingLevelMap).To(HaveKey("medium"))
			Expect(*model.ThinkingLevelMap["medium"]).To(Equal("medium"))
			Expect(model.ThinkingLevelMap["off"]).To(BeNil())
		})

		It("should create a Secret with image env vars and inject them as explicit env entries", func() {
			By("Updating the AgentImage to include env vars")
			img := &ainselv1alpha1.AgentImage{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: testImageName, Namespace: "default"}, img)).To(Succeed())
			img.Spec.Env = []ainselv1alpha1.AgentImageEnvVar{
				{Name: "CUSTOM_VAR", Value: "custom-value"},
				{Name: "ANOTHER_VAR", Value: "another-value"},
			}
			Expect(k8sClient.Update(ctx, img)).To(Succeed())

			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			agentName := "agent-" + resourceName

			By("Verifying the image env Secret was created")
			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      agentName + "-image-env",
				Namespace: "default",
			}, secret)).To(Succeed())
			Expect(secret.Data).To(HaveKeyWithValue("CUSTOM_VAR", []byte("custom-value")))
			Expect(secret.Data).To(HaveKeyWithValue("ANOTHER_VAR", []byte("another-value")))

			By("Verifying the Deployment exposes each image env via explicit env entries")
			deploy := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      agentName,
				Namespace: "default",
			}, deploy)).To(Succeed())

			container := deploy.Spec.Template.Spec.Containers[0]
			// envFrom is no longer used; explicit env entries enable $(VAR)
			// substitution in MCP_SERVER_TOKENS.
			Expect(container.EnvFrom).To(BeEmpty())

			custom := findEnvVar(container.Env, "CUSTOM_VAR")
			Expect(custom).NotTo(BeNil())
			Expect(custom.ValueFrom).NotTo(BeNil())
			Expect(custom.ValueFrom.SecretKeyRef).NotTo(BeNil())
			Expect(custom.ValueFrom.SecretKeyRef.Name).To(Equal(agentName + "-image-env"))
			Expect(custom.ValueFrom.SecretKeyRef.Key).To(Equal("CUSTOM_VAR"))

			another := findEnvVar(container.Env, "ANOTHER_VAR")
			Expect(another).NotTo(BeNil())
			Expect(another.ValueFrom.SecretKeyRef.Key).To(Equal("ANOTHER_VAR"))
		})

		It("should not allow AgentImage env to override platform-managed auth env vars", func() {
			const platformToken = "platform-internal-token"
			originalToken := os.Getenv("HUB_INTERNAL_VALIDATE_SECRET")
			Expect(os.Setenv("HUB_INTERNAL_VALIDATE_SECRET", platformToken)).To(Succeed())
			DeferCleanup(func() error {
				if originalToken == "" {
					return os.Unsetenv("HUB_INTERNAL_VALIDATE_SECRET")
				}
				return os.Setenv("HUB_INTERNAL_VALIDATE_SECRET", originalToken)
			})

			By("Declaring a mis-set value for the platform-owned env name on the AgentImage")
			img := &ainselv1alpha1.AgentImage{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: testImageName, Namespace: "default"}, img)).To(Succeed())
			img.Spec.Env = []ainselv1alpha1.AgentImageEnvVar{
				{Name: "CUSTOM_VAR", Value: "custom-value"},
				{Name: "HUB_INTERNAL_VALIDATE_SECRET", Value: "wrong-token"},
			}
			Expect(k8sClient.Update(ctx, img)).To(Succeed())

			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			agentName := "agent-" + resourceName

			By("Verifying the image env Secret carries the canonical platform values")
			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      agentName + "-image-env",
				Namespace: "default",
			}, secret)).To(Succeed())
			Expect(secret.Data).To(HaveKeyWithValue("CUSTOM_VAR", []byte("custom-value")))
			Expect(secret.Data).To(HaveKeyWithValue("HUB_INTERNAL_VALIDATE_SECRET", []byte(platformToken)))

			By("Verifying the agent container gets exactly one platform-owned entry each, as literals")
			deploy := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      agentName,
				Namespace: "default",
			}, deploy)).To(Succeed())

			container := deploy.Spec.Template.Spec.Containers[0]
			var internalToken *corev1.EnvVar
			for i := range container.Env {
				if container.Env[i].Name == "HUB_INTERNAL_VALIDATE_SECRET" {
					Expect(internalToken).To(BeNil(), "expected exactly one HUB_INTERNAL_VALIDATE_SECRET entry")
					internalToken = &container.Env[i]
				}
			}
			Expect(internalToken).NotTo(BeNil())
			Expect(internalToken.Value).To(Equal(platformToken))
			Expect(internalToken.ValueFrom).To(BeNil())

			By("Verifying non-reserved image env vars still flow through")
			custom := findEnvVar(container.Env, "CUSTOM_VAR")
			Expect(custom).NotTo(BeNil())
			Expect(custom.ValueFrom.SecretKeyRef.Key).To(Equal("CUSTOM_VAR"))
		})

		It("should delete the image env Secret when the AgentImage has no env vars", func() {
			agentName := "agent-" + resourceName

			By("Cleaning up any pre-existing image env Secret")
			_ = k8sClient.Delete(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      agentName + "-image-env",
					Namespace: "default",
				},
			})

			By("Pre-creating a leftover image env Secret owned by the Agent")
			agent := &ainselv1alpha1.Agent{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, agent)).To(Succeed())
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      agentName + "-image-env",
					Namespace: "default",
				},
				StringData: map[string]string{"OLD_VAR": "old-value"},
			}
			Expect(controllerutil.SetControllerReference(agent, secret, k8sClient.Scheme())).To(Succeed())
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())

			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the image env Secret was deleted")
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      agentName + "-image-env",
				Namespace: "default",
			}, &corev1.Secret{})
			Expect(errors.IsNotFound(err)).To(BeTrue())
		})

		It("should not delete an image env Secret not owned by the Agent", func() {
			agentName := "agent-" + resourceName

			By("Cleaning up any pre-existing image env Secret")
			_ = k8sClient.Delete(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      agentName + "-image-env",
					Namespace: "default",
				},
			})

			By("Pre-creating an image env Secret without owner references")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      agentName + "-image-env",
					Namespace: "default",
				},
				StringData: map[string]string{"USER_VAR": "user-value"},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())

			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the image env Secret was NOT deleted")
			remaining := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      agentName + "-image-env",
				Namespace: "default",
			}, remaining)).To(Succeed())
			Expect(remaining.Data).To(HaveKeyWithValue("USER_VAR", []byte("user-value")))
		})

		It("should reference the secret named in APIKeySecretRef for Ollama Cloud", func() {
			By("Updating the Agent to set a custom OllamaCloud APIKeySecretRef")
			agent := &ainselv1alpha1.Agent{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, agent)).To(Succeed())
			agent.Spec.OllamaCloud = &ainselv1alpha1.AgentOllamaCloud{
				APIKeySecretRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "custom-ollama-secret"},
					Key:                  "api-key",
				},
			}
			Expect(k8sClient.Update(ctx, agent)).To(Succeed())

			By("Reconciling")
			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the Deployment references the custom secret")
			deploy := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "agent-" + resourceName,
				Namespace: "default",
			}, deploy)).To(Succeed())
			ollamaEnv := findEnvVar(deploy.Spec.Template.Spec.Containers[0].Env, "OLLAMA_API_KEY")
			Expect(ollamaEnv).NotTo(BeNil())
			Expect(ollamaEnv.ValueFrom).NotTo(BeNil())
			Expect(ollamaEnv.ValueFrom.SecretKeyRef).NotTo(BeNil())
			Expect(ollamaEnv.ValueFrom.SecretKeyRef.Name).To(Equal("custom-ollama-secret"))
		})

		It("should reference the secret named in APIKeySecretRef for OpenCode", func() {
			By("Updating the Agent to set a custom OpenCode APIKeySecretRef")
			agent := &ainselv1alpha1.Agent{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, agent)).To(Succeed())
			agent.Spec.OpenCode = &ainselv1alpha1.AgentOpenCode{
				APIKeySecretRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "custom-opencode-secret"},
					Key:                  "api-key",
				},
			}
			Expect(k8sClient.Update(ctx, agent)).To(Succeed())

			By("Reconciling")
			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the Deployment references the custom secret")
			deploy := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "agent-" + resourceName,
				Namespace: "default",
			}, deploy)).To(Succeed())
			opencodeEnv := findEnvVar(deploy.Spec.Template.Spec.Containers[0].Env, "OPENCODE_API_KEY")
			Expect(opencodeEnv).NotTo(BeNil())
			Expect(opencodeEnv.ValueFrom).NotTo(BeNil())
			Expect(opencodeEnv.ValueFrom.SecretKeyRef).NotTo(BeNil())
			Expect(opencodeEnv.ValueFrom.SecretKeyRef.Name).To(Equal("custom-opencode-secret"))
		})

		It("should reference the secret named in APIKeySecretRef for Alibaba Cloud", func() {
			By("Updating the Agent to set a custom AlibabaCloud APIKeySecretRef")
			agent := &ainselv1alpha1.Agent{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, agent)).To(Succeed())
			agent.Spec.AlibabaCloud = &ainselv1alpha1.AgentAlibabaCloud{
				APIKeySecretRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "custom-alibaba-secret"},
					Key:                  "api-key",
				},
			}
			Expect(k8sClient.Update(ctx, agent)).To(Succeed())

			By("Reconciling")
			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the Deployment references the custom secret")
			deploy := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "agent-" + resourceName,
				Namespace: "default",
			}, deploy)).To(Succeed())
			alibabaEnv := findEnvVar(deploy.Spec.Template.Spec.Containers[0].Env, "ALIBABA_CLOUD_API_KEY")
			Expect(alibabaEnv).NotTo(BeNil())
			Expect(alibabaEnv.ValueFrom).NotTo(BeNil())
			Expect(alibabaEnv.ValueFrom.SecretKeyRef).NotTo(BeNil())
			Expect(alibabaEnv.ValueFrom.SecretKeyRef.Name).To(Equal("custom-alibaba-secret"))
		})

		It("should wire agent-scoped MCP servers when spec.mcp is set", func() {
			By("Enabling an MCP server on the image, then overriding on the agent")
			img := &ainselv1alpha1.AgentImage{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: testImageName, Namespace: "default"}, img)).To(Succeed())
			img.Spec.MCPServers = []ainselv1alpha1.AgentImageMCPServer{
				{Name: "shared-mcp", URL: "http://shared.example/mcp"},
			}
			img.Spec.Env = []ainselv1alpha1.AgentImageEnvVar{{Name: "AGENT_MCP_TOKEN"}}
			Expect(k8sClient.Update(ctx, img)).To(Succeed())

			Expect(updateAgentWithRetry(ctx, typeNamespacedName, func(a *ainselv1alpha1.Agent) {
				a.Spec.MCP = &ainselv1alpha1.AgentMCP{Servers: []ainselv1alpha1.AgentMCPServer{
					{Name: "agent-mcp", URL: "http://agent.example/mcp", TokenFromEnv: "AGENT_MCP_TOKEN"}, // #nosec G101 -- test fixture: env var name, not a credential
				}}
			})).To(Succeed())

			By("Reconciling and verifying the deployment env")
			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			deploy := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "agent-" + resourceName,
				Namespace: "default",
			}, deploy)).To(Succeed())
			mainEnv := deploy.Spec.Template.Spec.Containers[0].Env
			mcpServers := findEnvVar(mainEnv, "MCP_SERVERS")
			Expect(mcpServers).NotTo(BeNil())
			Expect(mcpServers.Value).To(ContainSubstring("agent-mcp=http://agent.example/mcp"))
			Expect(mcpServers.Value).NotTo(
				ContainSubstring("shared-mcp"), "agent definitions replace the image's servers, they do not merge",
			)
			tokens := findEnvVar(mainEnv, "MCP_SERVER_TOKENS")
			Expect(tokens).NotTo(BeNil())
			Expect(tokens.Value).To(ContainSubstring("agent-mcp=$(AGENT_MCP_TOKEN)"))
		})

		It("should wire no MCP servers when spec.mcp is explicitly empty", func() {
			By("Enabling an MCP server on the image, then selecting none on the agent")
			img := &ainselv1alpha1.AgentImage{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: testImageName, Namespace: "default"}, img)).To(Succeed())
			img.Spec.MCPServers = []ainselv1alpha1.AgentImageMCPServer{
				{Name: "shared-mcp", URL: "http://shared.example/mcp"},
			}
			Expect(k8sClient.Update(ctx, img)).To(Succeed())

			Expect(updateAgentWithRetry(ctx, typeNamespacedName, func(a *ainselv1alpha1.Agent) {
				a.Spec.MCP = &ainselv1alpha1.AgentMCP{}
			})).To(Succeed())

			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			deploy := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "agent-" + resourceName,
				Namespace: "default",
			}, deploy)).To(Succeed())
			mcpServers := findEnvVar(deploy.Spec.Template.Spec.Containers[0].Env, "MCP_SERVERS")
			Expect(mcpServers).NotTo(BeNil())
			Expect(mcpServers.Value).To(BeEmpty(), "explicit empty selection wires no servers")
		})

		It("should apply agent env overrides on top of the image env", func() {
			By("Declaring shared defaults on the image, an override and an addition on the agent")
			img := &ainselv1alpha1.AgentImage{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: testImageName, Namespace: "default"}, img)).To(Succeed())
			img.Spec.Env = []ainselv1alpha1.AgentImageEnvVar{
				{Name: "LOG_LEVEL", Value: "info"},
				{Name: "SHARED_ONLY", Value: "yes"},
			}
			Expect(k8sClient.Update(ctx, img)).To(Succeed())

			Expect(updateAgentWithRetry(ctx, typeNamespacedName, func(a *ainselv1alpha1.Agent) {
				a.Spec.Env = []ainselv1alpha1.AgentEnvVar{
					{Name: "LOG_LEVEL", Value: "debug"},
					{Name: "AGENT_ONLY", Value: "agent-value"},
				}
			})).To(Succeed())

			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			agentName := "agent-" + resourceName

			By("Verifying the env Secret merges image defaults with the agent's overrides")
			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      agentName + "-image-env",
				Namespace: "default",
			}, secret)).To(Succeed())
			Expect(secret.Data).To(HaveKeyWithValue("LOG_LEVEL", []byte("debug")),
				"the agent's value must win over the image default")
			Expect(secret.Data).To(HaveKeyWithValue("SHARED_ONLY", []byte("yes")),
				"image defaults the agent does not override must survive")
			Expect(secret.Data).To(HaveKeyWithValue("AGENT_ONLY", []byte("agent-value")),
				"agent-only variables must be added")

			By("Verifying the Deployment exposes every effective variable")
			deploy := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      agentName,
				Namespace: "default",
			}, deploy)).To(Succeed())
			for _, name := range []string{"LOG_LEVEL", "SHARED_ONLY", "AGENT_ONLY"} {
				e := findEnvVar(deploy.Spec.Template.Spec.Containers[0].Env, name)
				Expect(e).NotTo(BeNil(), "missing env entry %s", name)
				Expect(e.ValueFrom).NotTo(BeNil())
				Expect(e.ValueFrom.SecretKeyRef.Name).To(Equal(agentName + "-image-env"))
				Expect(e.ValueFrom.SecretKeyRef.Key).To(Equal(name))
			}
		})

		It("should create the env Secret when only the agent defines env vars", func() {
			By("Clearing the image env so the agent's own vars are the only source")
			img := &ainselv1alpha1.AgentImage{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: testImageName, Namespace: "default"}, img)).To(Succeed())
			img.Spec.Env = nil
			Expect(k8sClient.Update(ctx, img)).To(Succeed())

			Expect(updateAgentWithRetry(ctx, typeNamespacedName, func(a *ainselv1alpha1.Agent) {
				a.Spec.Env = []ainselv1alpha1.AgentEnvVar{
					{Name: "AGENT_ONLY", Value: "agent-value"},
				}
			})).To(Succeed())

			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			agentName := "agent-" + resourceName
			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      agentName + "-image-env",
				Namespace: "default",
			}, secret)).To(Succeed(), "agent env alone must still produce the Secret")
			Expect(secret.Data).To(HaveKeyWithValue("AGENT_ONLY", []byte("agent-value")))
			Expect(findEnvVar(
				mustDeployment(agentName).Spec.Template.Spec.Containers[0].Env,
				"AGENT_ONLY",
			)).NotTo(BeNil())
		})

		It("should not allow agent env to override platform-managed auth env vars", func() {
			const platformToken = "platform-internal-token"
			originalToken := os.Getenv("HUB_INTERNAL_VALIDATE_SECRET")
			Expect(os.Setenv("HUB_INTERNAL_VALIDATE_SECRET", platformToken)).To(Succeed())
			DeferCleanup(func() error {
				if originalToken == "" {
					return os.Unsetenv("HUB_INTERNAL_VALIDATE_SECRET")
				}
				return os.Setenv("HUB_INTERNAL_VALIDATE_SECRET", originalToken)
			})

			By("Declaring a mis-set value for the platform-owned name as an agent override")
			Expect(updateAgentWithRetry(ctx, typeNamespacedName, func(a *ainselv1alpha1.Agent) {
				a.Spec.Env = []ainselv1alpha1.AgentEnvVar{
					{Name: "HUB_INTERNAL_VALIDATE_SECRET", Value: "wrong-token"}, // #nosec G101 -- test fixture: asserts the value is NOT used
				}
			})).To(Succeed())

			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			agentName := "agent-" + resourceName
			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      agentName + "-image-env",
				Namespace: "default",
			}, secret)).To(Succeed())
			Expect(secret.Data).To(HaveKeyWithValue("HUB_INTERNAL_VALIDATE_SECRET", []byte(platformToken)),
				"the canonical platform value must win over an agent override")

			By("Verifying the agent container gets exactly one platform-owned entry, as a literal")
			container := mustDeployment(agentName).Spec.Template.Spec.Containers[0]
			var internalToken *corev1.EnvVar
			for i := range container.Env {
				if container.Env[i].Name == "HUB_INTERNAL_VALIDATE_SECRET" {
					Expect(internalToken).To(BeNil(), "expected exactly one HUB_INTERNAL_VALIDATE_SECRET entry")
					internalToken = &container.Env[i]
				}
			}
			Expect(internalToken).NotTo(BeNil())
			Expect(internalToken.Value).To(Equal(platformToken))
			Expect(internalToken.ValueFrom).To(BeNil(), "platform-owned values are injected as literals")
		})
	})
})

// Issue #96: resolvePiProvider is the operator's single source of truth for
// provider selection. It must resolve the four valid providers exactly, honour
// per-provider baseUrl overrides, and refuse (never default) on an empty or
// unknown spec.llm.provider.
var _ = Describe("resolvePiProvider", func() {
	newAgent := func(mut func(*ainselv1alpha1.Agent)) *ainselv1alpha1.Agent {
		a := &ainselv1alpha1.Agent{}
		a.Name = "provider-test"
		a.Spec.LLM.Model = "glm-5.1:cloud"
		mut(a)
		return a
	}

	It("resolves ollama-cloud to its canonical name, URL and API-key env", func() {
		agent := newAgent(func(a *ainselv1alpha1.Agent) {
			a.Spec.LLM.Provider = ainselv1alpha1.AgentLLMProviderOllamaCloud
		})
		pp, err := resolvePiProvider(agent)
		Expect(err).NotTo(HaveOccurred())
		Expect(pp.Name).To(Equal("ollama-api-key"))
		Expect(pp.BaseURL).To(Equal("https://ollama.com/v1"))
		Expect(pp.APIKeyEnv).To(Equal("OLLAMA_API_KEY"))
	})

	It("resolves opencode to its canonical name, URL and API-key env", func() {
		agent := newAgent(func(a *ainselv1alpha1.Agent) {
			a.Spec.LLM.Provider = ainselv1alpha1.AgentLLMProviderOpenCode
		})
		pp, err := resolvePiProvider(agent)
		Expect(err).NotTo(HaveOccurred())
		Expect(pp.Name).To(Equal("opencode"))
		Expect(pp.BaseURL).To(Equal("https://opencode.ai/zen/go/v1"))
		Expect(pp.APIKeyEnv).To(Equal("OPENCODE_API_KEY"))
	})

	It("resolves alibaba-cloud to its canonical name, URL and API-key env", func() {
		agent := newAgent(func(a *ainselv1alpha1.Agent) {
			a.Spec.LLM.Provider = ainselv1alpha1.AgentLLMProviderAlibabaCloud
		})
		pp, err := resolvePiProvider(agent)
		Expect(err).NotTo(HaveOccurred())
		Expect(pp.Name).To(Equal("alibaba-cloud"))
		Expect(pp.BaseURL).To(Equal("https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1"))
		Expect(pp.APIKeyEnv).To(Equal("ALIBABA_CLOUD_API_KEY"))
	})

	It("resolves custom to the openai key with the spec.customProvider.url", func() {
		agent := newAgent(func(a *ainselv1alpha1.Agent) {
			a.Spec.LLM.Provider = ainselv1alpha1.AgentLLMProviderCustom
			a.Spec.CustomProvider = &ainselv1alpha1.AgentCustomProvider{URL: "https://llm.internal.example/v1"}
		})
		pp, err := resolvePiProvider(agent)
		Expect(err).NotTo(HaveOccurred())
		Expect(pp.Name).To(Equal("openai"))
		Expect(pp.BaseURL).To(Equal("https://llm.internal.example/v1"))
		Expect(pp.APIKeyEnv).To(Equal("CUSTOM_PROVIDER_API_KEY"))
	})

	It("lets a baseUrl override win over the built-in URL for each built-in provider", func() {
		agent := newAgent(func(a *ainselv1alpha1.Agent) {
			a.Spec.LLM.Provider = ainselv1alpha1.AgentLLMProviderOllamaCloud
			a.Spec.OllamaCloud = &ainselv1alpha1.AgentOllamaCloud{BaseURL: "https://ollama.internal.example/v1"}
		})
		pp, err := resolvePiProvider(agent)
		Expect(err).NotTo(HaveOccurred())
		Expect(pp.BaseURL).To(Equal("https://ollama.internal.example/v1"))

		agent = newAgent(func(a *ainselv1alpha1.Agent) {
			a.Spec.LLM.Provider = ainselv1alpha1.AgentLLMProviderOpenCode
			a.Spec.OpenCode = &ainselv1alpha1.AgentOpenCode{BaseURL: "https://zen.internal.example/v1"}
		})
		pp, err = resolvePiProvider(agent)
		Expect(err).NotTo(HaveOccurred())
		Expect(pp.BaseURL).To(Equal("https://zen.internal.example/v1"))

		// The Alibaba case is the one the issue calls out: a different region.
		agent = newAgent(func(a *ainselv1alpha1.Agent) {
			a.Spec.LLM.Provider = ainselv1alpha1.AgentLLMProviderAlibabaCloud
			a.Spec.AlibabaCloud = &ainselv1alpha1.AgentAlibabaCloud{BaseURL: "https://token-plan.eu-central-1.maas.aliyuncs.com/compatible-mode/v1"}
		})
		pp, err = resolvePiProvider(agent)
		Expect(err).NotTo(HaveOccurred())
		Expect(pp.BaseURL).To(Equal("https://token-plan.eu-central-1.maas.aliyuncs.com/compatible-mode/v1"))
	})

	It("errors on an unset provider instead of defaulting to ollama-cloud", func() {
		agent := newAgent(func(a *ainselv1alpha1.Agent) {})
		_, err := resolvePiProvider(agent)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("spec.llm.provider is required"))
		Expect(err.Error()).To(ContainSubstring("ollama-cloud, opencode, alibaba-cloud, custom"))
	})

	It("errors on an unknown provider instead of defaulting to ollama-cloud", func() {
		agent := newAgent(func(a *ainselv1alpha1.Agent) {
			a.Spec.LLM.Provider = "ollama"
		})
		_, err := resolvePiProvider(agent)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("unknown"))
		Expect(err.Error()).To(ContainSubstring("ollama-cloud, opencode, alibaba-cloud, custom"))
	})
})
