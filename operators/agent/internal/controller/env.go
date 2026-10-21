package controller

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	ainselv1alpha1 "github.com/ainsel-project/ainsel/shared/api/api/v1alpha1"
	sharedskills "github.com/ainsel-project/ainsel/shared/api/skills"
)

type piProviderConfig struct {
	// Name is the provider key in Pi's models.json and the --provider flag value.
	Name string
	// BaseURL is the OpenAI-completions base URL.
	BaseURL string
	// APIKeyEnv is the environment variable name that holds the API key.
	APIKeyEnv string
	// Compat is an optional JSON object string for Pi model compat flags.
	Compat string
	// ProviderCompat is an optional JSON object string emitted at the
	// provider level in models.json (e.g. supportsDeveloperRole, maxTokensField).
	ProviderCompat string
	// ModelCompat is an optional JSON object string emitted at the model
	// level in models.json (e.g. thinkingFormat).
	ModelCompat string
	// ThinkingLevelMap is an optional JSON object string that maps pi
	// thinking levels to provider-specific values.
	ThinkingLevelMap string
}

// piLLMProviderAllowedValues lists the accepted spec.llm.provider values, in
// the order they should appear in error messages. It mirrors the CRD enum on
// AgentLLM.Provider; keep the two in sync.
var piLLMProviderAllowedValues = []string{
	ainselv1alpha1.AgentLLMProviderOllamaCloud,
	ainselv1alpha1.AgentLLMProviderOpenCode,
	ainselv1alpha1.AgentLLMProviderAlibabaCloud,
	ainselv1alpha1.AgentLLMProviderCustom,
	ainselv1alpha1.AgentLLMProviderOpenAICodex,
}

// resolvePiProvider returns the Pi CLI provider configuration for the
// agent's LLM provider. The provider must be set explicitly on the Agent:
// there is deliberately no fallback to a built-in provider. An unset or
// unknown provider is an error, not a default — silently landing on
// ollama-cloud would send the agent's API key and prompt traffic to a
// third-party SaaS the user never chose (issue #96). The CRD enum rejects
// unknown non-empty values at the API server; this check also covers Agents
// stored before the enum landed, and the empty value.
//
// Each built-in provider's base URL may be overridden per agent via the
// provider block's baseUrl field (e.g. a different Alibaba MaaS region);
// custom providers keep taking their URL from spec.customProvider.url.
func resolvePiProvider(agent *ainselv1alpha1.Agent) (piProviderConfig, error) {
	switch agent.Spec.LLM.Provider {
	case ainselv1alpha1.AgentLLMProviderOpenAICodex:
		if !piSubscription(agent) || agent.Spec.Runtime.Subscription == nil {
			return piProviderConfig{}, fmt.Errorf("openai-codex requires a Pi subscription")
		}
		return piProviderConfig{Name: "openai-codex"}, nil
	case ainselv1alpha1.AgentLLMProviderOllamaCloud:
		cfg := piProviderConfig{
			Name:      "ollama-api-key",
			BaseURL:   "https://ollama.com/v1",
			APIKeyEnv: "OLLAMA_API_KEY", // #nosec G101 -- env var name, not a hardcoded value
		}
		if agent.Spec.OllamaCloud != nil && agent.Spec.OllamaCloud.BaseURL != "" {
			cfg.BaseURL = agent.Spec.OllamaCloud.BaseURL
		}
		return cfg, nil
	case ainselv1alpha1.AgentLLMProviderOpenCode:
		cfg := piProviderConfig{
			Name:      "opencode",
			BaseURL:   "https://opencode.ai/zen/go/v1",
			APIKeyEnv: "OPENCODE_API_KEY", // #nosec G101 -- env var name, not a hardcoded value
			Compat:    `,"compat":{"requiresReasoningContentOnAssistantMessages":true}`,
		}
		if agent.Spec.OpenCode != nil && agent.Spec.OpenCode.BaseURL != "" {
			cfg.BaseURL = agent.Spec.OpenCode.BaseURL
		}
		return cfg, nil
	case ainselv1alpha1.AgentLLMProviderAlibabaCloud:
		cfg := piProviderConfig{
			Name:             "alibaba-cloud",
			BaseURL:          "https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1",
			APIKeyEnv:        "ALIBABA_CLOUD_API_KEY", // #nosec G101 -- env var name, not a hardcoded value
			ProviderCompat:   `"compat":{"supportsDeveloperRole":false,"supportsUsageInStreaming":false,"maxTokensField":"max_tokens","supportsReasoningEffort":false}`,
			ModelCompat:      `"compat":{"thinkingFormat":"qwen"}`,
			ThinkingLevelMap: `"thinkingLevelMap":{"off":null,"minimal":null,"low":null,"medium":"medium","high":"high","xhigh":null}`,
		}
		if agent.Spec.AlibabaCloud != nil && agent.Spec.AlibabaCloud.BaseURL != "" {
			cfg.BaseURL = agent.Spec.AlibabaCloud.BaseURL
		}
		return cfg, nil
	case ainselv1alpha1.AgentLLMProviderCustom:
		// Use "openai" as the provider key so Pi CLI recognizes it.
		// The custom baseUrl is injected via models.json.
		cfg := piProviderConfig{
			Name:      "openai",
			APIKeyEnv: "CUSTOM_PROVIDER_API_KEY",
			Compat:    "",
		}
		if agent.Spec.CustomProvider != nil {
			cfg.BaseURL = agent.Spec.CustomProvider.URL
		}
		return cfg, nil
	case "":
		return piProviderConfig{}, fmt.Errorf(
			"spec.llm.provider is required: set it to one of %s", strings.Join(piLLMProviderAllowedValues, ", "))
	default:
		return piProviderConfig{}, fmt.Errorf(
			"spec.llm.provider %q is unknown: must be one of %s", agent.Spec.LLM.Provider, strings.Join(piLLMProviderAllowedValues, ", "))
	}
}

