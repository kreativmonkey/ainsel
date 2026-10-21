package controller

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	ainselv1alpha1 "github.com/ainsel-project/ainsel/shared/api/api/v1alpha1"
	// +kubebuilder:scaffold:imports
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var (
	ctx       context.Context
	cancel    context.CancelFunc
	testEnv   *envtest.Environment
	cfg       *rest.Config
	k8sClient client.Client
)

func TestControllers(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Controller Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	ctx, cancel = context.WithCancel(context.TODO())

	var err error
	err = ainselv1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	// +kubebuilder:scaffold:scheme

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "config", "crd", "bases"),
		},
		ErrorIfCRDPathMissing: true,
	}

	// Retrieve the first found binary directory to allow running tests from IDEs
	if getFirstFoundEnvTestBinaryDir() != "" {
		testEnv.BinaryAssetsDirectory = getFirstFoundEnvTestBinaryDir()
	}

	// cfg is defined in this file globally.
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())
})

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	cancel()
	Eventually(func() error {
		return testEnv.Stop()
	}, time.Minute, time.Second).Should(Succeed())
})

// getFirstFoundEnvTestBinaryDir locates the first binary in the specified path.
// ENVTEST-based tests depend on specific binaries, usually located in paths set by
// controller-runtime. When running tests directly (e.g., via an IDE) without using
// Makefile targets, the 'BinaryAssetsDirectory' must be explicitly configured.
//
// This function streamlines the process by finding the required binaries, similar to
// setting the 'KUBEBUILDER_ASSETS' environment variable. To ensure the binaries are
// properly set up, run 'make setup-envtest' beforehand.
func getFirstFoundEnvTestBinaryDir() string {
	basePath := filepath.Join("..", "..", "bin", "k8s")
	entries, err := os.ReadDir(basePath)
	if err != nil {
		logf.Log.Error(err, "Failed to read directory", "path", basePath)
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return filepath.Join(basePath, entry.Name())
		}
	}
	return ""
}

// Shared fixtures for the envtest specs: every controller spec reconciles an
// Agent named fixtureAgentName backed by the AgentImage fixtureImageName.
const (
	fixtureAgentName = "test-agent"
	fixtureImageName = "test-image"
	fixtureImageURL  = "dpinsel/agent:latest"
)

func findEnvVar(envs []corev1.EnvVar, name string) *corev1.EnvVar {
	for i := range envs {
		if envs[i].Name == name {
			return &envs[i]
		}
	}
	return nil
}

// mustDeployment fetches an agent's Deployment via the suite client, failing
// the spec when it is missing.
func mustDeployment(name string) *appsv1.Deployment {
	deploy := &appsv1.Deployment{}
	ExpectWithOffset(1, k8sClient.Get(ctx, types.NamespacedName{
		Name:      name,
		Namespace: "default",
	}, deploy)).To(Succeed())
	return deploy
}

// piModelsProvider mirrors the slice of pi's models.json layout the operator
// generates, so assertions can check parsed values instead of raw strings.
type piModelsProvider struct {
	API     string `json:"api"`
	BaseURL string `json:"baseUrl"`
	Models  []struct {
		ID            string   `json:"id"`
		ContextWindow int      `json:"contextWindow"`
		Reasoning     bool     `json:"reasoning"`
		Input         []string `json:"input"`
		Cost          *struct {
			Input      float64 `json:"input"`
			Output     float64 `json:"output"`
			CacheRead  float64 `json:"cacheRead"`
			CacheWrite float64 `json:"cacheWrite"`
		} `json:"cost"`
	} `json:"models"`
}

// parsePiModelsJSON decodes the models.json payload produced by
// reconcilePiModelsConfigMap and returns its providers map.
func parsePiModelsJSON(raw string) map[string]piModelsProvider {
	GinkgoHelper()
	var parsed struct {
		Providers map[string]piModelsProvider `json:"providers"`
	}
	Expect(json.Unmarshal([]byte(raw), &parsed)).To(Succeed())
	return parsed.Providers
}

