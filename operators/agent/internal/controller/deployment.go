package controller

import (
	"context"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	ainselv1alpha1 "github.com/ainsel-project/ainsel/shared/api/api/v1alpha1"
	sharedpersonas "github.com/ainsel-project/ainsel/shared/api/personas"
	sharedskills "github.com/ainsel-project/ainsel/shared/api/skills"

	"github.com/ainsel-project/ainsel/operators/agent/internal/controller/mcpservers"
)

const (
	// secretHashAnnotation is stamped on the Deployment pod template. When
	// the value changes Kubernetes performs a rolling restart automatically.
	secretHashAnnotation = "ainsel.dev/secret-hash" // #nosec G101 -- annotation key, not a credential
	// personaHashAnnotation is stamped on the Deployment pod template alongside
	// secretHashAnnotation. When the persona ConfigMap data changes, the hash
	// changes, which causes Kubernetes to perform a rolling restart automatically.
	personaHashAnnotation = "ainsel.dev/persona-hash"
	// skillHashAnnotation is stamped on the Deployment pod template alongside
	// secretHashAnnotation and personaHashAnnotation. When the shared skills
	// ConfigMap data changes, the hash changes, which causes Kubernetes to
	// perform a rolling restart automatically so agents pick up skill updates.
	skillHashAnnotation = "ainsel.dev/skill-hash"
	// piModelsHashAnnotation is stamped on the Deployment pod template. The
	// generated pi models.json is copied into the runtime by the setup-pi-models
)

// podPlan carries the pod-template state shared by the render stages of
// reconcileDeployment: the pre-computed volumes, mounts and init containers,
// plus the MCP_SERVERS entries and missing-env diagnostics that buildAgentEnv
// accumulates while the agent container env is rendered. Each stage that hands
// a slice to the rendered template copies it first, so no two consumers share
// the same backing array.
type podPlan struct {
	labels           map[string]string
	imagePullPolicy  corev1.PullPolicy
	securityHardened bool

	// personaCMName and piModelsCMName are the ConfigMaps whose hashes are
	// stamped as rolling-restart annotations on the pod template.
	personaCMName  string
	piModelsCMName string

	podVolumes        []corev1.Volume
	agentVolumeMounts []corev1.VolumeMount
	initContainers    []corev1.Container

	// mcpEntries accumulates the MCP_SERVERS list: the agent's own
	// definitions (or its runtime profile's), plus any sidecar-declared
	// servers and the injected chat sidecar.
	mcpEntries []string

	// missingEnv captures MCP servers whose tokenFromEnv references an
	// env var not defined on the AgentImage. It is populated inside the
	// CreateOrUpdate closure and returned to the caller so the Reconcile
	// loop can set a Degraded condition and emit Warning Events.
	missingEnv []mcpservers.MissingEnvEntry
}