// piDefaultContextWindow is the context window (in tokens) written to the
// agent's generated models.json when the Agent spec does not set
// spec.llm.contextWindow. It is deliberately a single uniform value, not a
// per-model or per-provider detection: the controller never queries the
// endpoint, and "auto" implies a certainty the code does not have. The value
// must reflect what the actual endpoint enforces — if it is declared larger
// than the endpoint's real limit, the context overflows before any compaction
// can trigger (pi's own threshold and the auto-compact preflight are both
// relative to the declared window; declaring too large silently disables
// compaction until the endpoint errors, while declaring too small only makes
// compaction fire early, which is harmless). 131072 (128k) is the safe
// fallback: most endpoints (Ollama, llama.cpp, LM Studio, gpt-oss on Ollama
// Cloud) enforce exactly that. Endpoints that allow more declare the larger
// window via spec.llm.contextWindow.
const piDefaultContextWindow = 131072

// resolveHubURL returns the hub backend REST API URL that MCP sidecars
// (e.g. the chat-mcp sidecar) use to proxy requests back to the hub. The
// operator reads HUB_URL from its own environment; the chart injects it
// on the operator Deployment.
func resolveHubURL() string {
	if v := os.Getenv("HUB_URL"); v != "" {
		return v
	}
	return ""
}

// resolveHubInternalToken returns the shared secret that MCP sidecars use
// to bypass OIDC auth on the hub backend. The operator reads
// HUB_INTERNAL_VALIDATE_SECRET from its own environment; the chart injects
// it on the operator Deployment.
func resolveHubInternalToken() string {
	return os.Getenv("HUB_INTERNAL_VALIDATE_SECRET")
}

// platformManagedAgentEnv returns env names the platform owns for the agent
// container, mapped to their canonical values. When the operator knows the
// hub internal token, AgentImage env declarations for these names are
// ignored (container env) and overridden (image-env Secret): a mis-set value
// on an image silently breaks auth on the hub internal endpoints and thus
// the whole task claim path (incident 2026-08-07, back then via the legacy
// AGENT_TOKEN name, which has since been removed). Returns nil when the
// platform token is unknown so legacy setups keep working unchanged.
func platformManagedAgentEnv() map[string]string {
	token := resolveHubInternalToken()
	if token == "" {
		return nil
	}
	return map[string]string{
		"HUB_INTERNAL_VALIDATE_SECRET": token,
	}
}

