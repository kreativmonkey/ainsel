package controller

import (
	"context"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ainselv1alpha1 "github.com/ainsel-project/ainsel/shared/api/api/v1alpha1"
)

// receiveEventWith drains a fake recorder's events until one contains all of
// want. Unrelated events can be queued ahead of the one under test — the
// digest-resolution Warning fires for every reconcile whose image is
// unreachable — so a single Receive would race.
func receiveEventWith(events <-chan string, want ...string) bool {
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				// Closed channel: further receives return immediately, which
				// would spin instead of timing out.
				return false
			}
			match := true
			for _, w := range want {
				if !strings.Contains(ev, w) {
					match = false
					break
				}
			}
			if match {
				return true
			}
		case <-time.After(2 * time.Second):
			return false
		}
	}
}

var _ = Describe("Agent Controller", func() {
	Context("When reconciling a resource", func() {
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

		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			agentName := "agent-" + resourceName

			By("Verifying the operator does NOT create the legacy per-agent persona ConfigMap")
			// The hub now owns persona ConfigMaps (rendered as "persona-<id>").
			// The operator no longer renders <agent>-persona.
			cm := &corev1.ConfigMap{}
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      agentName + "-persona",
				Namespace: "default",
			}, cm)
			Expect(errors.IsNotFound(err)).To(BeTrue(),
				"operator must not render the legacy <agent>-persona ConfigMap; the hub owns persona-<id>")

			By("Verifying the Deployment was created")
			deploy := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      agentName,
				Namespace: "default",
			}, deploy)).To(Succeed())
			Expect(deploy.Spec.Template.Spec.Containers).To(HaveLen(1))
			Expect(deploy.Spec.Template.Spec.Containers[0].Name).To(Equal("agent"))

			By("Verifying the container image is sourced from the AgentImage CR")
			Expect(deploy.Spec.Template.Spec.Containers[0].Image).To(Equal(testImageURL))

			By("Verifying AGENT_TOOLS is set to the container-kind EnabledTools only")
			envs := deploy.Spec.Template.Spec.Containers[0].Env
			agentToolsEnv := findEnvVar(envs, "AGENT_TOOLS")
			Expect(agentToolsEnv).NotTo(BeNil())
			// Only container-kind tools are surfaced; shell kinds (e.g. "bash")
			// are dropped — they ran inside the runtime via Bash patterns in the
			// claude-cli days, which is no longer relevant now the agent is pi-only.
			Expect(agentToolsEnv.Value).To(Equal("git"))
			By("Verifying CLAUDE_CLI_ALLOWED_TOOLS is no longer emitted")
			Expect(findEnvVar(envs, "CLAUDE_CLI_ALLOWED_TOOLS")).To(BeNil())
			By("Verifying AGENT_PROVIDER is no longer emitted")
			Expect(findEnvVar(envs, "AGENT_PROVIDER")).To(BeNil())

			By("Verifying the metrics Service was created")
			svc := &corev1.Service{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      agentName + "-metrics",
				Namespace: "default",
			}, svc)).To(Succeed())

			By("Verifying the Agent status has sub-conditions set")
			updatedAgent := &ainselv1alpha1.Agent{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updatedAgent)).To(Succeed())
			Expect(updatedAgent.Status.Conditions).NotTo(BeEmpty())

			findCondition := func(conditions []metav1.Condition, condType string) *metav1.Condition {
				for i := range conditions {
					if conditions[i].Type == condType {
						return &conditions[i]
					}
				}
				return nil
			}

			By("Verifying DeploymentReady is True (template pinned at zero: nothing to be ready)")
			deployCond := findCondition(updatedAgent.Status.Conditions, ainselv1alpha1.AgentConditionDeploymentReady)
			Expect(deployCond).NotTo(BeNil())
			Expect(deployCond.Status).To(Equal(metav1.ConditionTrue))
			Expect(deployCond.Reason).To(Equal("TemplatePinnedAtZero"))

			By("Verifying MCPDiscoveryComplete is True")
			mcpCond := findCondition(updatedAgent.Status.Conditions, ainselv1alpha1.AgentConditionMCPDiscoveryComplete)
			Expect(mcpCond).NotTo(BeNil())
			Expect(mcpCond.Status).To(Equal(metav1.ConditionTrue))

			By("Verifying PersonaConfigMapReady is False (no persona CM in this test)")
			personaCond := findCondition(updatedAgent.Status.Conditions, ainselv1alpha1.AgentConditionPersonaConfigMapReady)
			Expect(personaCond).NotTo(BeNil())
			Expect(personaCond.Status).To(Equal(metav1.ConditionFalse))
			Expect(personaCond.Reason).To(Equal("NotFound"))

			By("Verifying ImageEnvSecretReady is True")
			secretCond := findCondition(updatedAgent.Status.Conditions, ainselv1alpha1.AgentConditionImageEnvSecretReady)
			Expect(secretCond).NotTo(BeNil())
			Expect(secretCond.Status).To(Equal(metav1.ConditionTrue))

			By("Verifying aggregate Ready is False (PersonaConfigMapReady not ready; template Deployment is trivially ready)")
			readyCondition := findCondition(updatedAgent.Status.Conditions, ainselv1alpha1.AgentConditionReady)
			Expect(readyCondition).NotTo(BeNil())
			Expect(readyCondition.Status).To(Equal(metav1.ConditionFalse))
			Expect(readyCondition.Reason).To(Equal("SubConditionsNotReady"))
			Expect(readyCondition.Message).To(ContainSubstring("PersonaConfigMapReady"))

			By("Creating the persona ConfigMap and simulating Deployment readiness")
			pcm := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "persona-01hxtestpersona00000000000",
					Namespace: "default",
				},
				Data: map[string]string{"CLAUDE.md": "test persona"},
			}
			Expect(k8sClient.Create(ctx, pcm)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, pcm) })

			deploy = &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: agentName, Namespace: "default"}, deploy)).To(Succeed())
			deploy.Status.ReadyReplicas = 1
			deploy.Status.Replicas = 1
			Expect(k8sClient.Status().Update(ctx, deploy)).To(Succeed())

			By("Reconciling again to pick up ready replicas")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the Agent status has Ready=True condition")
			Expect(k8sClient.Get(ctx, typeNamespacedName, updatedAgent)).To(Succeed())
			readyCondition = findCondition(updatedAgent.Status.Conditions, ainselv1alpha1.AgentConditionReady)
			Expect(readyCondition).NotTo(BeNil())
			Expect(readyCondition.Status).To(Equal(metav1.ConditionTrue))
			Expect(readyCondition.Reason).To(Equal("AllReady"))
		})

		It("should reflect the Deployment's ReadyReplicas in Agent status.replicas", func() {
			By("Initial reconciliation creates the Deployment")
			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Status.replicas defaults to 0 while no pods are ready")
			// Reset deployment status in case a previous test left ReadyReplicas > 0.
			deploy := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "agent-" + resourceName,
				Namespace: "default",
			}, deploy)).To(Succeed())
			deploy.Status.ReadyReplicas = 0
			deploy.Status.Replicas = 0
			deploy.Status.AvailableReplicas = 0
			Expect(k8sClient.Status().Update(ctx, deploy)).To(Succeed())

			// Re-reconcile to pick up the zeroed status.
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			updatedAgent := &ainselv1alpha1.Agent{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updatedAgent)).To(Succeed())
			Expect(updatedAgent.Status.Replicas).To(BeEquivalentTo(0))

			By("Simulating the Deployment becoming ready")
			deploy = &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "agent-" + resourceName,
				Namespace: "default",
			}, deploy)).To(Succeed())
			deploy.Status.Replicas = 1
			deploy.Status.ReadyReplicas = 1
			deploy.Status.AvailableReplicas = 1
			Expect(k8sClient.Status().Update(ctx, deploy)).To(Succeed())

			By("Reconciling again propagates ReadyReplicas to Agent status")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, typeNamespacedName, updatedAgent)).To(Succeed())
			Expect(updatedAgent.Status.Replicas).To(BeEquivalentTo(1))
		})

		It("should keep the Deployment pinned at zero despite spec.scaling.replicas", func() {
			By("Setting spec.scaling.replicas = 3 on the Agent")
			agent := &ainselv1alpha1.Agent{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, agent)).To(Succeed())
			agent.Spec.Scaling = &ainselv1alpha1.AgentScaling{
				Replicas: ptr.To[int32](3),
			}
			Expect(k8sClient.Update(ctx, agent)).To(Succeed())

			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			deploy := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "agent-" + resourceName,
				Namespace: "default",
			}, deploy)).To(Succeed())

			Expect(deploy.Spec.Replicas).NotTo(BeNil())
			Expect(*deploy.Spec.Replicas).To(BeEquivalentTo(0),
				"the Deployment is the task template, not an executor; spec.scaling.replicas caps task Jobs instead")
		})

		It("should pin the Deployment at zero when scaling is nil", func() {
			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			deploy := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "agent-" + resourceName,
				Namespace: "default",
			}, deploy)).To(Succeed())

			Expect(deploy.Spec.Replicas).NotTo(BeNil())
			Expect(*deploy.Spec.Replicas).To(BeEquivalentTo(0),
				"a new agent starts with no warm pod; its first task gets a Job")
		})

		It("should find affected agents for a referenced secret", func() {
			agent := ainselv1alpha1.Agent{
				Spec: ainselv1alpha1.AgentSpec{
					OllamaCloud: &ainselv1alpha1.AgentOllamaCloud{
						APIKeySecretRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "my-ollama-secret"},
							Key:                  "api-key",
						},
					},
					OpenCode: &ainselv1alpha1.AgentOpenCode{
						APIKeySecretRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "my-opencode-secret"},
							Key:                  "api-key",
						},
					},
					AlibabaCloud: &ainselv1alpha1.AgentAlibabaCloud{
						APIKeySecretRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "my-alibaba-secret"},
							Key:                  "api-key",
						},
					},
				},
			}
			agent.Name = "test-agent"

			Expect(referencesSecret(agent, "my-ollama-secret")).To(BeTrue())
			Expect(referencesSecret(agent, "my-opencode-secret")).To(BeTrue())
			Expect(referencesSecret(agent, "my-alibaba-secret")).To(BeTrue())
			Expect(referencesSecret(agent, "agent-test-agent-image-env")).To(BeTrue())
			Expect(referencesSecret(agent, "unrelated-secret")).To(BeFalse())
		})

		It("should find affected agents for a persona ConfigMap change", func() {
			By("Creating agents with different persona IDs")
			agent1 := &ainselv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "agent-persona-test-1",
					Namespace: "default",
				},
				Spec: ainselv1alpha1.AgentSpec{
					ImageRef: ainselv1alpha1.AgentImageRef{Name: testImageName},
					Persona:  ainselv1alpha1.AgentPersona{ID: "01hxtestpersona000000000AA"},
					LLM:      ainselv1alpha1.AgentLLM{Model: "glm-5.1:cloud", Provider: ainselv1alpha1.AgentLLMProviderOllamaCloud},
				},
			}
			Expect(k8sClient.Create(ctx, agent1)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, agent1) })

			agent2 := &ainselv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "agent-persona-test-2",
					Namespace: "default",
				},
				Spec: ainselv1alpha1.AgentSpec{
					ImageRef: ainselv1alpha1.AgentImageRef{Name: testImageName},
					Persona:  ainselv1alpha1.AgentPersona{ID: "01hxtestpersona000000000BB"},
					LLM:      ainselv1alpha1.AgentLLM{Model: "glm-5.1:cloud", Provider: ainselv1alpha1.AgentLLMProviderOllamaCloud},
				},
			}
			Expect(k8sClient.Create(ctx, agent2)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, agent2) })

			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			By("Mapping a persona ConfigMap to the matching agent only")
			personaCM := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "persona-01hxtestpersona000000000AA",
					Namespace: "default",
				},
			}
			requests := controllerReconciler.findAffectedAgentsFromConfigMap(ctx, personaCM)
			Expect(requests).To(ConsistOf(
				reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "agent-persona-test-1"}},
			))

			By("Ignoring a non-persona ConfigMap")
			otherCM := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "some-other-configmap",
					Namespace: "default",
				},
			}
			requests = controllerReconciler.findAffectedAgentsFromConfigMap(ctx, otherCM)
			Expect(requests).To(BeEmpty())
		})

		It("should reconcile successfully on first create", func() {
			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the deployment was created")
			deploy := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "agent-" + resourceName,
				Namespace: "default",
			}, deploy)).To(Succeed())
		})

		It("should find affected agents for an AgentImage change", func() {
			By("Creating a shared AgentImage")
			sharedImg := &ainselv1alpha1.AgentImage{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "shared-image",
					Namespace: "default",
				},
				Spec: ainselv1alpha1.AgentImageSpec{
					ImageURL: "shared:latest",
				},
			}
			Expect(k8sClient.Create(ctx, sharedImg)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, sharedImg) })

			By("Creating agents referencing the shared and another image")
			agent1 := &ainselv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "agent-with-shared-image",
					Namespace: "default",
				},
				Spec: ainselv1alpha1.AgentSpec{
					ImageRef: ainselv1alpha1.AgentImageRef{Name: "shared-image"},
					Persona:  ainselv1alpha1.AgentPersona{ID: "01hxtestpersona00000000000"},
					LLM:      ainselv1alpha1.AgentLLM{Model: "glm-5.1:cloud", Provider: ainselv1alpha1.AgentLLMProviderOllamaCloud},
				},
			}
			Expect(k8sClient.Create(ctx, agent1)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, agent1) })

			agent2 := &ainselv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "agent-with-shared-image-2",
					Namespace: "default",
				},
				Spec: ainselv1alpha1.AgentSpec{
					ImageRef: ainselv1alpha1.AgentImageRef{Name: "shared-image"},
					Persona:  ainselv1alpha1.AgentPersona{ID: "01hxtestpersona00000000001"},
					LLM:      ainselv1alpha1.AgentLLM{Model: "glm-5.1:cloud", Provider: ainselv1alpha1.AgentLLMProviderOllamaCloud},
				},
			}
			Expect(k8sClient.Create(ctx, agent2)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, agent2) })

			agent3 := &ainselv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "agent-with-other-image",
					Namespace: "default",
				},
				Spec: ainselv1alpha1.AgentSpec{
					ImageRef: ainselv1alpha1.AgentImageRef{Name: "other-image"},
					Persona:  ainselv1alpha1.AgentPersona{ID: "01hxtestpersona00000000002"},
					LLM:      ainselv1alpha1.AgentLLM{Model: "glm-5.1:cloud", Provider: ainselv1alpha1.AgentLLMProviderOllamaCloud},
				},
			}
			Expect(k8sClient.Create(ctx, agent3)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, agent3) })

			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			By("Mapping the shared AgentImage to referencing agents")
			requests := controllerReconciler.agentImageToAgents(ctx, sharedImg)
			Expect(requests).To(ConsistOf(
				reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "agent-with-shared-image"}},
				reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "agent-with-shared-image-2"}},
			))
		})

		It("should set a Degraded condition and emit a Warning Event when tokenFromEnv references a missing env var", func() {
			By("Updating the AgentImage with an MCP server whose tokenFromEnv is not defined in env")
			img := &ainselv1alpha1.AgentImage{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: testImageName, Namespace: "default"}, img)).To(Succeed())
			img.Spec.Env = nil
			img.Spec.MCPServers = []ainselv1alpha1.AgentImageMCPServer{
				{Name: "forgejo-mcp-server", URL: "http://x", TokenFromEnv: "MISSING_TOKEN"},
			}
			Expect(k8sClient.Update(ctx, img)).To(Succeed())

			By("Creating a fake recorder to capture Events")
			fakeRecorder := record.NewFakeRecorder(10)

			By("Reconciling")
			controllerReconciler := &AgentReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: fakeRecorder,
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the Agent status has a Degraded condition with reason MCPTokenEnvMissing")
			updatedAgent := &ainselv1alpha1.Agent{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updatedAgent)).To(Succeed())
			degradedCondition := func() *metav1.Condition {
				for i := range updatedAgent.Status.Conditions {
					if updatedAgent.Status.Conditions[i].Type == ainselv1alpha1.AgentConditionDegraded {
						return &updatedAgent.Status.Conditions[i]
					}
				}
				return nil
			}()
			Expect(degradedCondition).NotTo(BeNil(), "Degraded condition must be set when tokenFromEnv references a missing env var")
			Expect(degradedCondition.Status).To(Equal(metav1.ConditionTrue))
			Expect(degradedCondition.Reason).To(Equal("MCPTokenEnvMissing"))
			Expect(degradedCondition.Message).To(ContainSubstring("forgejo-mcp-server"))
			Expect(degradedCondition.Message).To(ContainSubstring("MISSING_TOKEN"))

			By("Verifying a Warning Event was recorded")
			Expect(receiveEventWith(fakeRecorder.Events, "MCPTokenEnvMissing", "forgejo-mcp-server")).To(BeTrue(),
				"a Warning event about the missing token env var must be recorded")

			By("Verifying MCP_SERVER_TOKENS is not set (all entries skipped)")
			deploy := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "agent-" + resourceName,
				Namespace: "default",
			}, deploy)).To(Succeed())
			tokensEnv := findEnvVar(deploy.Spec.Template.Spec.Containers[0].Env, "MCP_SERVER_TOKENS")
			Expect(tokensEnv).To(BeNil())

			By("Fixing the AgentImage env to include the missing var")
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: testImageName, Namespace: "default"}, img)).To(Succeed())
			img.Spec.Env = []ainselv1alpha1.AgentImageEnvVar{
				{Name: "MISSING_TOKEN", Value: "now-present", Secret: true},
			}
			Expect(k8sClient.Update(ctx, img)).To(Succeed())

			By("Reconciling again to clear the Degraded condition")
			fakeRecorder2 := record.NewFakeRecorder(10)
			controllerReconciler2 := &AgentReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: fakeRecorder2,
			}
			_, err = controllerReconciler2.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the Degraded condition is cleared")
			Expect(k8sClient.Get(ctx, typeNamespacedName, updatedAgent)).To(Succeed())
			degradedCondition = func() *metav1.Condition {
				for i := range updatedAgent.Status.Conditions {
					if updatedAgent.Status.Conditions[i].Type == ainselv1alpha1.AgentConditionDegraded {
						return &updatedAgent.Status.Conditions[i]
					}
				}
				return nil
			}()
			Expect(degradedCondition).NotTo(BeNil(), "Degraded condition should still exist but be False")
			Expect(degradedCondition.Status).To(Equal(metav1.ConditionFalse))
			Expect(degradedCondition.Reason).To(Equal("AsExpected"))

			By("Cleaning up — removing MCP servers from the image")
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: testImageName, Namespace: "default"}, img)).To(Succeed())
			img.Spec.MCPServers = nil
			img.Spec.Env = nil
			Expect(k8sClient.Update(ctx, img)).To(Succeed())
		})

		It("should find affected agents for a skills ConfigMap change", func() {
			By("Creating an AgentImage with enabled skills")
			skilledImg := &ainselv1alpha1.AgentImage{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "skilled-image",
					Namespace: "default",
				},
				Spec: ainselv1alpha1.AgentImageSpec{
					ImageURL:      "skilled:latest",
					EnabledSkills: []string{"git-review"},
				},
			}
			Expect(k8sClient.Create(ctx, skilledImg)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, skilledImg) })

			By("Creating an AgentImage without enabled skills")
			plainImg := &ainselv1alpha1.AgentImage{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "plain-image",
					Namespace: "default",
				},
				Spec: ainselv1alpha1.AgentImageSpec{
					ImageURL: "plain:latest",
				},
			}
			Expect(k8sClient.Create(ctx, plainImg)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, plainImg) })

			By("Creating agents referencing both images")
			agent1 := &ainselv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "agent-skilled",
					Namespace: "default",
				},
				Spec: ainselv1alpha1.AgentSpec{
					ImageRef: ainselv1alpha1.AgentImageRef{Name: "skilled-image"},
					Persona:  ainselv1alpha1.AgentPersona{ID: "01hxtestpersona00000000000"},
					LLM:      ainselv1alpha1.AgentLLM{Model: "glm-5.1:cloud", Provider: ainselv1alpha1.AgentLLMProviderOllamaCloud},
				},
			}
			Expect(k8sClient.Create(ctx, agent1)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, agent1) })

			agent2 := &ainselv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "agent-plain",
					Namespace: "default",
				},
				Spec: ainselv1alpha1.AgentSpec{
					ImageRef: ainselv1alpha1.AgentImageRef{Name: "plain-image"},
					Persona:  ainselv1alpha1.AgentPersona{ID: "01hxtestpersona00000000001"},
					LLM:      ainselv1alpha1.AgentLLM{Model: "glm-5.1:cloud", Provider: ainselv1alpha1.AgentLLMProviderOllamaCloud},
				},
			}
			Expect(k8sClient.Create(ctx, agent2)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, agent2) })

			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			By("Mapping the skills ConfigMap to agents with enabled skills")
			skillsCM := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "skills",
					Namespace: "default",
				},
			}
			requests := controllerReconciler.findAffectedAgentsFromConfigMap(ctx, skillsCM)
			Expect(requests).To(ConsistOf(
				reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "agent-skilled"}},
			))
		})
	})

	// Issue #96: provider selection is explicit. An Agent without a valid
	// spec.llm.provider must fail the reconcile loudly instead of silently
	// landing on Ollama Cloud.
	Context("When an Agent has an invalid LLM provider", func() {
		ctx := context.Background()

		BeforeEach(func() {
			ensureAgentFixtures(ctx)
		})

		AfterEach(func() {
			cleanupAgentFixtures(ctx)
		})

		It("should fail the reconcile with Ready=False/InvalidLLMProvider when the provider is unset", func() {
			agent := &ainselv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{Name: "agent-no-provider", Namespace: "default"},
				Spec: ainselv1alpha1.AgentSpec{
					ImageRef: ainselv1alpha1.AgentImageRef{Name: fixtureImageName},
					Persona:  ainselv1alpha1.AgentPersona{ID: "01hxtestpersona000000000CC"},
					LLM:      ainselv1alpha1.AgentLLM{Model: "glm-5.1:cloud"},
				},
			}
			Expect(k8sClient.Create(ctx, agent)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, agent) })

			nn := types.NamespacedName{Name: "agent-no-provider", Namespace: "default"}
			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			res, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.RequeueAfter).To(BeZero(), "an invalid provider must not hot-requeue; the object only changes on user edit")

			By("Reporting Ready=False with reason InvalidLLMProvider")
			got := &ainselv1alpha1.Agent{}
			Expect(k8sClient.Get(ctx, nn, got)).To(Succeed())
			var ready *metav1.Condition
			for i := range got.Status.Conditions {
				if got.Status.Conditions[i].Type == ainselv1alpha1.AgentConditionReady {
					ready = &got.Status.Conditions[i]
				}
			}
			Expect(ready).NotTo(BeNil(), "the reconcile must set a Ready condition")
			Expect(ready.Status).To(Equal(metav1.ConditionFalse))
			Expect(ready.Reason).To(Equal("InvalidLLMProvider"))
			Expect(ready.Message).To(ContainSubstring("spec.llm.provider"))

			By("Creating no Deployment for the invalid agent")
			deploy := &appsv1.Deployment{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "agent-agent-no-provider", Namespace: "default"}, deploy)
			Expect(errors.IsNotFound(err)).To(BeTrue(), "no Deployment may exist for an agent with an invalid provider")
		})

		It("should reject an unknown provider value at the API server via the CRD enum", func() {
			agent := &ainselv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{Name: "agent-bogus-provider", Namespace: "default"},
				Spec: ainselv1alpha1.AgentSpec{
					ImageRef: ainselv1alpha1.AgentImageRef{Name: fixtureImageName},
					Persona:  ainselv1alpha1.AgentPersona{ID: "01hxtestpersona000000000DD"},
					LLM:      ainselv1alpha1.AgentLLM{Model: "glm-5.1:cloud", Provider: "ollama"},
				},
			}
			// "ollama" is not one of the enum values — the API server must
			// reject it outright, before the operator ever sees the object.
			Expect(k8sClient.Create(ctx, agent)).NotTo(Succeed())
		})
	})
})
