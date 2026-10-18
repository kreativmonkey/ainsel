package v1alpha1

import (
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AgentSpec defines the desired state of an Agent.
type AgentSpec struct {
	DisplayName  string        `json:"displayName"`
	Description  string        `json:"description,omitempty"`
	ImageRef     AgentImageRef `json:"imageRef"`
	Runtime      AgentRuntime  `json:"runtime"`
	LLM          AgentLLM      `json:"llm"`
	Persona      AgentPersona  `json:"persona"`
	EnabledTools []string      `json:"enabledTools,omitempty"`
	// Skills explicitly selects the skill library entries this agent mounts.
	// When nil, the agent runs on the referenced image's EnabledSkills — the
	// shared runtime profile's defaults; once set, the agent's list replaces
	// the image's, and an empty Items means no skills.
	// +optional
	Skills *AgentSkills `json:"skills,omitempty"`
	// MCP explicitly defines the MCP servers this agent connects to at
	// runtime, as resolved definitions (URL + token env reference), not just
	// names. When nil, the agent runs on the referenced image's MCPServers —
	// the shared runtime profile's defaults; once set, the agent's servers
	// replace the image's, and empty Servers means no MCP connections.
	// +optional
	MCP *AgentMCP `json:"mcp,omitempty"`
	// Env holds this agent's environment variable overrides, applied on top of
	// the referenced image's Env — the shared runtime profile's defaults. An
	// entry whose Name matches a profile entry overrides its value; any other
	// name is added for this agent only. Platform-managed names are ignored by
	// the operator. Absent or empty means the agent runs on the profile's
	// defaults alone.
	// +optional
	Env     []AgentEnvVar `json:"env,omitempty"`
	Scaling *AgentScaling `json:"scaling,omitempty"`
	// Pause temporarily withholds task delivery from this agent: while active,
	// the hub's claim endpoint hands out no tasks, so pending agent_tasks rows
	// accumulate as a backlog that drains (in id order) once the pause ends.
	// Event ingestion and routing are unaffected — nothing is lost while the
	// agent is paused. Nil means the agent runs normally. This is orthogonal
	// to Scaling, which governs pod count, not task delivery.
	// +optional
	Pause          *AgentPause          `json:"pause,omitempty"`
	Memory         *AgentMemory         `json:"memory,omitempty"`
	OllamaCloud    *AgentOllamaCloud    `json:"ollamaCloud,omitempty"`
	OpenCode       *AgentOpenCode       `json:"openCode,omitempty"`
	AlibabaCloud   *AgentAlibabaCloud   `json:"alibabaCloud,omitempty"`
	CustomProvider *AgentCustomProvider `json:"customProvider,omitempty"`
}

// AgentImageRef references an AgentImage by metadata name in the same namespace.
type AgentImageRef struct {
	Name string `json:"name"`
}

// AgentSkills is the agent-scoped skill selection. The wrapper makes the
// distinction between "not configured" (nil = inherit the referenced
// image's EnabledSkills) and "explicitly empty" (Items: [] = no skills)
// representable in the CR — a bare []string could not.
type AgentSkills struct {
	// Items lists skill library ids (hub /skills API) to mount for this agent.
	// +optional
	Items []string `json:"items"`
}

// AgentMCPServer is one MCP server definition this agent connects to at
// runtime: the URL is resolved from the hub's MCP registry at write time,
// so the agent pod is decoupled from later registry edits.
type AgentMCPServer struct {
	// Name is the MCP registry entry name.
	Name string `json:"name"`
	// URL is the MCP server endpoint the agent runtime connects to.
	URL string `json:"url"`
	// TokenFromEnv names the env var on the agent pod whose value is sent
	// as the bearer token. Empty means the server needs no token.
	// +optional
	TokenFromEnv string `json:"tokenFromEnv,omitempty"`
}

// AgentMCP is the agent-scoped MCP selection: Servers replaces (never
// merges with) the referenced image's MCPServers when present.
type AgentMCP struct {
	// Servers lists the MCP servers this agent connects to.
	// +optional
	Servers []AgentMCPServer `json:"servers"`
}

// AgentEnvVar is one per-agent environment variable override.
//
// Unlike Skills and MCP, agent env is a plain list rather than a wrapper with
// nil/empty distinction: entries *merge* over the referenced image's Env by
// name instead of replacing it, so "not configured" and "explicitly none"
// mean the same thing — run on the profile's defaults.
type AgentEnvVar struct {
	// Name is the environment variable name. Matching a name in the
	// referenced image's Env overrides that value for this agent.
	Name string `json:"name"`
	// Value is the variable's value.
	Value string `json:"value"`
	// Secret marks Value as sensitive: the hub API masks it on read, and an
	// empty Value on write means "keep the existing value" — the same
	// contract as AgentImageEnvVar.
	// +optional
	Secret bool `json:"secret,omitempty"`
}

// AgentRuntime holds operator-managed runtime configuration for the agent pod.
// Fields like ImagePullPolicy and Resources are set by the operator or Helm
// bootstrap — they are not exposed via the hub REST API.
// +kubebuilder:validation:XValidation:rule="!has(self.sandboxProfile) || (has(self.engine) && self.engine == 'codex' && (!has(self.securityHardened) || self.securityHardened))",message="sandboxProfile codex requires engine codex and securityHardened enabled"
type AgentRuntime struct {
	// SandboxProfile selects an administrator-provisioned sandbox worker pool.
	// Codex requires a dedicated labeled node and profiles/ainsel-codex.json
	// installed under the kubelet seccomp root. Not exposed by the hub or MCP.
	// +optional
	// +kubebuilder:validation:Enum=codex
	SandboxProfile string `json:"sandboxProfile,omitempty"`

	// Engine selects the task runtime. Empty preserves the Pi runtime.
	// +optional
	// +kubebuilder:validation:Enum=pi;codex;claude
	Engine string `json:"engine,omitempty"`
	// Subscription provides the private persistent home for a native CLI.
	// +optional
	Subscription *AgentSubscription `json:"subscription,omitempty"`

	ImagePullPolicy corev1.PullPolicy           `json:"imagePullPolicy,omitempty"`
	Resources       corev1.ResourceRequirements `json:"resources,omitempty"`
	// SecurityHardened controls whether the operator applies pod and container
	// security hardening (readOnlyRootFilesystem, runAsNonRoot, drop ALL
	// capabilities, seccomp RuntimeDefault, etc.) to the agent Deployment.
	// Defaults to true when nil. Set to false to relax hardening for agent
	// images that require it (e.g. sidecar images that must run as root).
	// +optional
	SecurityHardened *bool `json:"securityHardened,omitempty"`
}

// AgentSubscription configures direct end-user CLI authentication. The operator
// never reads authentication files. Provision a dedicated ReadWriteOncePod PVC.
type AgentSubscription struct {
	// ClaimName references an existing PVC in the Agent namespace.
	// +kubebuilder:validation:MinLength=1
	ClaimName string `json:"claimName"`
	// Login drains task Jobs and opens an interactive login pod instead.
	// +optional
	Login bool `json:"login,omitempty"`
}

// AgentLLMProvider enumerates the supported LLM provider backends.
// The controller translates each provider into the corresponding Pi CLI
// models.json entry and --provider flag.
const (
	AgentLLMProviderOllamaCloud  = "ollama-cloud"
	AgentLLMProviderOpenCode     = "opencode"
	AgentLLMProviderAlibabaCloud = "alibaba-cloud"
	AgentLLMProviderCustom       = "custom"
)

type AgentLLM struct {
	Model       string   `json:"model"`
	Provider    string   `json:"provider,omitempty"`
	MaxTurns    int      `json:"maxTurns,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	// Vision controls whether pi sends image input. When true, the generated
	// pi models.json advertises "input": ["text", "image"] so pi's media
	// tools attach screenshots and image files; while false pi sends none,
	// even to a model that can see them. The endpoint is never probed: images
	// sent to a model that cannot see them fail the request. Changes take
	// effect when the agent's pod next starts, since the init container
	// copies models.json in at start. Defaults to false.
	// +optional
	Vision *bool `json:"vision,omitempty"`
	// ContextWindow overrides the context window (in tokens) written to the
	// agent's generated models.json. It must match what the actual LLM
	// endpoint enforces, not the model's paper limit: when the declared
	// window exceeds the endpoint's real limit, neither pi's own overflow
	// threshold nor the auto-compact preflight can fire before the endpoint
	// rejects the request with a context-overflow error (e.g. an Ollama
	// server serving a 131072-token context). 0 / unset means the single
	// uniform default of 131072 (128k). Raise it via this field only if the
	// endpoint actually allows more — the controller cannot detect the
	// endpoint's real limit.
	// +optional
	// +kubebuilder:validation:Minimum=1024
	ContextWindow *int `json:"contextWindow,omitempty"`
	// Cost is the price of this model in USD per million tokens, written to
	// the agent's generated pi models.json so pi can compute usage.cost for
	// every message. Without it pi defaults the cost to zero and the hub's
	// cost figures (conversation rows, token tiles, by-event totals) are all
	// exactly 0. The four rates are priced separately because prompt-cache
	// traffic is billed very differently from plain input (on Anthropic,
	// roughly cacheRead 0.1x / cacheWrite 1.25x of input): a cost derived
	// from an input+output total alone would be wrong in both directions.
	// All four fields are required when the block is set — pi's model schema
	// demands a complete cost object.
	// +optional
	Cost *AgentLLMCost `json:"cost,omitempty"`
}

// AgentLLMCost is a per-million-token price table in USD, mirroring pi's
// ModelCost schema (models.json "cost": {input, output, cacheRead, cacheWrite}).
type AgentLLMCost struct {
	// Input is the USD price per million uncached input tokens.
	Input float64 `json:"input"`
	// Output is the USD price per million output tokens.
	Output float64 `json:"output"`
	// CacheRead is the USD price per million prompt-cache read tokens.
	CacheRead float64 `json:"cacheRead"`
	// CacheWrite is the USD price per million prompt-cache write tokens.
	CacheWrite float64 `json:"cacheWrite"`
}

// AgentPersona points the agent runtime at a persona managed by the hub.
type AgentPersona struct {
	// ID is the ULID of a persona managed by the hub.
	// The agent operator mounts a ConfigMap named persona-<id>
	// (rendered by the hub) at /etc/agent/persona.md.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	ID string `json:"id"`
}

// AgentPause marks an agent as paused: its tasks stay in the queue until the
// pause ends. Presence of the block means paused; PausedUntil narrows it to a
// window that expires on its own.
type AgentPause struct {
	// PausedUntil is when the pause ends. While it is in the future the agent
	// stays paused; once it has passed the agent is running again without any
	// further action. Nil means the pause is indefinite and must be lifted
	// explicitly.
	// +optional
	PausedUntil *metav1.Time `json:"pausedUntil,omitempty"`
}

// IsPaused reports whether the agent's task delivery is currently withheld.
// The answer is evaluated at read time against now, so an expiring pause
// needs no timers or controllers: the same object reads paused before
// PausedUntil and running after it.
func (a *Agent) IsPaused(now time.Time) bool {
	p := a.Spec.Pause
	if p == nil {
		return false
	}
	if p.PausedUntil == nil {
		return true
	}
	return p.PausedUntil.After(now)
}

// AgentScaling holds the concurrency policy for the agent's task runs. Every
// queued task runs as its own Kubernetes Job — one pod, one task, exit — so
// Replicas is the cap on how many of those Jobs run at once. The agent's
// Deployment exists only as the pod template those Jobs are built from and is
// kept at zero replicas.
type AgentScaling struct {
	// Replicas caps the number of task Jobs running at once for this agent.
	// Each task runs in its own Job, so this is the agent's concurrency limit.
	// Defaults to 1. Set to 0 to pause the agent: due tasks stay queued.
	// +optional
	// +kubebuilder:validation:Minimum=0
	Replicas *int32 `json:"replicas,omitempty"`
}

type AgentMemory struct {
	Enabled  bool   `json:"enabled"`
	Provider string `json:"provider,omitempty"`
}

// AgentOllamaCloud configures the Ollama Cloud provider.
// The APIKey is stored in a secret created by the hub backend.
type AgentOllamaCloud struct {
	// APIKeySecretRef references the secret containing the Ollama Cloud API key.
	// The secret must have a key named "api-key".
	APIKeySecretRef *corev1.SecretKeySelector `json:"apiKeySecretRef,omitempty"`
}

// AgentOpenCode configures the OpenCode provider.
// The APIKey is stored in a secret created by the hub backend.
type AgentOpenCode struct {
	// APIKeySecretRef references the secret containing the OpenCode API key.
	// The secret must have a key named "api-key".
	APIKeySecretRef *corev1.SecretKeySelector `json:"apiKeySecretRef,omitempty"`
}

// AgentAlibabaCloud configures the Alibaba Token Plan provider.
// The APIKey is stored in a secret created by the hub backend.
type AgentAlibabaCloud struct {
	// APIKeySecretRef references the secret containing the Alibaba Token Plan API key.
	// The secret must have a key named "api-key".
	APIKeySecretRef *corev1.SecretKeySelector `json:"apiKeySecretRef,omitempty"`
}

// AgentCustomProvider configures a custom LLM provider.
// The URL is the OpenAI-compatible API endpoint and the API key is stored
// in a secret created by the hub backend.
type AgentCustomProvider struct {
	// URL is the base URL of the custom LLM API (e.g. https://api.openai.com/v1).
	URL string `json:"url"`
	// APIKeySecretRef references the secret containing the custom provider API key.
	// The secret must have a key named "api-key".
	APIKeySecretRef *corev1.SecretKeySelector `json:"apiKeySecretRef,omitempty"`
}

// AgentStatus defines the observed state of an Agent.
type AgentStatus struct {
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// Replicas is the number of ready pods, as observed by the operator.
	Replicas int32 `json:"replicas,omitempty"`

	// The four fields below are the queue signal the operator scales on. They
	// are written by the hub, which owns the task store, and read by the
	// operator; everything else in status is operator-owned. Both writers patch
	// only their own fields, so neither clobbers the other.

	// LastInvocation is when the hub last enqueued a task for this agent. It
	// is the idle clock for scaling down: quiet is measured from the last piece
	// of work arriving, not from the last pod exiting.
	LastInvocation *metav1.Time `json:"lastInvocation,omitempty"`

	// PendingTasks is agent_tasks rows waiting to be claimed.
	PendingTasks int32 `json:"pendingTasks,omitempty"`

	// ActiveTasks is agent_tasks rows currently being worked by a task pod.
	// In the per-task Job model a row is only marked claimed by a pod that has
	// already started running it, so this is a live-work count, not a drain
	// signal.
	ActiveTasks int32 `json:"activeTasks,omitempty"`

	// QueueObservedAt is when PendingTasks/ActiveTasks were last measured.
	QueueObservedAt *metav1.Time `json:"queueObservedAt,omitempty"`

	// PendingTaskIDs lists the oldest runnable pending task ids (retry
	// backoff elapsed), oldest first, capped at a fixed size. The operator
	// creates one Job per id, bounded by spec.scaling.replicas. Written by the
	// hub alongside the counts.
	// +optional
	PendingTaskIDs []int64 `json:"pendingTaskIds,omitempty"`

	// Paused mirrors the effective pause state of spec.pause (nil, expired or
	// active) as observed by the operator on its last reconcile, so
	// `kubectl get agents` shows which agents are withholding task delivery.
	// The authoritative check always evaluates spec.pause at read time; this
	// field is display state and may lag until the next reconcile.
	// +optional
	Paused bool `json:"paused,omitempty"`

	// Scaling explains the pod count the operator is driving toward. It exists
	// because zero pods is now a valid steady state, and a reader needs to tell
	// "asleep, waiting for work" from "broken".
	// +optional
	Scaling *AgentScalingStatus `json:"scaling,omitempty"`

	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// AgentScalingStatus is the operator's account of what it is doing with this
// agent's task Jobs.
type AgentScalingStatus struct {
	// Desired is the number of task Jobs the operator is keeping in flight,
	// bounded by spec.scaling.replicas. Compare with status.replicas (ready
	// pods) to see whether it is still catching up.
	Desired int32 `json:"desired,omitempty"`
	// Reason is a stable code for the decision: JobPerTask (work is running
	// as per-task Jobs) or Capped (more tasks are due than the concurrency
	// cap allows).
	Reason string `json:"reason,omitempty"`
	// Message is a one-line human-readable explanation, safe to show in a UI.
	// +optional
	Message string `json:"message,omitempty"`
}

const (
	AgentConditionReady                 = "Ready"
	AgentConditionDeploymentReady       = "DeploymentReady"
	AgentConditionConsumerReady         = "ConsumerReady"
	AgentConditionMCPDiscoveryComplete  = "MCPDiscoveryComplete"
	AgentConditionPersonaConfigMapReady = "PersonaConfigMapReady"
	AgentConditionImageEnvSecretReady   = "ImageEnvSecretReady"
	AgentConditionDegraded              = "Degraded"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Display Name",type=string,JSONPath=`.spec.displayName`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Replicas",type=integer,JSONPath=`.status.replicas`
// +kubebuilder:printcolumn:name="Paused",type=boolean,JSONPath=`.status.paused`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Agent is the Schema for the agents API.
type Agent struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AgentSpec   `json:"spec,omitempty"`
	Status AgentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AgentList contains a list of Agent.
type AgentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Agent `json:"items"`
}
