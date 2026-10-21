package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ainselv1alpha1 "github.com/ainsel-project/ainsel/shared/api/api/v1alpha1"
	sharedpersonas "github.com/ainsel-project/ainsel/shared/api/personas"
	sharedskills "github.com/ainsel-project/ainsel/shared/api/skills"

	"github.com/ainsel-project/ainsel/operators/agent/internal/controller/mcpservers"
)

const (
	// monitoringAPIVersion is the apiVersion of the prometheus-operator CRDs.
	monitoringAPIVersion = "monitoring.coreos.com/v1"
	// serviceMonitorKind is the prometheus-operator Kind that registers a scrape target.
	serviceMonitorKind = "ServiceMonitor"
	// agentTerminationGracePeriodSeconds gives a long-running agent task
	// time to finish on SIGTERM before Kubernetes SIGKILLs the pod.
	agentTerminationGracePeriodSeconds = int64(1800)
	// init container at pod start, so a change to what it advertises — vision,
	// the model, the context window — only reaches a running pod through a
	// restart. When the ConfigMap data changes the hash changes, and Kubernetes
	// performs a rolling restart automatically.
	piModelsHashAnnotation = "ainsel.dev/pi-models-hash"
)

// AgentReconciler reconciles a Agent object
type AgentReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// Recorder emits Kubernetes Events for the agent-controller. May be
	// nil — in that case Events are skipped (useful in unit tests that
	// don't need to assert on Events).
	Recorder record.EventRecorder

	// AgentGracePeriod is the pod terminationGracePeriodSeconds for agent
	// deployments. Zero uses the default (1800).
	AgentGracePeriod int64
}

//+kubebuilder:rbac:groups=ainsel.dev,resources=agents,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=ainsel.dev,resources=agents/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=ainsel.dev,resources=agents/finalizers,verbs=update
//+kubebuilder:rbac:groups=ainsel.dev,resources=agentimages,verbs=get;list;watch

// gracePeriod returns the configured agent grace period or the default.
func (r *AgentReconciler) gracePeriod() int64 {
	if r.AgentGracePeriod > 0 {
		return r.AgentGracePeriod
	}
	return agentTerminationGracePeriodSeconds
}

//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=monitoring.coreos.com,resources=servicemonitors,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=events,verbs=create;patch