// newPodPlan computes the pod-template state that does not depend on the
// existing Deployment: persona/pi/skills volumes, the setup-pi-models init
// container, and the hardening decision.
func newPodPlan(agent *ainselv1alpha1.Agent, agentName string, img *ainselv1alpha1.AgentImage) *podPlan {
	labels := map[string]string{
		"app.kubernetes.io/name":       agentName,
		"app.kubernetes.io/managed-by": "agent-operator",
		"app.kubernetes.io/component":  "agents",
		"ainsel.dev/agent":             agent.Name,
	}

	imagePullPolicy := agent.Spec.Runtime.ImagePullPolicy
	if imagePullPolicy == "" {
		imagePullPolicy = corev1.PullAlways
	}

	// Determine persona volume source. Hub renders this ConfigMap in
	// the hub namespace using the shared naming helper; see
	// shared/api/personas.PersonaConfigMapName — the single source of truth
	// for the format shared by producer (hub) and consumer (this operator).
	personaCMName := sharedpersonas.PersonaConfigMapName(agent.Spec.Persona.ID)

	agentVolumeMounts := []corev1.VolumeMount{
		{Name: "persona", MountPath: "/etc/agent/", ReadOnly: true},
		{Name: "workspace", MountPath: "/workspace"},
	}
	podVolumes := []corev1.Volume{
		{
			Name: "persona",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: personaCMName},
				},
			},
		},
		{
			Name:         "workspace",
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		},
	}
	var initContainers []corev1.Container

	// Pi config mount. Pi expects ~/.pi/agent/ to be writable (it creates
	// sessions/ etc.), so we use an emptyDir for the directory and an init
	// container to copy models.json from the ConfigMap into it.
	piModelsCMName := agentName + "-pi-models"
	podVolumes = append(podVolumes,
		corev1.Volume{
			Name:         "pi-home",
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		},
		corev1.Volume{
			Name: "pi-models",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: piModelsCMName},
				},
			},
		},
	)
	agentVolumeMounts = append(agentVolumeMounts, corev1.VolumeMount{
		Name:      "pi-home",
		MountPath: "/home/agent/.pi/agent",
	})
	// Skills are sourced from the single shared hub-managed ConfigMap
	// (sharedskills.ConfigMapName). The operator projects only the items
	// the AgentImage enabled via spec.enabledSkills so that pods that
	// don't reference a skill don't get it. Each enabled skill key is
	// projected as <id>/SKILL.md so Pi discovers it natively under
	// /home/agent/.pi/agent/skills/<id>/SKILL.md (handled by the init
	// container copy below).
	// Determine whether security hardening is enabled. Defaults to true
	// when spec.runtime.securityHardened is nil.
	securityHardened := agent.Spec.Runtime.SecurityHardened == nil || *agent.Spec.Runtime.SecurityHardened

	// With fsGroup: 1000, emptyDir volumes are group-owned by GID 1000,
	// so the init container (running as UID 1000) can write without chown
	// when hardening is on. When hardening is off, the init container runs
	// as root and chown is needed so UID 1000 can read the copied files.
	setupPiCommand := "cp /var/pi-models/models.json /home/agent/.pi/agent/models.json"
	if !securityHardened {
		setupPiCommand += " && chown 1000:1000 /home/agent/.pi/agent/models.json"
	}
	piInitVolumeMounts := []corev1.VolumeMount{
		{Name: "pi-home", MountPath: "/home/agent/.pi/agent"},
		{Name: "pi-models", MountPath: "/var/pi-models", ReadOnly: true},
	}
	skills := effectiveSkills(agent, img)
	if len(skills) > 0 {
		skillItems := make([]corev1.KeyToPath, 0, len(skills))
		for _, id := range skills {
			skillItems = append(skillItems, corev1.KeyToPath{
				Key:  id,
				Path: id + "/SKILL.md",
			})
		}
		podVolumes = append(podVolumes, corev1.Volume{
			Name: "agent-skills",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: sharedskills.ConfigMapName},
					Items:                skillItems,
					// The hub keeps this ConfigMap sized to the skills
					// agents have enabled, and delivery of one skill
					// can lag or fail outright while the shared object
					// is at its size ceiling. Optionality is declared
					// per volume, not per item, so this covers the
					// whole set: a missing key projects nothing and
					// the agent comes up with the skills that did
					// land. Without it the apiserver refuses to set
					// up the volume, so an undelivered skill would
					// wedge every pod that enabled it -- a worse
					// outcome than running without that one skill.
					Optional: ptr.To(true),
				},
			},
		})
		piInitVolumeMounts = append(piInitVolumeMounts, corev1.VolumeMount{
			Name:      "agent-skills",
			MountPath: "/var/agent-skills",
			ReadOnly:  true,
		})
		setupPiCommand += " && cp -r /var/agent-skills/. /home/agent/.pi/agent/skills/"
		if !securityHardened {
			setupPiCommand += " && chown -R 1000:1000 /home/agent/.pi/agent/skills"
		}
	}
	initContainer := corev1.Container{
		Name:  "setup-pi-models",
		Image: "busybox:1.37",
		Command: []string{
			"sh", "-c",
			setupPiCommand,
		},
		VolumeMounts: piInitVolumeMounts,
	}
	if securityHardened {
		initContainer.SecurityContext = &corev1.SecurityContext{
			RunAsUser:                ptr.To(int64(1000)),
			RunAsNonRoot:             ptr.To(true),
			AllowPrivilegeEscalation: ptr.To(false),
			ReadOnlyRootFilesystem:   ptr.To(true),
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{"ALL"},
			},
		}
	}
	initContainers = append(initContainers, initContainer)

	return &podPlan{
		labels:            labels,
		imagePullPolicy:   imagePullPolicy,
		securityHardened:  securityHardened,
		personaCMName:     personaCMName,
		piModelsCMName:    piModelsCMName,
		podVolumes:        podVolumes,
		agentVolumeMounts: agentVolumeMounts,
		initContainers:    initContainers,
	}
}