// platformSidecarEnv returns env vars that the operator automatically
// injects into every sidecar container that declares an MCPPath (i.e. MCP
// sidecars like the chat-mcp sidecar). These are platform-level vars the
// sidecar needs to communicate with the hub; they should not have to be
// configured manually per AgentImage.
func platformSidecarEnv() []corev1.EnvVar {
	var envs []corev1.EnvVar
	if hubURL := resolveHubURL(); hubURL != "" {
		envs = append(envs, corev1.EnvVar{Name: "HUB_URL", Value: hubURL})
	}
	if token := resolveHubInternalToken(); token != "" {
		envs = append(envs, corev1.EnvVar{Name: "HUB_INTERNAL_VALIDATE_SECRET", Value: token})
	}
	return envs
}

// computeSecretHash builds a stable hash of the data in the referenced secrets.
// If a secret does not exist, a stable sentinel value is used so that when the
// secret is later created the hash changes and triggers a restart.
func (r *AgentReconciler) computeSecretHash(ctx context.Context, namespace string, refs []corev1.SecretKeySelector, envSecretName string) (string, error) {
	h := sha256.New()

	// Sort refs by (Name, Key) so the hash is deterministic regardless of order.
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Name != refs[j].Name {
			return refs[i].Name < refs[j].Name
		}
		return refs[i].Key < refs[j].Key
	})

	for _, ref := range refs {
		secret := &corev1.Secret{}
		key := types.NamespacedName{Namespace: namespace, Name: ref.Name}
		err := r.Get(ctx, key, secret)
		if err != nil {
			if apierrors.IsNotFound(err) {
				// Stable sentinel for missing secret: "__missing__:<name>:<key>"
				_, _ = fmt.Fprintf(h, "__missing__:%s:%s\n", ref.Name, ref.Key)
				continue
			}
			return "", err
		}

		val, ok := secret.Data[ref.Key]
		if !ok {
			// Stable sentinel for missing key inside an existing secret.
			_, _ = fmt.Fprintf(h, "__missing_key__:%s:%s\n", ref.Name, ref.Key)
			continue
		}

		_, _ = fmt.Fprintf(h, "%s:%s:%x\n", ref.Name, ref.Key, val)
	}

	// Hash the image env secret (all keys).
	secret := &corev1.Secret{}
	key := types.NamespacedName{Namespace: namespace, Name: envSecretName}
	err := r.Get(ctx, key, secret)
	if err != nil {
		if apierrors.IsNotFound(err) {
			_, _ = fmt.Fprintf(h, "__missing__:%s:*\n", envSecretName)
		} else {
			return "", err
		}
	} else {
		keys := make([]string, 0, len(secret.Data))
		for k := range secret.Data {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			_, _ = fmt.Fprintf(h, "%s:%s:%x\n", envSecretName, k, secret.Data[k])
		}
	}

	return fmt.Sprintf("sha256:%x", h.Sum(nil)), nil
}

// computeConfigMapHash builds a stable hash of the Data map in a ConfigMap. If
// the ConfigMap does not exist, a stable sentinel value is used so that when it
// is later created the hash changes and triggers a restart. This mirrors the
// computeSecretHash behaviour for missing secrets.
func (r *AgentReconciler) computeConfigMapHash(ctx context.Context, namespace, cmName string) (string, error) {
	h := sha256.New()

	cm := &corev1.ConfigMap{}
	err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: cmName}, cm)
	if err != nil {
		if apierrors.IsNotFound(err) {
			_, _ = fmt.Fprintf(h, "__missing__:%s\n", cmName)
			return fmt.Sprintf("sha256:%x", h.Sum(nil)), nil
		}
		return "", err
	}

	// Sort keys for a deterministic hash regardless of map iteration order.
	keys := make([]string, 0, len(cm.Data))
	for k := range cm.Data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		_, _ = fmt.Fprintf(h, "%s:%s\n", k, cm.Data[k])
	}

	return fmt.Sprintf("sha256:%x", h.Sum(nil)), nil
}