// Reconcile reconciles an Agent CR by ensuring the associated ConfigMap, Deployment, and Service exist.
func (r *AgentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// 1. Fetch the Agent CR
	var agent ainselv1alpha1.Agent
	if err := r.Get(ctx, req.NamespacedName, &agent); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("Agent resource not found, probably deleted")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// 1.5. Deletion — owned objects (Deployment, per-task Jobs, Secrets,
	// ConfigMaps, Services) go away via ownerReferences; nothing else to clean up.
	if !agent.DeletionTimestamp.IsZero() {
		// The CR is being deleted.
		return ctrl.Result{}, nil
	}

	agentName := fmt.Sprintf("agent-%s", agent.Name)

	// 2. Resolve the AgentImage referenced by spec.imageRef.name.
	var img ainselv1alpha1.AgentImage
	if err := r.Get(ctx, types.NamespacedName{Namespace: agent.Namespace, Name: agent.Spec.ImageRef.Name}, &img); err != nil {
		if apierrors.IsNotFound(err) {
			apimeta.SetStatusCondition(&agent.Status.Conditions, metav1.Condition{
				Type:               ainselv1alpha1.AgentConditionReady,
				Status:             metav1.ConditionFalse,
				Reason:             "ImageNotFound",
				Message:            fmt.Sprintf("AgentImage %q not found in namespace %q", agent.Spec.ImageRef.Name, agent.Namespace),
				LastTransitionTime: metav1.Now(),
			})
			if updErr := r.patchStatus(ctx, &agent); updErr != nil {
				return ctrl.Result{}, updErr
			}
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		return ctrl.Result{}, err
	}
	podImage := img.Spec.ImageURL

	// 4. Defensive guardrail: the CRD requires spec.persona.id (minLength 1),
	// but verify here so we surface a controller-side diagnostic if a request
	// slips through (e.g. via a stale client). The hub renders the
	// "persona-<id>" ConfigMap when the persona row exists.
	if agent.Spec.Persona.ID == "" {
		return ctrl.Result{}, fmt.Errorf("agent %s: spec.persona.id is required", agent.Name)
	}

	if err := r.reconcileSubscriptionClaim(ctx, &agent); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.validateSubscription(ctx, &agent); err != nil {
		return ctrl.Result{}, err
	}

	// 4.25. Explicit LLM provider: the operator is authoritative even for
	// Agents stored before the CRD enum landed (and for the empty value,
	// which stays schema-valid for one release). Fail the reconcile loudly
	// instead of falling back to a built-in provider — an implicit provider
	// would send the agent's key and prompt traffic to a third-party SaaS
	// the user never chose (issue #96). No requeue: the object only changes
	// on user edit, which triggers a fresh reconcile.
	if !nativeRuntime(&agent) {
		if _, err := resolvePiProvider(&agent); err != nil {
			apimeta.SetStatusCondition(&agent.Status.Conditions, metav1.Condition{
				Type:               ainselv1alpha1.AgentConditionReady,
				Status:             metav1.ConditionFalse,
				Reason:             "InvalidLLMProvider",
				Message:            err.Error(),
				LastTransitionTime: metav1.Now(),
			})
			if updErr := r.patchStatus(ctx, &agent); updErr != nil {
				return ctrl.Result{}, updErr
			}
			log.Info("refusing to reconcile agent with invalid LLM provider", "error", err)
			return ctrl.Result{}, nil
		}
	}

	// 4.5. ConfigMap with Pi models.json for ollama-cloud agents (must exist before Deployment)
	if !nativeRuntime(&agent) {
		if err := r.reconcilePiModelsConfigMap(ctx, &agent, agentName); err != nil {
			return ctrl.Result{}, fmt.Errorf("reconciling Pi models ConfigMap: %w", err)
		}
	}

	// 4.75. Secret for AgentImage env vars (owned by the Agent for GC)
	if err := r.reconcileImageEnvSecret(ctx, &agent, agentName, &img); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling image env secret: %w", err)
	}
	apimeta.SetStatusCondition(&agent.Status.Conditions, metav1.Condition{
		Type:               ainselv1alpha1.AgentConditionImageEnvSecretReady,
		Status:             metav1.ConditionTrue,
		Reason:             "Reconciled",
		Message:            "Image env secret is up to date",
		LastTransitionTime: metav1.Now(),
	})

	// 5. Deployment — kept at zero replicas: it is the pod template the
	// per-task Jobs are built from, not an executor.
	deploy, mcpMissingEnv, err := r.reconcileDeployment(ctx, &agent, agentName, &img, podImage)
	if err != nil {
		return ctrl.Result{}, err
	}

	// 5.2. Dispatch: fan the hub's published due-task ids out into per-task
	// Jobs. This is the run account reported in status.
	scale, err := r.syncPerTaskJobs(ctx, &agent, agentName, deploy)
	if err != nil {
		return ctrl.Result{}, err
	}

	// 5.5. Surface MCP tokenFromEnv misconfiguration via Degraded condition
	// and Warning Events so operators can discover the problem via
	// kubectl describe agent and kubectl get events.
	r.reconcileMCPTokenEnvCondition(ctx, &agent, mcpMissingEnv)

	// 5.6. MCP discovery condition
	if len(mcpMissingEnv) == 0 {
		apimeta.SetStatusCondition(&agent.Status.Conditions, metav1.Condition{
			Type:               ainselv1alpha1.AgentConditionMCPDiscoveryComplete,
			Status:             metav1.ConditionTrue,
			Reason:             "AllResolved",
			Message:            "All enabled MCP servers resolved successfully",
			LastTransitionTime: metav1.Now(),
		})
	} else {
		var names []string
		for _, m := range mcpMissingEnv {
			names = append(names, m.ServerName)
		}
		apimeta.SetStatusCondition(&agent.Status.Conditions, metav1.Condition{
			Type:               ainselv1alpha1.AgentConditionMCPDiscoveryComplete,
			Status:             metav1.ConditionFalse,
			Reason:             "MissingEnvVars",
			Message:            fmt.Sprintf("MCP servers with missing env vars: %s", strings.Join(names, ", ")),
			LastTransitionTime: metav1.Now(),
		})
	}

	// 5.7. Persona ConfigMap condition.
	// A missing persona ConfigMap is non-blocking: the hub may not have
	// rendered it yet. We surface it as PersonaConfigMapReady=False so
	// operators can see the dependency, but reconciliation continues.
	personaCMName := sharedpersonas.PersonaConfigMapName(agent.Spec.Persona.ID)
	personaCM := &corev1.ConfigMap{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: agent.Namespace, Name: personaCMName}, personaCM); err != nil {
		if apierrors.IsNotFound(err) {
			apimeta.SetStatusCondition(&agent.Status.Conditions, metav1.Condition{
				Type:               ainselv1alpha1.AgentConditionPersonaConfigMapReady,
				Status:             metav1.ConditionFalse,
				Reason:             "NotFound",
				Message:            fmt.Sprintf("Persona ConfigMap %q not found in namespace %q", personaCMName, agent.Namespace),
				LastTransitionTime: metav1.Now(),
			})
		} else {
			return ctrl.Result{}, err
		}
	} else {
		apimeta.SetStatusCondition(&agent.Status.Conditions, metav1.Condition{
			Type:               ainselv1alpha1.AgentConditionPersonaConfigMapReady,
			Status:             metav1.ConditionTrue,
			Reason:             "Exists",
			Message:            fmt.Sprintf("Persona ConfigMap %q exists", personaCMName),
			LastTransitionTime: metav1.Now(),
		})
	}

	// 6. Service for metrics
	if err := r.reconcileMetricsService(ctx, &agent, agentName); err != nil {
		return ctrl.Result{}, err
	}

	// 7. ServiceMonitor so Prometheus scrapes the metrics Service.
	if err := r.reconcileServiceMonitor(ctx, &agent, agentName); err != nil {
		return ctrl.Result{}, err
	}

	// 8. Update status
	if err := r.updateStatus(ctx, &agent, deploy, scale); err != nil {
		return ctrl.Result{}, err
	}

	log.Info("Successfully reconciled Agent", "agent", agent.Name,
		"desiredReplicas", scale.Replicas, "scalingReason", scale.Reason)
	// Requeue periodically so the operator detects new image digests
	// behind mutable tags (e.g. :dev) and refreshes the task template.
	requeue := 5 * time.Minute
	// Dispatch waiting for a free slot must be looked at again sooner than
	// that, or backlogged tasks would sit until the next full period.
	if scale.RequeueAfter > 0 && scale.RequeueAfter < requeue {
		requeue = scale.RequeueAfter
	}
	// A pause that expires on its own must be looked at again when it lapses,
	// so status.paused (the kubectl/UI display mirror) self-clears without any
	// external trigger. Task delivery itself gates on spec.pause at read time
	// and never waits for this reconcile.
	if p := agent.Spec.Pause; p != nil && p.PausedUntil != nil {
		if rem := time.Until(p.PausedUntil.Time); rem > 0 && rem < requeue {
			requeue = rem
		}
	}
	return ctrl.Result{RequeueAfter: requeue}, nil
}