// reconcileDeployment converges the agent's Deployment, which exists solely as
// the pod template that buildTaskJob renders per-task Jobs from. It is pinned
// to zero replicas — all work runs as Jobs.
func (r *AgentReconciler) reconcileDeployment(ctx context.Context, agent *ainselv1alpha1.Agent, agentName string, img *ainselv1alpha1.AgentImage, podImage string) (*appsv1.Deployment, []mcpservers.MissingEnvEntry, error) {
	plan := newPodPlan(agent, agentName, img)
	if subscriptionRuntime(agent) {
		configureSubscriptionPlan(agent, plan)
	}

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      agentName,
			Namespace: agent.Namespace,
		},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, deploy, func() error {
		if err := controllerutil.SetControllerReference(agent, deploy, r.Scheme); err != nil {
			return err
		}

		// The Deployment selector is immutable. Deployments created before
		// app.kubernetes.io/component was added to the labels carry a selector
		// without it; trying to rewrite the selector fails every reconcile
		// with "field is immutable". Preserve the existing selector and only
		// update the pod template — the template labels are a superset of
		// every selector ever emitted, so the preserved selector keeps
		// matching rolled-out pods while the pods gain the component label.
		selector := deploy.Spec.Selector
		if selector == nil {
			selector = &metav1.LabelSelector{MatchLabels: plan.labels}
		}

		deploy.Labels = plan.labels

		agentEnv := buildAgentEnv(ctx, agent, agentName, img, plan)

		deploy.Spec = appsv1.DeploymentSpec{
			// Zero forever: work runs as per-task Jobs, never as Deployment
			// pods.
			Replicas: ptr.To(int32(0)),
			Selector: selector,
			Template: r.renderPodTemplate(agent, img, podImage, plan, agentEnv),
		}
		// Inject the sidecar containers composed by buildSidecars. The agent
		// container stays first in the list; sidecars share its network
		// namespace (accessible via localhost).
		if !nativeRuntime(agent) {
			deploy.Spec.Template.Spec.Containers = append(
				deploy.Spec.Template.Spec.Containers,
				buildSidecars(agent, agentName, img)...,
			)
		}

		return r.stampPodTemplateAnnotations(ctx, agent, agentName, img, podImage, plan, deploy)
	})
	if err != nil {
		return nil, plan.missingEnv, err
	}
	return deploy, plan.missingEnv, nil
}