// effectiveSkills returns the skill ids an agent mounts: the agent's own
// spec.skills when set (an explicit override, possibly empty), otherwise
// the referenced image's EnabledSkills (the profile's defaults).
func effectiveSkills(agent *ainselv1alpha1.Agent, img *ainselv1alpha1.AgentImage) []string {
	if agent.Spec.Skills != nil {
		return agent.Spec.Skills.Items
	}
	return img.Spec.EnabledSkills
}

// effectiveMCPServers returns the MCP server definitions the agent
// connects to: the agent's own selection when set (explicit override, empty
// = no servers), falling back to the referenced image's servers (the shared
// runtime profile's defaults). Agent definitions are snapshots resolved by
// the hub at write time, so registry edits never rewrite running agents.
func effectiveMCPServers(agent *ainselv1alpha1.Agent, img *ainselv1alpha1.AgentImage) []ainselv1alpha1.AgentImageMCPServer {
	if agent.Spec.MCP != nil {
		servers := make([]ainselv1alpha1.AgentImageMCPServer, 0, len(agent.Spec.MCP.Servers))
		for _, s := range agent.Spec.MCP.Servers {
			// The types are field-identical; staticcheck prefers the conversion.
			servers = append(servers, ainselv1alpha1.AgentImageMCPServer(s))
		}
		return servers
	}
	return img.Spec.MCPServers
}

// effectiveEnv returns the environment variables the agent pod takes from its
// runtime profile: the referenced image's Env (the shared defaults) with the
// agent's own overrides merged in by name. An agent entry matching an image
// name replaces that value; any other name is appended, so an agent can add
// variables the profile does not define. Image order is preserved and agent-only
// names keep their request order, which keeps the rendered Secret and env list
// stable across reconciles.
func effectiveEnv(agent *ainselv1alpha1.Agent, img *ainselv1alpha1.AgentImage) []ainselv1alpha1.AgentEnvVar {
	overrides := make(map[string]ainselv1alpha1.AgentEnvVar, len(agent.Spec.Env))
	order := make([]string, 0, len(agent.Spec.Env))
	for _, e := range agent.Spec.Env {
		if _, seen := overrides[e.Name]; !seen {
			order = append(order, e.Name)
		}
		// Duplicate names in the agent's list: the last entry wins. The hub
		// rejects duplicates, so this only guards hand-edited CRs.
		overrides[e.Name] = e
	}

	out := make([]ainselv1alpha1.AgentEnvVar, 0, len(img.Spec.Env)+len(order))
	inImage := make(map[string]bool, len(img.Spec.Env))
	for _, e := range img.Spec.Env {
		inImage[e.Name] = true
		if o, ok := overrides[e.Name]; ok {
			out = append(out, o)
			continue
		}
		out = append(out, ainselv1alpha1.AgentEnvVar{Name: e.Name, Value: e.Value, Secret: e.Secret})
	}
	for _, name := range order {
		if inImage[name] {
			continue
		}
		out = append(out, overrides[name])
	}
	return out
}

// computeSkillsHash builds a stable hash of the Data map in the shared skills
// ConfigMap. If the ConfigMap does not exist, a stable sentinel value is used
// so that when it is later created the hash changes and triggers a restart.

func (r *AgentReconciler) computeSkillsHash(ctx context.Context, namespace string) (string, error) {
	h := sha256.New()

	cm := &corev1.ConfigMap{}
	err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: sharedskills.ConfigMapName}, cm)
	if err != nil {
		if apierrors.IsNotFound(err) {
			_, _ = fmt.Fprintf(h, "__missing__:%s\n", sharedskills.ConfigMapName)
			return fmt.Sprintf("sha256:%x", h.Sum(nil)), nil
		}
		return "", err
	}

	keys := make([]string, 0, len(cm.Data))
	for k := range cm.Data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		_, _ = fmt.Fprintf(h, "%s:%s\n", k, cm.Data[k])
	}

	return fmt.Sprintf("sha256:%x", h.Sum(nil)), nil
}