// reconcilePiModelsConfigMap creates a ConfigMap with a Pi CLI models.json
// that defines the provider for the agent's chosen LLM backend. The API key is
// resolved at runtime by Pi from the environment variable named in the provider
// config (injected by the operator).
func (r *AgentReconciler) reconcilePiModelsConfigMap(ctx context.Context, agent *ainselv1alpha1.Agent, agentName string) error {
	pp, err := resolvePiProvider(agent)
	if err != nil {
		// Reconcile validates the provider before reaching this point; an
		// error here means a caller bypassed that check. Fail the reconcile
		// rather than writing models.json for the wrong provider.
		return err
	}
	// `ainsel` is our own sibling-of-`providers` namespace at the root of
	// models.json. Pi ignores unknown root keys; the pi-ainsel-llm-config
	// extension reads this block to inject sampling params into each
	// provider request. Keeps all LLM config in one ConfigMap and out of
	// the pod env.
	var ainselBlock string
	if agent.Spec.LLM.Temperature != nil {
		ainselBlock = fmt.Sprintf(`,
    "ainsel": {
        "temperature": %s
    }`, strconv.FormatFloat(*agent.Spec.LLM.Temperature, 'f', -1, 64))
	}
	// Pi resolves `apiKey` values as env-var references only when prefixed
	// with `$` (or `${...}`). A bare identifier is treated as the literal
	// API key and the request fails with 401. Accept either form in
	// pp.APIKeyEnv ("OLLAMA_API_KEY" or "$OLLAMA_API_KEY") and always emit
	// the $-prefixed form pi expects.
	apiKeyRef := pp.APIKeyEnv
	if !strings.HasPrefix(apiKeyRef, "$") {
		apiKeyRef = "$" + apiKeyRef
	}
	// Build optional JSON fragments for provider-level and model-level
	// compat fields. These are generic: any provider can carry them.
	providerCompatBlock := ""
	if pp.ProviderCompat != "" {
		providerCompatBlock = ",\n            " + pp.ProviderCompat
	}
	modelCompatBlock := ""
	if pp.ModelCompat != "" {
		modelCompatBlock = ",\n                    " + pp.ModelCompat
	}
	// Pi's media-capable tools (read, screenshots, attachments) check the
	// model's "input" array and omit images entirely when "image" is
	// missing. Advertise vision only for models that actually support it
	// (spec.llm.vision); otherwise a text-only model would receive image
	// payloads it cannot parse.
	input := `["text"]`
	if agent.Spec.LLM.Vision != nil && *agent.Spec.LLM.Vision {
		input = `["text", "image"]`
	}
	// The declared context window must reflect what the ENDPOINT enforces,
	// not the model's paper limit: declare it too large and neither pi's
	// overflow threshold nor the auto-compact preflight can fire before the
	// endpoint rejects the request with a context-overflow error. The Agent
	// spec may override the default (typically to raise it).
	contextWindow := piDefaultContextWindow
	if agent.Spec.LLM.ContextWindow != nil && *agent.Spec.LLM.ContextWindow > 0 {
		contextWindow = *agent.Spec.LLM.ContextWindow
	}
	thinkingLevelMapBlock := ""
	if pp.ThinkingLevelMap != "" {
		thinkingLevelMapBlock = ",\n                    " + pp.ThinkingLevelMap
	}
	// Pi prices every message from the model's `cost` block (USD per million
	// tokens, all four components required by pi's ModelCost schema). A model
	// without it gets cost = 0, so usage.cost — and every cost figure the hub
	// derives from it — is exactly zero. The rates come from the Agent spec
	// (spec.llm.cost) so they are versioned with the agent rather than
	// hardcoded here. See issue #281.
	costBlock := ""
	if c := agent.Spec.LLM.Cost; c != nil {
		costBlock = fmt.Sprintf(`,
                    "cost": {
                        "input": %s,
                        "output": %s,
                        "cacheRead": %s,
                        "cacheWrite": %s
                    }`,
			strconv.FormatFloat(c.Input, 'g', -1, 64),
			strconv.FormatFloat(c.Output, 'g', -1, 64),
			strconv.FormatFloat(c.CacheRead, 'g', -1, 64),
			strconv.FormatFloat(c.CacheWrite, 'g', -1, 64))
	}
	modelsJSON := fmt.Sprintf(`{
    "providers": {
        %q: {
            "api": "openai-completions",
            "apiKey": %q,
            "baseUrl": %q%s,
            "models": [
                {
                    "id": %q,
                    "contextWindow": %d,
                    "input": %s,
                    "reasoning": true%s%s%s%s
                }
            ]
        }
    }%s
}`, pp.Name, apiKeyRef, pp.BaseURL, providerCompatBlock, agent.Spec.LLM.Model, contextWindow, input, pp.Compat, thinkingLevelMapBlock, modelCompatBlock, costBlock, ainselBlock)

	if piSubscription(agent) {
		// Keep the built-in Responses/OAuth provider and its model catalog.
		modelsJSON = fmt.Sprintf(`{"providers": {}%s}`, ainselBlock)
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      agentName + "-pi-models",
			Namespace: agent.Namespace,
		},
		Data: map[string]string{
			"models.json": modelsJSON,
		},
	}

	if err := controllerutil.SetControllerReference(agent, cm, r.Scheme); err != nil {
		return err
	}

	if err := r.Create(ctx, cm); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return err
		}
		existing := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: cm.Name, Namespace: cm.Namespace}}
		if err := r.Get(ctx, client.ObjectKeyFromObject(existing), existing); err != nil {
			return err
		}
		existing.Data = cm.Data
		if err := r.Update(ctx, existing); err != nil {
			return err
		}
	}
	return nil
}