// buildAgentEnv renders the agent container's environment. It also fills
// plan.mcpEntries (the MCP_SERVERS value) and plan.missingEnv (token
// references to env vars the AgentImage does not define), both consumed by
// later render stages and the Reconcile loop.
func buildAgentEnv(ctx context.Context, agent *ainselv1alpha1.Agent, agentName string, img *ainselv1alpha1.AgentImage, plan *podPlan) []corev1.EnvVar {
	envs := []corev1.EnvVar{
		{Name: "AGENT_NAME", Value: agent.Name},
		{Name: "PI_PROVIDER", Value: resolvePiProvider(agent).Name},
		{Name: "OLLAMA_CLOUD_MODEL", Value: agent.Spec.LLM.Model},
		{Name: "AGENT_PERSONA_PATH", Value: "/etc/agent/persona.md"},
		{Name: "HUB_URL", Value: resolveHubURL()},
		// HUB_INTERNAL_VALIDATE_SECRET authenticates the runner
		// against the hub's internal endpoints (X-Internal-Token).
		// It is platform-owned: AgentImage declarations are dropped
		// below so image config can never override it (see
		// platformManagedAgentEnv).
		{Name: "HUB_INTERNAL_VALIDATE_SECRET", Value: resolveHubInternalToken()},
		{Name: "HUB_ENABLED", Value: "true"},
	}
	// OLLAMA_API_KEY is required by the pi/Ollama-Cloud runtime.
	// The secret is created by the hub backend when the agent is
	// created with an ollamaCloud.apiKey. When spec.ollamaCloud
	// is unset the reference is optional so the pod still starts.
	ollamaSecretName := agent.Name + "-ollama-key"
	if agent.Spec.OllamaCloud != nil && agent.Spec.OllamaCloud.APIKeySecretRef != nil {
		ollamaSecretName = agent.Spec.OllamaCloud.APIKeySecretRef.Name
	}
	envs = append(envs, corev1.EnvVar{
		Name: "OLLAMA_API_KEY",
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: ollamaSecretName},
				Key:                  "api-key",
				Optional:             ptr.To(agent.Spec.OllamaCloud == nil),
			},
		},
	})
	// OPENCODE_API_KEY mirrors OLLAMA_API_KEY for agents using the
	// OpenCode provider. Optional so pods start even without a key.
	opencodeSecretName := agent.Name + "-opencode-key"
	if agent.Spec.OpenCode != nil && agent.Spec.OpenCode.APIKeySecretRef != nil {
		opencodeSecretName = agent.Spec.OpenCode.APIKeySecretRef.Name
	}
	envs = append(envs, corev1.EnvVar{
		Name: "OPENCODE_API_KEY",
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: opencodeSecretName},
				Key:                  "api-key",
				Optional:             ptr.To(agent.Spec.OpenCode == nil),
			},
		},
	})
	// ALIBABA_CLOUD_API_KEY mirrors OLLAMA_API_KEY for agents using
	// the Alibaba Token Plan provider. Optional so pods start even without a key.
	alibabaSecretName := agent.Name + "-alibaba-key"
	if agent.Spec.AlibabaCloud != nil && agent.Spec.AlibabaCloud.APIKeySecretRef != nil {
		alibabaSecretName = agent.Spec.AlibabaCloud.APIKeySecretRef.Name
	}
	envs = append(envs, corev1.EnvVar{
		Name: "ALIBABA_CLOUD_API_KEY",
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: alibabaSecretName},
				Key:                  "api-key",
				Optional:             ptr.To(agent.Spec.AlibabaCloud == nil),
			},
		},
	})
	// CUSTOM_PROVIDER_API_KEY is used by agents with a
	// custom LLM provider (non-standard endpoints). It is
	// optional so pods start even without a key.
	customProviderSecretName := agent.Name + "-custom-provider-key"
	if agent.Spec.CustomProvider != nil && agent.Spec.CustomProvider.APIKeySecretRef != nil {
		customProviderSecretName = agent.Spec.CustomProvider.APIKeySecretRef.Name
	}
	envs = append(envs, corev1.EnvVar{
		Name: "CUSTOM_PROVIDER_API_KEY",
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: customProviderSecretName},
				Key:                  "api-key",
				Optional:             ptr.To(agent.Spec.CustomProvider == nil),
			},
		},
	})
	if agent.Spec.LLM.MaxTurns > 0 {
		envs = append(envs, corev1.EnvVar{
			Name:  "OLLAMA_CLOUD_MAX_TURNS",
			Value: fmt.Sprintf("%d", agent.Spec.LLM.MaxTurns),
		})
	}
	// Translate spec.enabledTools into AGENT_TOOLS.
	// claude-cli tool kinds in the AgentImage catalog are
	// ignored — the runtime no longer supports them.
	kindByName := make(map[string]ainselv1alpha1.AgentImageToolKind, len(img.Spec.Tools))
	for _, t := range img.Spec.Tools {
		kindByName[t.Name] = t.Kind
	}
	var containerTools []string
	for _, name := range agent.Spec.EnabledTools {
		if kindByName[name] == ainselv1alpha1.AgentImageToolKindContainer {
			containerTools = append(containerTools, name)
		}
		// shell and claude-cli kinds are silently dropped
	}
	envs = append(envs,
		corev1.EnvVar{Name: "AGENT_TOOLS", Value: strings.Join(containerTools, ",")},
	)
	// Inject the effective runtime-profile env vars —
	// the image defaults with this agent's overrides
	// merged in — as explicit entries (not envFrom)
	// so they are eligible for Kubernetes $(VAR)
	// substitution in MCP_SERVER_TOKENS below. Values
	// are sourced from the operator-managed
	// <agent>-image-env Secret built by
	// reconcileImageEnvSecret.
	profileEnv := effectiveEnv(agent, img)
	imageEnvNames := make(map[string]bool, len(profileEnv))
	managedEnv := platformManagedAgentEnv()
	for _, e := range profileEnv {
		if _, reserved := managedEnv[e.Name]; reserved {
			// Platform-owned name: the canonical value is already
			// injected above. Skipping it here prevents the image
			// entry (added later, wins in k8s) from overriding it.
			continue
		}
		envs = append(envs, corev1.EnvVar{
			Name: e.Name,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: agentName + "-image-env"},
					Key:                  e.Name,
				},
			},
		})
		imageEnvNames[e.Name] = true
	}
	// Inject the effective MCP server URLs: the agent's
	// own definitions (explicit override, empty = none)
	// or the referenced image's servers (legacy).
	// Also append any MCP servers configured on the AgentImage.
	for _, s := range effectiveMCPServers(agent, img) {
		plan.mcpEntries = append(plan.mcpEntries, fmt.Sprintf("%s=%s", s.Name, s.URL))
	}
	// Append MCP entries for sidecar containers that declare an MCP path.
	for _, sc := range img.Spec.Sidecars {
		if sc.MCPPath == "" {
			continue
		}
		port := sc.Port
		if port == 0 {
			port = 8080
		}
		plan.mcpEntries = append(plan.mcpEntries, fmt.Sprintf("%s=http://localhost:%d%s", sc.Name, port, sc.MCPPath))
	}
	// Inject the chat-mcp sidecar entry unless the AgentImage
	// already defines one or CHAT_MCP_IMAGE is unset.
	plan.mcpEntries = append(plan.mcpEntries, chatMCPEntry(img)...)
	envs = append(envs, corev1.EnvVar{
		Name:  "MCP_SERVERS",
		Value: mcpservers.EnvValue(mcpservers.DedupeEntries(plan.mcpEntries)),
	})
	// Build MCP_SERVER_TOKENS using $(VAR) references that
	// Kubernetes substitutes from the image env entries
	// added above. Servers whose tokenFromEnv is missing
	// from the image env are skipped; the controller logs
	// them so a future Degraded condition can surface the
	// misconfiguration to the user.
	tokenValue, me := mcpservers.TokenEnvValue(effectiveMCPServers(agent, img), imageEnvNames)
	if len(me) > 0 {
		plan.missingEnv = append(plan.missingEnv, me...)
		var descs []string
		for _, m := range me {
			descs = append(descs, fmt.Sprintf("%s -> %s", m.ServerName, m.EnvVarName))
		}
		logf.FromContext(ctx).Info(
			"MCP server token references env var that is not defined on the AgentImage; skipping",
			"agent", agent.Name,
			"missing", strings.Join(descs, ", "),
		)
	}
	if tokenValue != "" {
		envs = append(envs, corev1.EnvVar{
			Name:  "MCP_SERVER_TOKENS",
			Value: tokenValue,
		})
	}
	if nativeRuntime(agent) {
		filtered := envs[:0]
		for _, e := range envs {
			switch e.Name {
			case "HUB_INTERNAL_VALIDATE_SECRET", "OLLAMA_API_KEY", "OPENCODE_API_KEY", "ALIBABA_CLOUD_API_KEY", "CUSTOM_PROVIDER_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "OPENAI_BASE_URL", "ANTHROPIC_BASE_URL", "MCP_SERVERS", "MCP_SERVER_TOKENS", "AINSEL_RUNTIME", "HOME", "CODEX_HOME", "CLAUDE_CONFIG_DIR":
				continue
			}
			filtered = append(filtered, e)
		}
		envs = append(filtered, subscriptionEnv(agent)...)
	}
	if piSubscription(agent) {
		envs = append(envs, subscriptionEnv(agent)...)
	}
	return envs
}