// ensureAgentFixtures creates the AgentImage and Agent fixtures shared by the
// controller specs. It is idempotent so specs may run in any file order.
func ensureAgentFixtures(ctx context.Context) {
	fixtureNN := types.NamespacedName{Name: fixtureAgentName, Namespace: "default"}
	By("creating the AgentImage fixture")
	img := &ainselv1alpha1.AgentImage{}
	err := k8sClient.Get(ctx, types.NamespacedName{Name: fixtureImageName, Namespace: "default"}, img)
	if err != nil && errors.IsNotFound(err) {
		img = &ainselv1alpha1.AgentImage{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fixtureImageName,
				Namespace: "default",
			},
			Spec: ainselv1alpha1.AgentImageSpec{
				DisplayName: "Test Image",
				ImageURL:    fixtureImageURL,
				Tools: []ainselv1alpha1.AgentImageTool{
					{Name: "git", Kind: ainselv1alpha1.AgentImageToolKindContainer},
					{Name: "bash", Kind: ainselv1alpha1.AgentImageToolKindShell},
				},
			},
		}
		Expect(k8sClient.Create(ctx, img)).To(Succeed())
		// Set status.phase = Ready via status subresource
		img.Status.Phase = ainselv1alpha1.AgentImagePhaseReady
		Expect(k8sClient.Status().Update(ctx, img)).To(Succeed())
	}

	By("creating the custom resource for the Kind Agent")
	agent := &ainselv1alpha1.Agent{}
	err = k8sClient.Get(ctx, fixtureNN, agent)
	if err != nil && errors.IsNotFound(err) {
		resource := &ainselv1alpha1.Agent{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fixtureAgentName,
				Namespace: "default",
			},
			Spec: ainselv1alpha1.AgentSpec{
				DisplayName: "Test Agent",
				ImageRef: ainselv1alpha1.AgentImageRef{
					Name: fixtureImageName,
				},
				Runtime: ainselv1alpha1.AgentRuntime{},
				LLM: ainselv1alpha1.AgentLLM{
					Model:    "glm-5.1:cloud",
					Provider: ainselv1alpha1.AgentLLMProviderOllamaCloud,
				},
				Persona: ainselv1alpha1.AgentPersona{
					ID: "01hxtestpersona00000000000",
				},
				EnabledTools: []string{"git", "bash"},
			},
		}
		Expect(k8sClient.Create(ctx, resource)).To(Succeed())
	}
}

// cleanupAgentFixtures deletes the Agent and AgentImage fixtures. Owned
// resources (Deployment, Secrets, ConfigMaps) are not garbage collected in
// envtest and are converged by CreateOrUpdate in later specs.
func cleanupAgentFixtures(ctx context.Context) {
	fixtureNN := types.NamespacedName{Name: fixtureAgentName, Namespace: "default"}
	resource := &ainselv1alpha1.Agent{}
	err := k8sClient.Get(ctx, fixtureNN, resource)
	if err == nil {
		By("Cleanup the specific resource instance Agent")
		Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
	}

	By("Cleanup the AgentImage fixture")
	img := &ainselv1alpha1.AgentImage{}
	err = k8sClient.Get(ctx, types.NamespacedName{Name: fixtureImageName, Namespace: "default"}, img)
	if err == nil {
		Expect(k8sClient.Delete(ctx, img)).To(Succeed())
	}
}

// updateAgentWithRetry fetches the fixture Agent, applies mutate and updates
// it, retrying on conflicts.
func updateAgentWithRetry(ctx context.Context, nn types.NamespacedName, mutate func(*ainselv1alpha1.Agent)) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		agent := &ainselv1alpha1.Agent{}
		if err := k8sClient.Get(ctx, nn, agent); err != nil {
			return err
		}
		mutate(agent)
		return k8sClient.Update(ctx, agent)
	})
}

// updateSkillsWithRetry sets the fixture Agent's skill selection.
func updateSkillsWithRetry(ctx context.Context, nn types.NamespacedName, skills *ainselv1alpha1.AgentSkills) error {
	return updateAgentWithRetry(ctx, nn, func(a *ainselv1alpha1.Agent) { a.Spec.Skills = skills })
}