// reconcileImageEnvSecret maintains the <agent>-image-env Secret holding the
// effective runtime-profile env: the referenced image's defaults with this
// agent's overrides merged in. The name predates agent-scoped overrides and is
// kept so existing Secrets (and the pods referencing them) are not orphaned.

func (r *AgentReconciler) reconcileImageEnvSecret(ctx context.Context, agent *ainselv1alpha1.Agent, agentName string, img *ainselv1alpha1.AgentImage) error {
	secretName := agentName + "-image-env"
	profileEnv := effectiveEnv(agent, img)
	if len(profileEnv) == 0 {
		// Clean up any previously-created secret when neither the image nor
		// the agent defines env vars.
		// Only delete if the secret is owned by this Agent to avoid removing
		// user-managed secrets.
		secret := &corev1.Secret{}
		if err := r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: agent.Namespace}, secret); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return fmt.Errorf("getting image env secret for cleanup: %w", err)
		}
		if metav1.IsControlledBy(secret, agent) {
			if err := r.Delete(ctx, secret); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("deleting unused image env secret: %w", err)
			}
		}
		return nil
	}

	secretData := make(map[string][]byte, len(profileEnv))
	managedEnv := platformManagedAgentEnv()
	for _, e := range profileEnv {
		if v, reserved := managedEnv[e.Name]; reserved {
			// Platform-owned names always carry the canonical platform value
			// so sidecars and $(VAR) substitutions referencing the secret
			// can never pick up a mis-set image or agent value.
			secretData[e.Name] = []byte(v)
			continue
		}
		if e.ValueFrom == nil {
			secretData[e.Name] = []byte(e.Value)
		}
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: agent.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, secret, func() error {
		if err := controllerutil.SetControllerReference(agent, secret, r.Scheme); err != nil {
			return err
		}
		secret.Data = secretData
		return nil
	})
	if err != nil {
		return fmt.Errorf("reconciling image env secret: %w", err)
	}
	return nil
}