// renderPodTemplate assembles the pod template from the plan and the rendered
// agent-container env. The hardening branches copy the plan's slices before
// appending, so the plan itself is never mutated.
func (r *AgentReconciler) renderPodTemplate(agent *ainselv1alpha1.Agent, img *ainselv1alpha1.AgentImage, podImage string, plan *podPlan, agentEnv []corev1.EnvVar) corev1.PodTemplateSpec {
	// Build pod securityContext based on the securityHardened flag.
	var podSecContext *corev1.PodSecurityContext
	if plan.securityHardened {
		podSecContext = &corev1.PodSecurityContext{
			RunAsNonRoot: ptr.To(true),
			RunAsUser:    ptr.To(int64(1000)),
			RunAsGroup:   ptr.To(int64(1000)),
			FSGroup:      ptr.To(int64(1000)),
			SeccompProfile: &corev1.SeccompProfile{
				Type: corev1.SeccompProfileTypeRuntimeDefault,
			},
		}
	} else {
		podSecContext = &corev1.PodSecurityContext{
			FSGroup: ptr.To(int64(1000)),
		}
	}

	// Build volumes, adding /tmp emptyDir when hardening is on.
	// Copy the slice to avoid mutating the shared backing array.
	deployVolumes := append([]corev1.Volume{}, plan.podVolumes...)
	if plan.securityHardened {
		deployVolumes = append(deployVolumes, corev1.Volume{
			Name:         "tmp",
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		})
	}

	// Build agent container volume mounts, adding /tmp when hardening is on.
	// Copy the slice to avoid mutating the shared backing array.
	agentMounts := append([]corev1.VolumeMount{}, plan.agentVolumeMounts...)
	if plan.securityHardened {
		agentMounts = append(agentMounts, corev1.VolumeMount{
			Name:      "tmp",
			MountPath: "/tmp",
		})
	}

	// Build agent container securityContext.
	var agentSecContext *corev1.SecurityContext
	if plan.securityHardened {
		agentSecContext = &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptr.To(false),
			ReadOnlyRootFilesystem:   ptr.To(true),
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{"ALL"},
			},
		}
	}
	var command []string
	workingDir := ""
	if nativeRuntime(agent) {
		workingDir = "/workspace"
		command = subscriptionCommand("node", "/opt/ainsel/subscription/runner.mjs")
	}
	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels: plan.labels,
		},
		Spec: corev1.PodSpec{
			SecurityContext:               podSecContext,
			AutomountServiceAccountToken:  subscriptionServiceAccountToken(agent),
			TerminationGracePeriodSeconds: ptr.To(r.gracePeriod()),
			InitContainers:                plan.initContainers,
			Containers: []corev1.Container{
				{
					Name:            "agent",
					Command:         command,
					WorkingDir:      workingDir,
					Image:           podImage,
					ImagePullPolicy: plan.imagePullPolicy,
					Resources:       agent.Spec.Runtime.Resources,
					SecurityContext: agentSecContext,
					Ports: []corev1.ContainerPort{
						{Name: "health", ContainerPort: 8080, Protocol: corev1.ProtocolTCP},
						{Name: "metrics", ContainerPort: 9090, Protocol: corev1.ProtocolTCP},
					},
					Env:          agentEnv,
					VolumeMounts: agentMounts,
				},
			},
			Volumes: deployVolumes,
		},
	}
}

