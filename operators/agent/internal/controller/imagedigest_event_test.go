package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ainselv1alpha1 "github.com/ainsel-project/ainsel/shared/api/api/v1alpha1"
)

// digestFailureFixtures builds a reconciler whose image points at a registry
// that answers 401 to the anonymous manifest request — which is what every
// registry outside Docker Hub does today, since resolveImageDigest only
// authenticates anonymously. Recorder is left nil when recorder is nil.
//
// The returned image reference is the one on the AgentImage: passing
// imageObject()'s digest-pinned default instead would skip the registry
// entirely, because a pinned ref already carries its digest.
func digestFailureFixtures(t *testing.T, recorder *record.FakeRecorder) (*AgentReconciler, *ainselv1alpha1.Agent, *ainselv1alpha1.AgentImage) {
	t.Helper()
	withTestClient(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	img := imageObject()
	img.Spec.ImageURL = strings.TrimPrefix(srv.URL, "http://") + "/ainsel/pi:dev"
	agent := scalingAgent(nil)

	scheme := scalingFixtures()
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(img, agent).
		WithStatusSubresource(&ainselv1alpha1.Agent{}).
		Build()

	r := &AgentReconciler{Client: c, Scheme: scheme}
	if recorder != nil {
		r.Recorder = recorder
	}
	return r, agent, img
}

// Reconciliation must survive an unresolvable digest — but the lost restart
// trigger has to be visible somewhere, hence the Warning event.
func TestReconcileDeployment_DigestFailureEmitsWarningEvent(t *testing.T) {
	recorder := record.NewFakeRecorder(10)
	r, agent, img := digestFailureFixtures(t, recorder)

	deploy, _, err := r.reconcileDeployment(context.Background(), agent, "agent-worker", img, img.Spec.ImageURL)
	if err != nil {
		t.Fatalf("reconcileDeployment must not fail on an unresolvable digest: %v", err)
	}
	if _, ok := deploy.Spec.Template.Annotations[imageDigestAnnotation]; ok {
		t.Errorf("%s must be skipped when the digest cannot be resolved", imageDigestAnnotation)
	}

	select {
	case ev := <-recorder.Events:
		if !strings.Contains(ev, "Warning") || !strings.Contains(ev, "ImageDigestResolutionFailed") {
			t.Errorf("expected a Warning ImageDigestResolutionFailed event, got %q", ev)
		}
		if !strings.Contains(ev, img.Spec.ImageURL) {
			t.Errorf("event should name the image %q, got %q", img.Spec.ImageURL, ev)
		}
	default:
		t.Fatal("expected an ImageDigestResolutionFailed event, got none")
	}
}

// The nil-Recorder guard matters for callers that build a bare reconciler (tests,
// out-of-cluster runs): a missing recorder must not panic the reconcile.
func TestReconcileDeployment_DigestFailureWithoutRecorder(t *testing.T) {
	r, agent, img := digestFailureFixtures(t, nil)

	if _, _, err := r.reconcileDeployment(context.Background(), agent, "agent-worker", img, img.Spec.ImageURL); err != nil {
		t.Fatalf("reconcileDeployment with a nil Recorder: %v", err)
	}
}