// reconcileMCPTokenEnvCondition sets or clears the Degraded condition on the
// Agent status and emits Warning Events when MCP servers reference env vars
// that are not defined on the AgentImage. When missingEnv is empty the
func (r *AgentReconciler) reconcileMCPTokenEnvCondition(ctx context.Context, agent *ainselv1alpha1.Agent, missingEnv []mcpservers.MissingEnvEntry) {
	if len(missingEnv) > 0 {
		var parts []string
		for _, m := range missingEnv {
			parts = append(parts, fmt.Sprintf("MCP server %q references env var %q which is not defined on the AgentImage", m.ServerName, m.EnvVarName))
			if r.Recorder != nil {
				r.Recorder.Eventf(agent, corev1.EventTypeWarning, "MCPTokenEnvMissing",
					"MCP server %q references env var %q which is not defined on the AgentImage; the bearer header will be omitted for this MCP",
					m.ServerName, m.EnvVarName)
			}
		}
		apimeta.SetStatusCondition(&agent.Status.Conditions, metav1.Condition{
			Type:               ainselv1alpha1.AgentConditionDegraded,
			Status:             metav1.ConditionTrue,
			Reason:             "MCPTokenEnvMissing",
			Message:            strings.Join(parts, "; "),
			LastTransitionTime: metav1.Now(),
		})
	} else {
		// Clear the Degraded condition if it was previously set.
		if cond := apimeta.FindStatusCondition(agent.Status.Conditions, ainselv1alpha1.AgentConditionDegraded); cond != nil && cond.Status == metav1.ConditionTrue {
			apimeta.SetStatusCondition(&agent.Status.Conditions, metav1.Condition{
				Type:               ainselv1alpha1.AgentConditionDegraded,
				Status:             metav1.ConditionFalse,
				Reason:             "AsExpected",
				Message:            "All MCP server token env vars are defined",
				LastTransitionTime: metav1.Now(),
			})
		}
	}
}

func (r *AgentReconciler) reconcileMetricsService(ctx context.Context, agent *ainselv1alpha1.Agent, agentName string) error {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      agentName + "-metrics",
			Namespace: agent.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		if err := controllerutil.SetControllerReference(agent, svc, r.Scheme); err != nil {
			return err
		}
		svc.Spec = corev1.ServiceSpec{
			Selector: map[string]string{
				"app.kubernetes.io/name":       agentName,
				"app.kubernetes.io/managed-by": "agent-operator",
				"ainsel.dev/agent":             agent.Name,
			},
			Ports: []corev1.ServicePort{
				{
					Name:       "metrics",
					Port:       9090,
					TargetPort: intstr.FromInt32(9090),
					Protocol:   corev1.ProtocolTCP,
				},
			},
		}
		return nil
	})
	return err
}