// stampPodTemplateAnnotations writes the rolling-restart trigger
// annotations on the pod template. The annotation keys and the hash input
// orderings are load-bearing: changing either re-restarts every agent pod on
// upgrade.
func (r *AgentReconciler) stampPodTemplateAnnotations(ctx context.Context, agent *ainselv1alpha1.Agent, agentName string, img *ainselv1alpha1.AgentImage, podImage string, plan *podPlan, deploy *appsv1.Deployment) error {
	// Compute a stable hash of all referenced secrets and annotate the
	// pod template. When a secret changes, the hash changes, which causes
	// Kubernetes to perform a rolling restart automatically.
	var hashRefs []corev1.SecretKeySelector
	if agent.Spec.OllamaCloud != nil && agent.Spec.OllamaCloud.APIKeySecretRef != nil {
		hashRefs = append(hashRefs, *agent.Spec.OllamaCloud.APIKeySecretRef)
	} else {
		hashRefs = append(hashRefs, corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: agent.Name + "-ollama-key"},
			Key:                  "api-key",
		})
	}
	if agent.Spec.OpenCode != nil && agent.Spec.OpenCode.APIKeySecretRef != nil {
		hashRefs = append(hashRefs, *agent.Spec.OpenCode.APIKeySecretRef)
	} else {
		hashRefs = append(hashRefs, corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: agent.Name + "-opencode-key"},
			Key:                  "api-key",
		})
	}
	if agent.Spec.AlibabaCloud != nil && agent.Spec.AlibabaCloud.APIKeySecretRef != nil {
		hashRefs = append(hashRefs, *agent.Spec.AlibabaCloud.APIKeySecretRef)
	} else {
		hashRefs = append(hashRefs, corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: agent.Name + "-alibaba-key"},
			Key:                  "api-key",
		})
	}
	if agent.Spec.CustomProvider != nil && agent.Spec.CustomProvider.APIKeySecretRef != nil {
		hashRefs = append(hashRefs, *agent.Spec.CustomProvider.APIKeySecretRef)
	} else {
		hashRefs = append(hashRefs, corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: agent.Name + "-custom-provider-key"},
			Key:                  "api-key",
			Optional:             ptr.To(true),
		})
	}
	if nativeRuntime(agent) {
		hashRefs = nil
	}
	hash, err := r.computeSecretHash(ctx, agent.Namespace, hashRefs, agentName+"-image-env")
	if err != nil {
		return fmt.Errorf("computing secret hash: %w", err)
	}
	if deploy.Spec.Template.Annotations == nil {
		deploy.Spec.Template.Annotations = map[string]string{}
	}
	deploy.Spec.Template.Annotations[secretHashAnnotation] = hash

	// Compute a stable hash of the persona ConfigMap data and annotate
	// the pod template. When the hub updates the persona ConfigMap, the
	// hash changes, which causes Kubernetes to perform a rolling restart
	// automatically so the agent picks up the new persona.
	personaHash, err := r.computeConfigMapHash(ctx, agent.Namespace, plan.personaCMName)
	if err != nil {
		return fmt.Errorf("computing persona hash: %w", err)
	}
	deploy.Spec.Template.Annotations[personaHashAnnotation] = personaHash

	// Compute a stable hash of the generated pi models.json and annotate
	// the pod template. The flag lives in the ConfigMap, not the pod spec,
	// so without this an agent that is already awake keeps advertising the
	// old model capabilities — notably spec.llm.vision, which pi only reads
	// at startup.
	if !nativeRuntime(agent) {
		piModelsHash, err := r.computeConfigMapHash(ctx, agent.Namespace, plan.piModelsCMName)
		if err != nil {
			return fmt.Errorf("computing pi models hash: %w", err)
		}
		deploy.Spec.Template.Annotations[piModelsHashAnnotation] = piModelsHash
	}

	// Compute a stable hash of the shared skills ConfigMap data and
	// annotate the pod template. When the hub updates a skill, the hash
	// changes, which causes Kubernetes to perform a rolling restart
	// automatically so the agent picks up the new skill content.
	if len(effectiveSkills(agent, img)) > 0 {
		skillHash, err := r.computeSkillsHash(ctx, agent.Namespace)
		if err != nil {
			return fmt.Errorf("computing skill hash: %w", err)
		}
		deploy.Spec.Template.Annotations[skillHashAnnotation] = skillHash
	}

	// Resolve the image digest from the container registry and annotate
	// the pod template. When a mutable tag (e.g. :dev) is rebuilt, the
	// digest changes, which causes Kubernetes to perform a rolling
	// restart so agents pick up the new image automatically.
	if digest, err := resolveImageDigest(ctx, podImage); err != nil {
		logf.FromContext(ctx).Info("could not resolve image digest; skipping restart annotation",
			"image", podImage, "error", err)
	} else {
		deploy.Spec.Template.Annotations[imageDigestAnnotation] = digest
	}
	return nil
}
