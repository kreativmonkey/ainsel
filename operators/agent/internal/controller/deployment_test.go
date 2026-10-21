package controller

import (
	"context"

	ainselv1alpha1 "github.com/ainsel-project/ainsel/shared/api/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var _ = Describe("Agent Controller", func() {
	Context("When rendering the pod template", func() {
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

		It("should mount the hub-rendered persona ConfigMap named persona-<id>", func() {
			// The hub renders persona ConfigMaps as "persona-<id>" (see
			// services/hub/internal/personas/reconciler.go). The operator's
			// Deployment must mount that ConfigMap, not the legacy
			// <agent>-persona name the operator used to render itself.
			const personaID = "01hxtestpersona00000000000"

			agent := &ainselv1alpha1.Agent{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, agent)).To(Succeed())
			agent.Spec.Persona = ainselv1alpha1.AgentPersona{ID: personaID}
			Expect(k8sClient.Update(ctx, agent)).To(Succeed())

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

			var personaVol *corev1.Volume
			for i := range deploy.Spec.Template.Spec.Volumes {
				if deploy.Spec.Template.Spec.Volumes[i].Name == "persona" {
					personaVol = &deploy.Spec.Template.Spec.Volumes[i]
					break
				}
			}
			Expect(personaVol).NotTo(BeNil(), "persona volume must be present on the agent Deployment")
			Expect(personaVol.ConfigMap).NotTo(BeNil(), "persona volume must be backed by a ConfigMap")
			Expect(personaVol.ConfigMap.Name).To(Equal("persona-"+personaID),
				"persona volume must reference the hub-rendered ConfigMap by id")
		})

		It("should reconcile the agent deployment", func() {
			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify deployment was created
			deploy := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "agent-" + resourceName,
				Namespace: "default",
			}, deploy)).To(Succeed())
		})

		It("should expose the model as AINSEL_LLM_MODEL with OLLAMA_CLOUD_MODEL as a transitional alias", func() {
			// Issue #96: the runtime model contract is provider-neutral
			// AINSEL_LLM_MODEL. OLLAMA_CLOUD_MODEL is dual-written with the
			// same value for one release so old and new runtime images work
			// against old and new operators in either skew direction.
			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			container := mustDeployment("agent-" + resourceName).Spec.Template.Spec.Containers[0]
			var canonical, alias string
			seenCanonical, seenAlias := false, false
			for i := range container.Env {
				switch container.Env[i].Name {
				case "AINSEL_LLM_MODEL":
					seenCanonical = true
					canonical = container.Env[i].Value
				case "OLLAMA_CLOUD_MODEL":
					seenAlias = true
					alias = container.Env[i].Value
				}
			}
			Expect(seenCanonical).To(BeTrue(), "AINSEL_LLM_MODEL must be set on the agent container")
			Expect(seenAlias).To(BeTrue(), "OLLAMA_CLOUD_MODEL must stay set during the alias window")
			Expect(canonical).To(Equal("glm-5.1:cloud"))
			Expect(alias).To(Equal(canonical), "the alias must carry the same value as the canonical name")
		})

		It("should stamp a secret-hash annotation on the Deployment pod template", func() {
			By("Creating the referenced secrets")
			agentName := "agent-" + resourceName
			ollamaSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName + "-ollama-key",
					Namespace: "default",
				},
				Data: map[string][]byte{"api-key": []byte("ollama-key-value")},
			}
			Expect(k8sClient.Create(ctx, ollamaSecret)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, ollamaSecret) })

			opencodeSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName + "-opencode-key",
					Namespace: "default",
				},
				Data: map[string][]byte{"api-key": []byte("opencode-key-value")},
			}
			Expect(k8sClient.Create(ctx, opencodeSecret)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, opencodeSecret) })

			By("Reconciling")
			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the Deployment has the secret-hash annotation")
			deploy := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      agentName,
				Namespace: "default",
			}, deploy)).To(Succeed())
			Expect(deploy.Spec.Template.Annotations).To(HaveKey(secretHashAnnotation))
		})

		It("should change the secret-hash annotation when a referenced secret is updated", func() {
			By("Creating the referenced secrets")
			agentName := "agent-" + resourceName
			ollamaSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName + "-ollama-key",
					Namespace: "default",
				},
				Data: map[string][]byte{"api-key": []byte("initial-value")},
			}
			Expect(k8sClient.Create(ctx, ollamaSecret)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, ollamaSecret) })

			By("Reconciling to get the initial hash")
			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			deploy := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      agentName,
				Namespace: "default",
			}, deploy)).To(Succeed())
			initialHash := deploy.Spec.Template.Annotations[secretHashAnnotation]
			Expect(initialHash).NotTo(BeEmpty())

			By("Updating the secret data")
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: resourceName + "-ollama-key", Namespace: "default"}, ollamaSecret)).To(Succeed())
			ollamaSecret.Data["api-key"] = []byte("updated-value")
			Expect(k8sClient.Update(ctx, ollamaSecret)).To(Succeed())

			By("Reconciling again and verifying the hash changed")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      agentName,
				Namespace: "default",
			}, deploy)).To(Succeed())
			newHash := deploy.Spec.Template.Annotations[secretHashAnnotation]
			Expect(newHash).NotTo(BeEmpty())
			Expect(newHash).NotTo(Equal(initialHash))
		})

		It("should stamp a persona-hash annotation on the Deployment pod template", func() {
			By("Creating the persona ConfigMap")
			const personaID = "01hxtestpersona00000000000"
			personaCM := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "persona-" + personaID,
					Namespace: "default",
				},
				Data: map[string]string{
					"persona.md": "You are a helpful agent.",
				},
			}
			Expect(k8sClient.Create(ctx, personaCM)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, personaCM) })

			By("Reconciling")
			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the Deployment has the persona-hash annotation")
			agentName := "agent-" + resourceName
			deploy := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      agentName,
				Namespace: "default",
			}, deploy)).To(Succeed())
			Expect(deploy.Spec.Template.Annotations).To(HaveKey(personaHashAnnotation))
			initialHash := deploy.Spec.Template.Annotations[personaHashAnnotation]
			Expect(initialHash).NotTo(BeEmpty())
		})

		It("should change the persona-hash annotation when the persona ConfigMap data is updated", func() {
			By("Creating the persona ConfigMap with initial data")
			const personaID = "01hxtestpersona00000000000"
			personaCM := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "persona-" + personaID,
					Namespace: "default",
				},
				Data: map[string]string{
					"persona.md": "You are a helpful agent.",
				},
			}
			Expect(k8sClient.Create(ctx, personaCM)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, personaCM) })

			By("Reconciling to get the initial hash")
			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			agentName := "agent-" + resourceName
			deploy := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      agentName,
				Namespace: "default",
			}, deploy)).To(Succeed())
			initialHash := deploy.Spec.Template.Annotations[personaHashAnnotation]
			Expect(initialHash).NotTo(BeEmpty())

			By("Updating the persona ConfigMap data")
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "persona-" + personaID,
				Namespace: "default",
			}, personaCM)).To(Succeed())
			personaCM.Data["persona.md"] = "You are an even more helpful agent."
			Expect(k8sClient.Update(ctx, personaCM)).To(Succeed())

			By("Reconciling again and verifying the hash changed")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      agentName,
				Namespace: "default",
			}, deploy)).To(Succeed())
			newHash := deploy.Spec.Template.Annotations[personaHashAnnotation]
			Expect(newHash).NotTo(BeEmpty())
			Expect(newHash).NotTo(Equal(initialHash))
		})

		It("should change the pi-models-hash annotation when image input is turned on", func() {
			By("Reconciling to get the initial hash")
			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			agentName := "agent-" + resourceName
			deploy := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      agentName,
				Namespace: "default",
			}, deploy)).To(Succeed())
			Expect(deploy.Spec.Template.Annotations).To(HaveKey(piModelsHashAnnotation))
			initialHash := deploy.Spec.Template.Annotations[piModelsHashAnnotation]
			Expect(initialHash).NotTo(BeEmpty())

			By("Turning image input on")
			agent := &ainselv1alpha1.Agent{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, agent)).To(Succeed())
			vision := true
			agent.Spec.LLM.Vision = &vision
			Expect(k8sClient.Update(ctx, agent)).To(Succeed())

			By("Reconciling again and verifying the hash changed")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      agentName,
				Namespace: "default",
			}, deploy)).To(Succeed())
			newHash := deploy.Spec.Template.Annotations[piModelsHashAnnotation]
			Expect(newHash).NotTo(BeEmpty())
			Expect(newHash).NotTo(Equal(initialHash))

			By("Verifying the generated models.json advertises image input")
			modelsCM := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      agentName + "-pi-models",
				Namespace: "default",
			}, modelsCM)).To(Succeed())
			Expect(modelsCM.Data["models.json"]).To(ContainSubstring(`"input": ["text", "image"]`))
		})

		It("should mount the skills ConfigMap with projected items when the AgentImage has enabledSkills", func() {
			By("Updating the AgentImage to include enabled skills")
			img := &ainselv1alpha1.AgentImage{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: testImageName, Namespace: "default"}, img)).To(Succeed())
			img.Spec.EnabledSkills = []string{"git-review", "bash-advanced"}
			Expect(k8sClient.Update(ctx, img)).To(Succeed())

			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the Deployment mounts the skills volume with projected items")
			deploy := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "agent-" + resourceName,
				Namespace: "default",
			}, deploy)).To(Succeed())

			var skillsVol *corev1.Volume
			for i := range deploy.Spec.Template.Spec.Volumes {
				if deploy.Spec.Template.Spec.Volumes[i].Name == "agent-skills" {
					skillsVol = &deploy.Spec.Template.Spec.Volumes[i]
					break
				}
			}
			Expect(skillsVol).NotTo(BeNil(), "skills volume must be present when EnabledSkills is set")
			Expect(skillsVol.ConfigMap).NotTo(BeNil())
			Expect(skillsVol.ConfigMap.Name).To(Equal("skills"))
			Expect(skillsVol.ConfigMap.Items).To(HaveLen(2))
			Expect(skillsVol.ConfigMap.Items).To(ContainElements(
				corev1.KeyToPath{Key: "git-review", Path: "git-review/SKILL.md"},
				corev1.KeyToPath{Key: "bash-advanced", Path: "bash-advanced/SKILL.md"},
			))
			// The hub delivers skills on a size-capped shared object, so a
			// projected key can legitimately be absent. Tolerance must be
			// declared or the missing key fails volume setup and wedges the
			// pod instead of just leaving that skill undiscovered.
			Expect(skillsVol.ConfigMap.Optional).NotTo(BeNil())
			Expect(*skillsVol.ConfigMap.Optional).To(BeTrue(),
				"skills volume must be optional so an undelivered skill cannot block pod startup")

			By("Verifying the init container copies skills into the EmptyDir")
			initContainers := deploy.Spec.Template.Spec.InitContainers
			Expect(len(initContainers)).To(BeNumerically(">=", 1))
			setupCmd := initContainers[0].Command[2]
			Expect(setupCmd).To(ContainSubstring("cp -r /var/agent-skills/. /home/agent/.pi/agent/skills/"))
		})

		It("should mount the agent's own skill selection when spec.skills is set", func() {
			By("Enabling a skill on the image, then overriding the selection on the agent")
			img := &ainselv1alpha1.AgentImage{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: testImageName, Namespace: "default"}, img)).To(Succeed())
			img.Spec.EnabledSkills = []string{"git-review"}
			Expect(k8sClient.Update(ctx, img)).To(Succeed())

			Expect(updateSkillsWithRetry(ctx, typeNamespacedName, &ainselv1alpha1.AgentSkills{Items: []string{"bash-advanced"}})).To(Succeed())
			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the agent's list wins entirely (no merge with the image)")
			deploy := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "agent-" + resourceName,
				Namespace: "default",
			}, deploy)).To(Succeed())

			var skillsVol *corev1.Volume
			for i := range deploy.Spec.Template.Spec.Volumes {
				if deploy.Spec.Template.Spec.Volumes[i].Name == "agent-skills" {
					skillsVol = &deploy.Spec.Template.Spec.Volumes[i]
					break
				}
			}
			Expect(skillsVol).NotTo(BeNil(), "skills volume must be present")
			Expect(skillsVol.ConfigMap.Items).To(ConsistOf(
				corev1.KeyToPath{Key: "bash-advanced", Path: "bash-advanced/SKILL.md"},
			))
		})

		It("should mount no skills when spec.skills is explicitly empty", func() {
			By("Overriding an image skill with an explicit empty agent selection")
			img := &ainselv1alpha1.AgentImage{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: testImageName, Namespace: "default"}, img)).To(Succeed())
			img.Spec.EnabledSkills = []string{"git-review"}
			Expect(k8sClient.Update(ctx, img)).To(Succeed())

			Expect(updateSkillsWithRetry(ctx, typeNamespacedName, &ainselv1alpha1.AgentSkills{Items: []string{}})).To(Succeed())
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

			for i := range deploy.Spec.Template.Spec.Volumes {
				Expect(deploy.Spec.Template.Spec.Volumes[i].Name).NotTo(
					Equal("agent-skills"), "explicit empty selection must not mount the skills volume",
				)
			}
		})

		It("should stamp a skill-hash annotation on the Deployment pod template", func() {
			By("Creating the shared skills ConfigMap")
			skillsCM := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "skills",
					Namespace: "default",
				},
				Data: map[string]string{
					"git-review": "SKILL.md content",
				},
			}
			Expect(k8sClient.Create(ctx, skillsCM)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, skillsCM) })

			By("Updating the AgentImage to include enabled skills")
			img := &ainselv1alpha1.AgentImage{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: testImageName, Namespace: "default"}, img)).To(Succeed())
			img.Spec.EnabledSkills = []string{"git-review"}
			Expect(k8sClient.Update(ctx, img)).To(Succeed())

			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the Deployment has the skill-hash annotation")
			agentName := "agent-" + resourceName
			deploy := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      agentName,
				Namespace: "default",
			}, deploy)).To(Succeed())
			Expect(deploy.Spec.Template.Annotations).To(HaveKey(skillHashAnnotation))
			initialHash := deploy.Spec.Template.Annotations[skillHashAnnotation]
			Expect(initialHash).NotTo(BeEmpty())
		})

		It("should change the skill-hash annotation when the skills ConfigMap data is updated", func() {
			By("Creating the skills ConfigMap with initial data")
			skillsCM := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "skills",
					Namespace: "default",
				},
				Data: map[string]string{
					"git-review": "initial SKILL.md content",
				},
			}
			Expect(k8sClient.Create(ctx, skillsCM)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, skillsCM) })

			By("Updating the AgentImage to include enabled skills")
			img := &ainselv1alpha1.AgentImage{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: testImageName, Namespace: "default"}, img)).To(Succeed())
			img.Spec.EnabledSkills = []string{"git-review"}
			Expect(k8sClient.Update(ctx, img)).To(Succeed())

			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			agentName := "agent-" + resourceName
			deploy := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      agentName,
				Namespace: "default",
			}, deploy)).To(Succeed())
			initialHash := deploy.Spec.Template.Annotations[skillHashAnnotation]
			Expect(initialHash).NotTo(BeEmpty())

			By("Updating the skills ConfigMap data")
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "skills",
				Namespace: "default",
			}, skillsCM)).To(Succeed())
			skillsCM.Data["git-review"] = "updated SKILL.md content"
			Expect(k8sClient.Update(ctx, skillsCM)).To(Succeed())

			By("Reconciling again and verifying the hash changed")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      agentName,
				Namespace: "default",
			}, deploy)).To(Succeed())
			newHash := deploy.Spec.Template.Annotations[skillHashAnnotation]
			Expect(newHash).NotTo(BeEmpty())
			Expect(newHash).NotTo(Equal(initialHash))
		})

		It("should apply security hardening to the agent Deployment by default", func() {
			By("Reconciling without setting spec.runtime.securityHardened (defaults to true)")
			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			agentName := "agent-" + resourceName
			deploy := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: agentName, Namespace: "default"}, deploy)).To(Succeed())

			By("Verifying pod-level securityContext")
			podSec := deploy.Spec.Template.Spec.SecurityContext
			Expect(podSec).NotTo(BeNil())
			Expect(*podSec.RunAsNonRoot).To(BeTrue())
			Expect(*podSec.RunAsUser).To(BeEquivalentTo(1000))
			Expect(*podSec.RunAsGroup).To(BeEquivalentTo(1000))
			Expect(*podSec.FSGroup).To(BeEquivalentTo(1000))
			Expect(podSec.SeccompProfile).NotTo(BeNil())
			Expect(podSec.SeccompProfile.Type).To(Equal(corev1.SeccompProfileTypeRuntimeDefault))

			By("Verifying container-level securityContext on the agent container")
			agentContainer := deploy.Spec.Template.Spec.Containers[0]
			Expect(agentContainer.Name).To(Equal("agent"))
			Expect(agentContainer.SecurityContext).NotTo(BeNil())
			Expect(*agentContainer.SecurityContext.AllowPrivilegeEscalation).To(BeFalse())
			Expect(*agentContainer.SecurityContext.ReadOnlyRootFilesystem).To(BeTrue())
			Expect(agentContainer.SecurityContext.Capabilities).NotTo(BeNil())
			Expect(agentContainer.SecurityContext.Capabilities.Drop).To(ContainElement(corev1.Capability("ALL")))

			By("Verifying the app.kubernetes.io/component label is set")
			Expect(deploy.Spec.Template.Labels).To(HaveKeyWithValue("app.kubernetes.io/component", "agents"))

			By("Verifying the /tmp emptyDir volume and mount exist")
			var tmpVol *corev1.Volume
			for i := range deploy.Spec.Template.Spec.Volumes {
				if deploy.Spec.Template.Spec.Volumes[i].Name == "tmp" {
					tmpVol = &deploy.Spec.Template.Spec.Volumes[i]
					break
				}
			}
			Expect(tmpVol).NotTo(BeNil(), "/tmp emptyDir volume must be present when hardening is on")
			Expect(tmpVol.EmptyDir).NotTo(BeNil())

			var tmpMount *corev1.VolumeMount
			for i := range agentContainer.VolumeMounts {
				if agentContainer.VolumeMounts[i].Name == "tmp" {
					tmpMount = &agentContainer.VolumeMounts[i]
					break
				}
			}
			Expect(tmpMount).NotTo(BeNil(), "/tmp volume mount must be present on the agent container")
			Expect(tmpMount.MountPath).To(Equal("/tmp"))

			By("Verifying the init container has securityContext and no chown in command")
			initContainers := deploy.Spec.Template.Spec.InitContainers
			Expect(initContainers).NotTo(BeEmpty())
			setupInit := initContainers[0]
			Expect(setupInit.Name).To(Equal("setup-pi-models"))
			Expect(setupInit.SecurityContext).NotTo(BeNil())
			Expect(*setupInit.SecurityContext.RunAsNonRoot).To(BeTrue())
			Expect(*setupInit.SecurityContext.ReadOnlyRootFilesystem).To(BeTrue())
			Expect(setupInit.Command[2]).NotTo(ContainSubstring("chown"))
		})

		It("should keep a legacy Deployment selector while adding the component label to the pod template", func() {
			agentName := "agent-" + resourceName

			By("Pre-creating a Deployment with the legacy selector (no component label)")
			// envtest runs no garbage collector, so a Deployment left over from
			// an earlier spec (owned by a since-deleted Agent) must be removed
			// explicitly before seeding the legacy fixture.
			leftover := &appsv1.Deployment{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: agentName, Namespace: "default"}, leftover); err == nil {
				Expect(k8sClient.Delete(ctx, leftover)).To(Succeed())
			}
			legacyLabels := map[string]string{
				"app.kubernetes.io/name":       agentName,
				"app.kubernetes.io/managed-by": "agent-operator",
				"ainsel.dev/agent":             resourceName,
			}
			legacy := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      agentName,
					Namespace: "default",
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: ptr.To(int32(1)),
					Selector: &metav1.LabelSelector{MatchLabels: legacyLabels},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{Labels: legacyLabels},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{Name: "agent", Image: testImageURL}},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, legacy)).To(Succeed())

			By("Reconciling")
			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			// Regression: before the selector-preservation fix this failed with
			// "spec.selector ... field is immutable" because the reconcile tried
			// to add app.kubernetes.io/component to the existing selector.
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			deploy := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: agentName, Namespace: "default"}, deploy)).To(Succeed())

			By("Verifying the legacy selector is preserved unchanged")
			Expect(deploy.Spec.Selector.MatchLabels).To(Equal(legacyLabels))

			By("Verifying the pod template carries the component label")
			Expect(deploy.Spec.Template.Labels).To(HaveKeyWithValue("app.kubernetes.io/component", "agents"))

			By("Verifying the template still satisfies the preserved selector")
			for k, v := range deploy.Spec.Selector.MatchLabels {
				Expect(deploy.Spec.Template.Labels).To(HaveKeyWithValue(k, v))
			}
		})

		It("should skip security hardening when spec.runtime.securityHardened is false", func() {
			By("Setting spec.runtime.securityHardened to false")
			agent := &ainselv1alpha1.Agent{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, agent)).To(Succeed())
			agent.Spec.Runtime.SecurityHardened = ptr.To(false)
			Expect(k8sClient.Update(ctx, agent)).To(Succeed())

			By("Reconciling")
			controllerReconciler := &AgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			agentName := "agent-" + resourceName
			deploy := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: agentName, Namespace: "default"}, deploy)).To(Succeed())

			By("Verifying pod-level securityContext has only fsGroup")
			podSec := deploy.Spec.Template.Spec.SecurityContext
			Expect(podSec).NotTo(BeNil())
			Expect(podSec.RunAsNonRoot).To(BeNil())
			Expect(podSec.RunAsUser).To(BeNil())
			Expect(podSec.RunAsGroup).To(BeNil())
			Expect(*podSec.FSGroup).To(BeEquivalentTo(1000))
			Expect(podSec.SeccompProfile).To(BeNil())

			By("Verifying agent container has no securityContext")
			agentContainer := deploy.Spec.Template.Spec.Containers[0]
			Expect(agentContainer.SecurityContext).To(BeNil())

			By("Verifying no /tmp volume")
			for _, v := range deploy.Spec.Template.Spec.Volumes {
				Expect(v.Name).NotTo(Equal("tmp"), "/tmp volume must not be present when hardening is off")
			}

			By("Verifying init container has no securityContext and retains chown")
			initContainers := deploy.Spec.Template.Spec.InitContainers
			Expect(initContainers).NotTo(BeEmpty())
			setupInit := initContainers[0]
			Expect(setupInit.SecurityContext).To(BeNil())
			Expect(setupInit.Command[2]).To(ContainSubstring("chown"))
		})
	})
})