// reconcileServiceMonitor manages a prometheus-operator ServiceMonitor for the
// agent's metrics Service, so Prometheus picks up the scrape target automatically.
//
// If the prometheus-operator CRDs are not installed (the ServiceMonitor CRD is missing)
// the call is logged and skipped — installation of prometheus-operator is a
// cluster-level prerequisite, not an agent-level failure.
func (r *AgentReconciler) reconcileServiceMonitor(ctx context.Context, agent *ainselv1alpha1.Agent, agentName string) error {
	log := logf.FromContext(ctx)

	sm := &unstructured.Unstructured{}
	sm.SetAPIVersion(monitoringAPIVersion)
	sm.SetKind(serviceMonitorKind)
	sm.SetName(agentName + "-metrics")
	sm.SetNamespace(agent.Namespace)

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, sm, func() error {
		if err := controllerutil.SetControllerReference(agent, sm, r.Scheme); err != nil {
			return err
		}
		sm.Object["spec"] = map[string]interface{}{
			"selector": map[string]interface{}{
				"matchLabels": map[string]interface{}{
					"app.kubernetes.io/name":       agentName,
					"app.kubernetes.io/managed-by": "agent-operator",
					"ainsel.dev/agent":             agent.Name,
				},
			},
			"endpoints": []interface{}{
				map[string]interface{}{
					"port":     "metrics",
					"path":     "/metrics",
					"interval": "30s",
				},
			},
		}
		return nil
	})
	if err != nil {
		if apimeta.IsNoMatchError(err) {
			log.Info("prometheus-operator ServiceMonitor CRD not installed; skipping scrape registration",
				"agent", agent.Name)
			return nil
		}
		return fmt.Errorf("reconcile ServiceMonitor: %w", err)
	}
	return nil
}

func (r *AgentReconciler) updateStatus(ctx context.Context, agent *ainselv1alpha1.Agent, deploy *appsv1.Deployment, scale ScaleDecision) error {
	// Record what the dispatcher decided, so a reader can tell a paused agent
	// from one whose task Jobs cannot get scheduled.
	agent.Status.Scaling = &ainselv1alpha1.AgentScalingStatus{
		Desired: scale.Replicas,
		Reason:  scale.Reason,
		Message: scale.Message,
	}

	// DeploymentReady condition. In this model the Deployment is the pod
	// template the per-task Jobs are built from and is pinned at zero, so its
	// own spec — not the task run account — decides what "ready" means here.
	// Judging it against the active Job count would make every busy agent look
	// broken, because Jobs run in their own pods.
	if deploy != nil {
		desired := int32(0)
		if deploy.Spec.Replicas != nil {
			desired = *deploy.Spec.Replicas
		}
		if desired == 0 {
			// Zero pods is the steady state of the template: there is nothing
			// to be ready. Reporting False here would make every idle agent
			// look broken in the UI and in alerts.
			apimeta.SetStatusCondition(&agent.Status.Conditions, metav1.Condition{
				Type:               ainselv1alpha1.AgentConditionDeploymentReady,
				Status:             metav1.ConditionTrue,
				Reason:             "TemplatePinnedAtZero",
				Message:            "Deployment is the task template, pinned at zero; work runs as per-task Jobs",
				LastTransitionTime: metav1.Now(),
			})
		} else if deploy.Status.ReadyReplicas >= desired {
			apimeta.SetStatusCondition(&agent.Status.Conditions, metav1.Condition{
				Type:               ainselv1alpha1.AgentConditionDeploymentReady,
				Status:             metav1.ConditionTrue,
				Reason:             "MinimumReplicasAvailable",
				Message:            fmt.Sprintf("Deployment has %d/%d ready replicas", deploy.Status.ReadyReplicas, desired),
				LastTransitionTime: metav1.Now(),
			})
		} else {
			apimeta.SetStatusCondition(&agent.Status.Conditions, metav1.Condition{
				Type:               ainselv1alpha1.AgentConditionDeploymentReady,
				Status:             metav1.ConditionFalse,
				Reason:             "ReplicasNotReady",
				Message:            fmt.Sprintf("Deployment has %d/%d ready replicas", deploy.Status.ReadyReplicas, desired),
				LastTransitionTime: metav1.Now(),
			})
		}
		agent.Status.Replicas = deploy.Status.ReadyReplicas
	}

	// Compute aggregate Ready condition: True only when all sub-conditions are True.
	subConditions := []string{
		ainselv1alpha1.AgentConditionDeploymentReady,
		ainselv1alpha1.AgentConditionMCPDiscoveryComplete,
		ainselv1alpha1.AgentConditionPersonaConfigMapReady,
		ainselv1alpha1.AgentConditionImageEnvSecretReady,
	}
	allReady := true
	var notReady []string
	for _, ct := range subConditions {
		cond := apimeta.FindStatusCondition(agent.Status.Conditions, ct)
		if cond == nil || cond.Status != metav1.ConditionTrue {
			allReady = false
			notReady = append(notReady, ct)
		}
	}

	readyCondition := metav1.Condition{
		Type:               ainselv1alpha1.AgentConditionReady,
		LastTransitionTime: metav1.Now(),
	}
	if allReady {
		readyCondition.Status = metav1.ConditionTrue
		readyCondition.Reason = "AllReady"
		readyCondition.Message = "All resources reconciled successfully"
	} else {
		readyCondition.Status = metav1.ConditionFalse
		readyCondition.Reason = "SubConditionsNotReady"
		readyCondition.Message = fmt.Sprintf("Waiting on: %s", strings.Join(notReady, ", "))
	}
	apimeta.SetStatusCondition(&agent.Status.Conditions, readyCondition)

	agent.Status.ObservedGeneration = agent.Generation

	// Mirror the effective pause state for `kubectl get agents` and UIs. The
	// authoritative gate evaluates spec.pause at read time in the hub; this is
	// display state and may lag until the next reconcile (which is scheduled
	// for the expiry moment when the pause is time-bounded).
	agent.Status.Paused = agent.IsPaused(time.Now())

	return r.patchStatus(ctx, agent)
}

