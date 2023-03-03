/*
Copyright 2023 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package admissionwebhook

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	clientset "k8s.io/client-go/kubernetes"

	admissionv1 "k8s.io/api/admission/v1"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	genericfeatures "k8s.io/apiserver/pkg/features"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	featuregatetesting "k8s.io/component-base/featuregate/testing"
	apiservertesting "k8s.io/kubernetes/cmd/kube-apiserver/app/testing"
	"k8s.io/kubernetes/test/integration/framework"
)

type admissionRecorder struct {
	mu       sync.Mutex
	upCh     chan struct{}
	upOnce   sync.Once
	requests []*admissionv1.AdmissionRequest
}

func (r *admissionRecorder) Record(req *admissionv1.AdmissionRequest) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, req)
}

func (r *admissionRecorder) MarkerReceived() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.upOnce.Do(func() {
		close(r.upCh)
	})
}

func (r *admissionRecorder) Reset() chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = []*admissionv1.AdmissionRequest{}
	r.upCh = make(chan struct{})
	r.upOnce = sync.Once{}
	return r.upCh
}

func newMatchConditionHandler(recorder *admissionRecorder) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), 400)
		}
		review := admissionv1.AdmissionReview{}
		if err := json.Unmarshal(data, &review); err != nil {
			http.Error(w, err.Error(), 400)
		}

		review.Response = &admissionv1.AdmissionResponse{
			Allowed: true,
			UID:     review.Request.UID,
			Result:  &metav1.Status{Message: "admitted"},
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(review); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}

		switch r.URL.Path {
		case "/marker":
			recorder.MarkerReceived()
			return
		}

		recorder.Record(review.Request)
	})
}

// Test_MatchConditions tests a ValidatingWebhookConfigurations that validates different cases of matchCondition fields
// a webhook is created with failPolicy fail to force all matching conditions to fail
func Test_MatchConditions_ValidatingWebhookConfiguration(t *testing.T) {
	testcases := []struct {
		name            string
		matchConditions []admissionregistrationv1.MatchCondition
		pods            []*corev1.Pod
		matchedPods     []*corev1.Pod
		expectError     bool
	}{
		{
			name: "pods in namespace kube-system is ignored",
			matchConditions: []admissionregistrationv1.MatchCondition{
				{
					Name:       "pods-in-kube-system-exempt.kubernetes.io",
					Expression: "object.metadata.namespace != 'kube-system'",
				},
			},
			pods: []*corev1.Pod{
				matchConditionsTestPod("test1", "kube-system"),
				matchConditionsTestPod("test2", "default"),
			},
			matchedPods: []*corev1.Pod{
				matchConditionsTestPod("test2", "default"),
			},
			expectError: false,
		},
		{
			name: "matchConditions are ANDed together",
			matchConditions: []admissionregistrationv1.MatchCondition{
				{
					Name:       "pods-in-kube-system-exempt.kubernetes.io",
					Expression: "object.metadata.namespace != 'kube-system'",
				},
				{
					Name:       "pods-with-name-test1.kubernetes.io",
					Expression: "object.metadata.name == 'test1'",
				},
			},
			pods: []*corev1.Pod{
				matchConditionsTestPod("test1", "kube-system"),
				matchConditionsTestPod("test1", "default"),
				matchConditionsTestPod("test2", "default"),
			},
			matchedPods: []*corev1.Pod{
				matchConditionsTestPod("test1", "default"),
			},
			expectError: false,
		},
		{
			name: "runtime error evaluating match condition should match all",
			matchConditions: []admissionregistrationv1.MatchCondition{
				{
					Name:       "runtime-error.kubernetes.io",
					Expression: "object.nonExistentProperty == 'someval'",
				},
			},
			pods: []*corev1.Pod{
				matchConditionsTestPod("test1", "kube-system"),
				matchConditionsTestPod("test2", "default"),
			},
			matchedPods: []*corev1.Pod{
				matchConditionsTestPod("test1", "kube-system"),
				matchConditionsTestPod("test2", "default"),
			},
			expectError: false,
		},
		{
			name: "runtime error AND namespace select should still honor namespace select",
			matchConditions: []admissionregistrationv1.MatchCondition{
				{
					Name:       "runtime-error.kubernetes.io",
					Expression: "object.nonExistentProperty == 'someval'",
				},
				{
					Name:       "pods-in-kube-system-exempt.kubernetes.io",
					Expression: "object.metadata.namespace != 'kube-system'",
				},
			},
			pods: []*corev1.Pod{
				matchConditionsTestPod("test1", "kube-system"),
				matchConditionsTestPod("test2", "default"),
			},
			matchedPods: []*corev1.Pod{
				matchConditionsTestPod("test2", "default"),
			},
			expectError: false,
		},
		{
			name: "has access to oldObject",
			matchConditions: []admissionregistrationv1.MatchCondition{
				{
					Name:       "old-object-is-null.kubernetes.io",
					Expression: "oldObject == null",
				},
			},
			pods: []*corev1.Pod{
				matchConditionsTestPod("test2", "default"),
			},
			matchedPods: []*corev1.Pod{
				matchConditionsTestPod("test2", "default"),
			},
			expectError: false,
		},
		{
			name: "invalid field should error",
			matchConditions: []admissionregistrationv1.MatchCondition{
				{
					Name:       "old-object-is-null.kubernetes.io",
					Expression: "imnotafield == null",
				},
			},
			pods:        []*corev1.Pod{},
			matchedPods: []*corev1.Pod{},
			expectError: true,
		},
		{
			name: "missing expression should error",
			matchConditions: []admissionregistrationv1.MatchCondition{
				{
					Name: "old-object-is-null.kubernetes.io",
				},
			},
			pods:        []*corev1.Pod{},
			matchedPods: []*corev1.Pod{},
			expectError: true,
		},
		{
			name: "missing name should error",
			matchConditions: []admissionregistrationv1.MatchCondition{
				{
					Expression: "imnotafield == null",
				},
			},
			pods:        []*corev1.Pod{},
			matchedPods: []*corev1.Pod{},
			expectError: true,
		},
		{
			name: "empty name should error",
			matchConditions: []admissionregistrationv1.MatchCondition{
				{
					Name:       "",
					Expression: "imnotafield == null",
				},
			},
			pods:        []*corev1.Pod{},
			matchedPods: []*corev1.Pod{},
			expectError: true,
		},
		{
			name: "empty expression should error",
			matchConditions: []admissionregistrationv1.MatchCondition{
				{
					Name:       "test-empty-expression.kubernetes.io",
					Expression: "",
				},
			},
			pods:        []*corev1.Pod{},
			matchedPods: []*corev1.Pod{},
			expectError: true,
		},
	}

	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(localhostCert) {
		t.Fatal("Failed to append Cert from PEM")
	}
	cert, err := tls.X509KeyPair(localhostCert, localhostKey)
	if err != nil {
		t.Fatalf("Failed to build cert with error: %+v", err)
	}

	recorder := &admissionRecorder{requests: []*admissionv1.AdmissionRequest{}}

	webhookServer := httptest.NewUnstartedServer(newMatchConditionHandler(recorder))
	webhookServer.TLS = &tls.Config{
		RootCAs:      roots,
		Certificates: []tls.Certificate{cert},
	}
	webhookServer.StartTLS()
	defer webhookServer.Close()

	for _, testcase := range testcases {
		t.Run(testcase.name, func(t *testing.T) {
			upCh := recorder.Reset()
			defer featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, genericfeatures.AdmissionWebhookMatchConditions, true)()

			server, err := apiservertesting.StartTestServer(t, nil, []string{
				"--disable-admission-plugins=ServiceAccount",
			}, framework.SharedEtcd())
			if err != nil {
				t.Fatal(err)
			}
			defer server.TearDownFn()

			config := server.ClientConfig

			client, err := clientset.NewForConfig(config)
			if err != nil {
				t.Fatal(err)
			}

			// Write markers to a separate namespace to avoid cross-talk
			markerNs := "marker"
			_, err = client.CoreV1().Namespaces().Create(context.TODO(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: markerNs}}, metav1.CreateOptions{})
			if err != nil {
				t.Fatal(err)
			}

			// Create a marker object to use to check for the webhook configurations to be ready.
			marker, err := client.CoreV1().Pods(markerNs).Create(context.TODO(), newMarkerPod(markerNs), metav1.CreateOptions{})
			if err != nil {
				t.Fatal(err)
			}

			fail := admissionregistrationv1.Fail
			endpoint := webhookServer.URL
			markerEndpoint := webhookServer.URL + "/marker"
			validatingwebhook := &admissionregistrationv1.ValidatingWebhookConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name: "admission.integration.test",
				},
				Webhooks: []admissionregistrationv1.ValidatingWebhook{
					{
						Name: "admission.integration.test",
						Rules: []admissionregistrationv1.RuleWithOperations{{
							Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.OperationAll},
							Rule: admissionregistrationv1.Rule{
								APIGroups:   []string{""},
								APIVersions: []string{"v1"},
								Resources:   []string{"pods"},
							},
						}},
						ClientConfig: admissionregistrationv1.WebhookClientConfig{
							URL:      &endpoint,
							CABundle: localhostCert,
						},
						// ignore pods in the marker namespace
						NamespaceSelector: &metav1.LabelSelector{
							MatchExpressions: []metav1.LabelSelectorRequirement{
								{
									Key:      corev1.LabelMetadataName,
									Operator: metav1.LabelSelectorOpNotIn,
									Values:   []string{"marker"},
								},
							}},
						FailurePolicy:           &fail,
						SideEffects:             &noSideEffects,
						AdmissionReviewVersions: []string{"v1"},
						MatchConditions:         testcase.matchConditions,
					},
					{
						Name: "admission.integration.test.marker",
						Rules: []admissionregistrationv1.RuleWithOperations{{
							Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.OperationAll},
							Rule:       admissionregistrationv1.Rule{APIGroups: []string{""}, APIVersions: []string{"v1"}, Resources: []string{"pods"}},
						}},
						ClientConfig: admissionregistrationv1.WebhookClientConfig{
							URL:      &markerEndpoint,
							CABundle: localhostCert,
						},
						NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{
							corev1.LabelMetadataName: "marker",
						}},
						ObjectSelector:          &metav1.LabelSelector{MatchLabels: map[string]string{"marker": "true"}},
						FailurePolicy:           &fail,
						SideEffects:             &noSideEffects,
						AdmissionReviewVersions: []string{"v1"},
					},
				},
			}

			cfg, err := client.AdmissionregistrationV1().ValidatingWebhookConfigurations().Create(context.TODO(), validatingwebhook, metav1.CreateOptions{})
			if testcase.expectError == false && err != nil {
				t.Fatal(err)
			} else if testcase.expectError == true {
				if err == nil {
					t.Error("expected error creating ValidatingWebhookConfigurations")
				}
				return
			}
			defer func() {
				err := client.AdmissionregistrationV1().ValidatingWebhookConfigurations().Delete(context.TODO(), cfg.GetName(), metav1.DeleteOptions{})
				if err != nil {
					t.Fatal(err)
				}
			}()

			// wait until new webhook is called the first time
			if err := wait.PollImmediate(time.Millisecond*5, wait.ForeverTestTimeout, func() (bool, error) {
				_, err = client.CoreV1().Pods(markerNs).Patch(context.TODO(), marker.Name, types.JSONPatchType, []byte("[]"), metav1.PatchOptions{})
				select {
				case <-upCh:
					return true, nil
				default:
					t.Logf("Waiting for webhook to become effective, getting marker object: %v", err)
					return false, nil
				}
			}); err != nil {
				t.Fatal(err)
			}

			for _, pod := range testcase.pods {
				_, err := client.CoreV1().Pods(pod.Namespace).Create(context.TODO(), pod, metav1.CreateOptions{})
				if err != nil {
					t.Fatalf("unexpected error creating test pod: %v", err)
				}
			}

			if len(recorder.requests) != len(testcase.matchedPods) {
				t.Errorf("unexpected requests %v, expected %v", recorder.requests, testcase.matchedPods)
			}

			for i, request := range recorder.requests {
				if request.Name != testcase.matchedPods[i].Name {
					t.Errorf("unexpected pod name %v, expected %v", request.Name, testcase.matchedPods[i].Name)
				}
				if request.Namespace != testcase.matchedPods[i].Namespace {
					t.Errorf("unexpected pod namespace %v, expected %v", request.Namespace, testcase.matchedPods[i].Namespace)
				}
			}
		})
	}
}

func matchConditionsTestPod(name, ns string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "test",
					Image: "test",
				},
			},
		},
	}
}

func newMarkerPod(namespace string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      "marker",
			Labels: map[string]string{
				"marker": "true",
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:  "fake-name",
				Image: "fakeimage",
			}},
		},
	}
}