// patchStatus writes only the status fields this controller owns.
//
// The hub writes the queue measurements on the same subresource, so a whole-object
// update here would roll back whatever the hub published between the read and the
// write — including the drain count an agent must not be scaled away without.
// A merge patch naming exactly these keys leaves the hub's fields alone.
func (r *AgentReconciler) patchStatus(ctx context.Context, agent *ainselv1alpha1.Agent) error {
	status := map[string]any{
		"replicas":           agent.Status.Replicas,
		"conditions":         agent.Status.Conditions,
		"observedGeneration": agent.Status.ObservedGeneration,
		// Explicit bool: omitempty would drop `false` from the merge patch
		// and a resumed agent would keep reporting the paused mirror.
		"paused": agent.Status.Paused,
	}
	// The scaling decision is written key by key instead of marshalling the struct:
	// every field of it is omitempty in the Go type, so a zero would drop out of the
	// merge patch and the previous value would survive. That is what a parked agent
	// looks like — converged on nothing, still reporting the pod it last wanted.
	if s := agent.Status.Scaling; s != nil {
		status["scaling"] = map[string]any{
			"desired": s.Desired,
			"reason":  s.Reason,
			"message": s.Message,
		}
	}
	body, err := json.Marshal(map[string]any{"status": status})
	if err != nil {
		return fmt.Errorf("marshal agent status: %w", err)
	}
	return r.Status().Patch(ctx, agent, client.RawPatch(types.MergePatchType, body))
}

// findAffectedAgents maps a Secret event to the Agent(s) that reference it.
func (r *AgentReconciler) findAffectedAgents(ctx context.Context, obj client.Object) []reconcile.Request {
	secret := obj.(*corev1.Secret)
	log := logf.FromContext(ctx)

	agentList := &ainselv1alpha1.AgentList{}
	if err := r.List(ctx, agentList, client.InNamespace(secret.Namespace)); err != nil {
		log.Error(err, "failed to list agents for secret watcher")
		return nil
	}

	var requests []reconcile.Request
	for _, agent := range agentList.Items {
		if referencesSecret(agent, secret.Name) {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: agent.Namespace,
					Name:      agent.Name,
				},
			})
		}
	}
	return requests
}

// findAffectedAgentsFromConfigMap maps a ConfigMap event to the Agent(s) whose
// spec.persona.id references it or whose AgentImage has enabled skills. Only
// ConfigMaps with the "persona-" prefix or the shared skills ConfigMap are
// considered; all other ConfigMaps are silently ignored to keep overhead low.
func (r *AgentReconciler) findAffectedAgentsFromConfigMap(ctx context.Context, obj client.Object) []reconcile.Request {
	cm := obj.(*corev1.ConfigMap)
	log := logf.FromContext(ctx)

	// Fast path: persona ConfigMaps.
	if strings.HasPrefix(cm.Name, sharedpersonas.ConfigMapNamePrefix) {
		agentList := &ainselv1alpha1.AgentList{}
		if err := r.List(ctx, agentList, client.InNamespace(cm.Namespace)); err != nil {
			log.Error(err, "failed to list agents for persona ConfigMap watcher")
			return nil
		}
		var requests []reconcile.Request
		for _, agent := range agentList.Items {
			if sharedpersonas.PersonaConfigMapName(agent.Spec.Persona.ID) == cm.Name {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{
						Namespace: agent.Namespace,
						Name:      agent.Name,
					},
				})
			}
		}
		return requests
	}

	// Fast path: shared skills ConfigMap.
	if cm.Name == sharedskills.ConfigMapName {
		// Find all AgentImages with enabled skills so we can match them
		// against agents; agents with an explicit spec.skills are
		// matched regardless of their image.
		imgList := &ainselv1alpha1.AgentImageList{}
		if err := r.List(ctx, imgList, client.InNamespace(cm.Namespace)); err != nil {
			log.Error(err, "failed to list agent images for skills ConfigMap watcher")
			return nil
		}
		imageNamesWithSkills := make(map[string]struct{}, len(imgList.Items))
		for _, img := range imgList.Items {
			if len(img.Spec.EnabledSkills) > 0 {
				imageNamesWithSkills[img.Name] = struct{}{}
			}
		}

		agentList := &ainselv1alpha1.AgentList{}
		if err := r.List(ctx, agentList, client.InNamespace(cm.Namespace)); err != nil {
			log.Error(err, "failed to list agents for skills ConfigMap watcher")
			return nil
		}
		var requests []reconcile.Request
		for _, agent := range agentList.Items {
			_, imgHasSkills := imageNamesWithSkills[agent.Spec.ImageRef.Name]
			if agent.Spec.Skills != nil || imgHasSkills {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{
						Namespace: agent.Namespace,
						Name:      agent.Name,
					},
				})
			}
		}
		return requests
	}

	return nil
}

// agentImageToAgents maps an AgentImage change to all Agent CRs that reference it.
func (r *AgentReconciler) agentImageToAgents(ctx context.Context, obj client.Object) []reconcile.Request {
	img, ok := obj.(*ainselv1alpha1.AgentImage)
	if !ok {
		return nil
	}
	log := logf.FromContext(ctx)

	agentList := &ainselv1alpha1.AgentList{}
	if err := r.List(ctx, agentList, client.InNamespace(img.Namespace)); err != nil {
		log.Error(err, "failed to list agents for agentimage watcher")
		return nil
	}

	var requests []reconcile.Request
	for _, agent := range agentList.Items {
		if agent.Spec.ImageRef.Name == img.Name {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: agent.Namespace,
					Name:      agent.Name,
				},
			})
		}
	}
	return requests
}

func referencesSecret(agent ainselv1alpha1.Agent, secretName string) bool {
	if agent.Spec.OllamaCloud != nil && agent.Spec.OllamaCloud.APIKeySecretRef != nil && agent.Spec.OllamaCloud.APIKeySecretRef.Name == secretName {
		return true
	}
	if agent.Spec.OpenCode != nil && agent.Spec.OpenCode.APIKeySecretRef != nil && agent.Spec.OpenCode.APIKeySecretRef.Name == secretName {
		return true
	}
	if agent.Spec.AlibabaCloud != nil && agent.Spec.AlibabaCloud.APIKeySecretRef != nil && agent.Spec.AlibabaCloud.APIKeySecretRef.Name == secretName {
		return true
	}
	if agent.Spec.CustomProvider != nil && agent.Spec.CustomProvider.APIKeySecretRef != nil && agent.Spec.CustomProvider.APIKeySecretRef.Name == secretName {
		return true
	}
	// The image-env secret is always operator-managed with a fixed name.
	if "agent-"+agent.Name+"-image-env" == secretName {
		return true
	}
	return false
}

// SetupWithManager sets up the controller with the Manager.
func (r *AgentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&ainselv1alpha1.Agent{}).
		Owns(&appsv1.Deployment{}).
		Owns(&batchv1.Job{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.Pod{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.Secret{}).
		Watches(
			&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.findAffectedAgents),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Watches(
			&ainselv1alpha1.AgentImage{},
			handler.EnqueueRequestsFromMapFunc(r.agentImageToAgents),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Watches(
			&corev1.ConfigMap{},
			handler.EnqueueRequestsFromMapFunc(r.findAffectedAgentsFromConfigMap),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Named("agent").
		Complete(r)
}
