/*
Copyright 2019 The Kubernetes Authors.

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

package matching

import (
	"fmt"
	"strings"
	"testing"

	"k8s.io/api/admissionregistration/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/admission/plugin/webhook/predicates/namespace"
	"k8s.io/apiserver/pkg/admission/plugin/webhook/predicates/object"
)

var _ MatchCriteria = &fakeCriteria{}

type fakeCriteria struct {
	matchResources v1alpha1.MatchResources
}

func (fc *fakeCriteria) GetMatchResources() v1alpha1.MatchResources {
	return fc.matchResources
}

func (fc *fakeCriteria) GetParsedNamespaceSelector() (labels.Selector, error) {
	return metav1.LabelSelectorAsSelector(fc.matchResources.NamespaceSelector)
}

func (fc *fakeCriteria) GetParsedObjectSelector() (labels.Selector, error) {
	return metav1.LabelSelectorAsSelector(fc.matchResources.ObjectSelector)
}

func TestMatcher(t *testing.T) {
	a := &Matcher{namespaceMatcher: &namespace.Matcher{}, objectMatcher: &object.Matcher{}}

	allScopes := v1alpha1.AllScopes
	exactMatch := v1alpha1.Exact
	equivalentMatch := v1alpha1.Equivalent

	mapper := runtime.NewEquivalentResourceRegistryWithIdentity(func(resource schema.GroupResource) string {
		if resource.Resource == "deployments" {
			// co-locate deployments in all API groups
			return "/deployments"
		}
		return ""
	})
	mapper.RegisterKindFor(schema.GroupVersionResource{"extensions", "v1beta1", "deployments"}, "", schema.GroupVersionKind{"extensions", "v1beta1", "Deployment"})
	mapper.RegisterKindFor(schema.GroupVersionResource{"apps", "v1", "deployments"}, "", schema.GroupVersionKind{"apps", "v1", "Deployment"})
	mapper.RegisterKindFor(schema.GroupVersionResource{"apps", "v1beta1", "deployments"}, "", schema.GroupVersionKind{"apps", "v1beta1", "Deployment"})
	mapper.RegisterKindFor(schema.GroupVersionResource{"apps", "v1alpha1", "deployments"}, "", schema.GroupVersionKind{"apps", "v1alpha1", "Deployment"})

	mapper.RegisterKindFor(schema.GroupVersionResource{"extensions", "v1beta1", "deployments"}, "scale", schema.GroupVersionKind{"extensions", "v1beta1", "Scale"})
	mapper.RegisterKindFor(schema.GroupVersionResource{"apps", "v1", "deployments"}, "scale", schema.GroupVersionKind{"autoscaling", "v1", "Scale"})
	mapper.RegisterKindFor(schema.GroupVersionResource{"apps", "v1beta1", "deployments"}, "scale", schema.GroupVersionKind{"apps", "v1beta1", "Scale"})
	mapper.RegisterKindFor(schema.GroupVersionResource{"apps", "v1alpha1", "deployments"}, "scale", schema.GroupVersionKind{"apps", "v1alpha1", "Scale"})

	// register invalid kinds to trigger an error
	mapper.RegisterKindFor(schema.GroupVersionResource{"example.com", "v1", "widgets"}, "", schema.GroupVersionKind{"", "", ""})
	mapper.RegisterKindFor(schema.GroupVersionResource{"example.com", "v2", "widgets"}, "", schema.GroupVersionKind{"", "", ""})

	interfaces := &admission.RuntimeObjectInterfaces{EquivalentResourceMapper: mapper}

	// TODO write test cases for name matching
	testcases := []struct {
		name string

		criteria *v1alpha1.MatchResources
		attrs    admission.Attributes

		expectMatches bool
		expectErr     string
	}{
		{
			name:          "no rules (just write)",
			criteria:      &v1alpha1.MatchResources{NamespaceSelector: &metav1.LabelSelector{}, ResourceRules: []v1alpha1.NamedRuleWithOperations{}},
			attrs:         admission.NewAttributesRecord(nil, nil, schema.GroupVersionKind{"apps", "v1", "Deployment"}, "ns", "name", schema.GroupVersionResource{"apps", "v1", "deployments"}, "", admission.Create, &metav1.CreateOptions{}, false, nil),
			expectMatches: false,
		},
		{
			name: "wildcard rule, match as requested",
			criteria: &v1alpha1.MatchResources{
				NamespaceSelector: &metav1.LabelSelector{},
				ObjectSelector:    &metav1.LabelSelector{},
				ResourceRules: []v1alpha1.NamedRuleWithOperations{{
					RuleWithOperations: v1alpha1.RuleWithOperations{
						Operations: []v1alpha1.OperationType{"*"},
						Rule:       v1alpha1.Rule{APIGroups: []string{"*"}, APIVersions: []string{"*"}, Resources: []string{"*"}, Scope: &allScopes},
					},
				}}},
			attrs:         admission.NewAttributesRecord(nil, nil, schema.GroupVersionKind{"apps", "v1", "Deployment"}, "ns", "name", schema.GroupVersionResource{"apps", "v1", "deployments"}, "", admission.Create, &metav1.CreateOptions{}, false, nil),
			expectMatches: true,
		},
		{
			name: "specific rules, prefer exact match",
			criteria: &v1alpha1.MatchResources{
				NamespaceSelector: &metav1.LabelSelector{},
				ObjectSelector:    &metav1.LabelSelector{},
				ResourceRules: []v1alpha1.NamedRuleWithOperations{{
					RuleWithOperations: v1alpha1.RuleWithOperations{
						Operations: []v1alpha1.OperationType{"*"},
						Rule:       v1alpha1.Rule{APIGroups: []string{"extensions"}, APIVersions: []string{"v1beta1"}, Resources: []string{"deployments"}, Scope: &allScopes},
					},
				}, {
					RuleWithOperations: v1alpha1.RuleWithOperations{
						Operations: []v1alpha1.OperationType{"*"},
						Rule:       v1alpha1.Rule{APIGroups: []string{"apps"}, APIVersions: []string{"v1beta1"}, Resources: []string{"deployments"}, Scope: &allScopes},
					},
				}, {
					RuleWithOperations: v1alpha1.RuleWithOperations{
						Operations: []v1alpha1.OperationType{"*"},
						Rule:       v1alpha1.Rule{APIGroups: []string{"apps"}, APIVersions: []string{"v1"}, Resources: []string{"deployments"}, Scope: &allScopes},
					},
				}}},
			attrs:         admission.NewAttributesRecord(nil, nil, schema.GroupVersionKind{"apps", "v1", "Deployment"}, "ns", "name", schema.GroupVersionResource{"apps", "v1", "deployments"}, "", admission.Create, &metav1.CreateOptions{}, false, nil),
			expectMatches: true,
		},
		// TODO The webhook API and the behavior don't match. According to the webhook, MatchPolicy defaults to "Equivalent": https://github.com/kubernetes/kubernetes/blob/a0b69ecd01edc68f9eb88658edcb9f82daf27883/staging/src/k8s.io/api/admissionregistration/v1/types.go#L206-L221
		// but the code is written such that nil => exact match: https://github.com/kubernetes/kubernetes/blob/a0b69ecd01edc68f9eb88658edcb9f82daf27883/staging/src/k8s.io/apiserver/pkg/admission/plugin/webhook/generic/webhook.go#L169-L198
		// Should I follow the API as-documented or the behavior of the webhook code as-implemented?

		{
			name: "specific rules, match miss",
			criteria: &v1alpha1.MatchResources{
				NamespaceSelector: &metav1.LabelSelector{},
				ObjectSelector:    &metav1.LabelSelector{},
				ResourceRules: []v1alpha1.NamedRuleWithOperations{{
					RuleWithOperations: v1alpha1.RuleWithOperations{
						Operations: []v1alpha1.OperationType{"*"},
						Rule:       v1alpha1.Rule{APIGroups: []string{"extensions"}, APIVersions: []string{"v1beta1"}, Resources: []string{"deployments"}, Scope: &allScopes},
					},
				}, {
					RuleWithOperations: v1alpha1.RuleWithOperations{
						Operations: []v1alpha1.OperationType{"*"},
						Rule:       v1alpha1.Rule{APIGroups: []string{"apps"}, APIVersions: []string{"v1beta1"}, Resources: []string{"deployments"}, Scope: &allScopes},
					},
				}}},
			attrs:         admission.NewAttributesRecord(nil, nil, schema.GroupVersionKind{"apps", "v1", "Deployment"}, "ns", "name", schema.GroupVersionResource{"apps", "v1", "deployments"}, "", admission.Create, &metav1.CreateOptions{}, false, nil),
			expectMatches: false,
		},
		{
			name: "specific rules, exact match miss",
			criteria: &v1alpha1.MatchResources{
				MatchPolicy:       &exactMatch,
				NamespaceSelector: &metav1.LabelSelector{},
				ObjectSelector:    &metav1.LabelSelector{},
				ResourceRules: []v1alpha1.NamedRuleWithOperations{{
					RuleWithOperations: v1alpha1.RuleWithOperations{
						Operations: []v1alpha1.OperationType{"*"},
						Rule:       v1alpha1.Rule{APIGroups: []string{"extensions"}, APIVersions: []string{"v1beta1"}, Resources: []string{"deployments"}, Scope: &allScopes},
					},
				}, {
					RuleWithOperations: v1alpha1.RuleWithOperations{
						Operations: []v1alpha1.OperationType{"*"},
						Rule:       v1alpha1.Rule{APIGroups: []string{"apps"}, APIVersions: []string{"v1beta1"}, Resources: []string{"deployments"}, Scope: &allScopes},
					},
				}}},
			attrs:         admission.NewAttributesRecord(nil, nil, schema.GroupVersionKind{"apps", "v1", "Deployment"}, "ns", "name", schema.GroupVersionResource{"apps", "v1", "deployments"}, "", admission.Create, &metav1.CreateOptions{}, false, nil),
			expectMatches: false,
		},
		{
			name: "specific rules, equivalent match, prefer extensions",
			criteria: &v1alpha1.MatchResources{
				MatchPolicy:       &equivalentMatch,
				NamespaceSelector: &metav1.LabelSelector{},
				ObjectSelector:    &metav1.LabelSelector{},
				ResourceRules: []v1alpha1.NamedRuleWithOperations{{
					RuleWithOperations: v1alpha1.RuleWithOperations{
						Operations: []v1alpha1.OperationType{"*"},
						Rule:       v1alpha1.Rule{APIGroups: []string{"extensions"}, APIVersions: []string{"v1beta1"}, Resources: []string{"deployments"}, Scope: &allScopes},
					},
				}, {
					RuleWithOperations: v1alpha1.RuleWithOperations{
						Operations: []v1alpha1.OperationType{"*"},
						Rule:       v1alpha1.Rule{APIGroups: []string{"apps"}, APIVersions: []string{"v1beta1"}, Resources: []string{"deployments"}, Scope: &allScopes},
					},
				}}},
			attrs:         admission.NewAttributesRecord(nil, nil, schema.GroupVersionKind{"apps", "v1", "Deployment"}, "ns", "name", schema.GroupVersionResource{"apps", "v1", "deployments"}, "", admission.Create, &metav1.CreateOptions{}, false, nil),
			expectMatches: true,
		},
		{
			name: "specific rules, equivalent match, prefer apps",
			criteria: &v1alpha1.MatchResources{
				MatchPolicy:       &equivalentMatch,
				NamespaceSelector: &metav1.LabelSelector{},
				ObjectSelector:    &metav1.LabelSelector{},
				ResourceRules: []v1alpha1.NamedRuleWithOperations{{
					RuleWithOperations: v1alpha1.RuleWithOperations{
						Operations: []v1alpha1.OperationType{"*"},
						Rule:       v1alpha1.Rule{APIGroups: []string{"apps"}, APIVersions: []string{"v1beta1"}, Resources: []string{"deployments"}, Scope: &allScopes},
					},
				}, {
					RuleWithOperations: v1alpha1.RuleWithOperations{
						Operations: []v1alpha1.OperationType{"*"},
						Rule:       v1alpha1.Rule{APIGroups: []string{"extensions"}, APIVersions: []string{"v1beta1"}, Resources: []string{"deployments"}, Scope: &allScopes},
					},
				}}},
			attrs:         admission.NewAttributesRecord(nil, nil, schema.GroupVersionKind{"apps", "v1", "Deployment"}, "ns", "name", schema.GroupVersionResource{"apps", "v1", "deployments"}, "", admission.Create, &metav1.CreateOptions{}, false, nil),
			expectMatches: true,
		},

		{
			name: "specific rules, subresource prefer exact match",
			criteria: &v1alpha1.MatchResources{
				NamespaceSelector: &metav1.LabelSelector{},
				ObjectSelector:    &metav1.LabelSelector{},
				ResourceRules: []v1alpha1.NamedRuleWithOperations{{
					RuleWithOperations: v1alpha1.RuleWithOperations{
						Operations: []v1alpha1.OperationType{"*"},
						Rule:       v1alpha1.Rule{APIGroups: []string{"extensions"}, APIVersions: []string{"v1beta1"}, Resources: []string{"deployments", "deployments/scale"}, Scope: &allScopes},
					},
				}, {
					RuleWithOperations: v1alpha1.RuleWithOperations{
						Operations: []v1alpha1.OperationType{"*"},
						Rule:       v1alpha1.Rule{APIGroups: []string{"apps"}, APIVersions: []string{"v1beta1"}, Resources: []string{"deployments", "deployments/scale"}, Scope: &allScopes},
					},
				}, {
					RuleWithOperations: v1alpha1.RuleWithOperations{
						Operations: []v1alpha1.OperationType{"*"},
						Rule:       v1alpha1.Rule{APIGroups: []string{"apps"}, APIVersions: []string{"v1"}, Resources: []string{"deployments", "deployments/scale"}, Scope: &allScopes},
					},
				}}},
			attrs:         admission.NewAttributesRecord(nil, nil, schema.GroupVersionKind{"autoscaling", "v1", "Scale"}, "ns", "name", schema.GroupVersionResource{"apps", "v1", "deployments"}, "scale", admission.Create, &metav1.CreateOptions{}, false, nil),
			expectMatches: true,
		},
		{
			name: "specific rules, subresource match miss",
			criteria: &v1alpha1.MatchResources{
				NamespaceSelector: &metav1.LabelSelector{},
				ObjectSelector:    &metav1.LabelSelector{},
				ResourceRules: []v1alpha1.NamedRuleWithOperations{{
					RuleWithOperations: v1alpha1.RuleWithOperations{
						Operations: []v1alpha1.OperationType{"*"},
						Rule:       v1alpha1.Rule{APIGroups: []string{"extensions"}, APIVersions: []string{"v1beta1"}, Resources: []string{"deployments", "deployments/scale"}, Scope: &allScopes},
					},
				}, {
					RuleWithOperations: v1alpha1.RuleWithOperations{
						Operations: []v1alpha1.OperationType{"*"},
						Rule:       v1alpha1.Rule{APIGroups: []string{"apps"}, APIVersions: []string{"v1beta1"}, Resources: []string{"deployments", "deployments/scale"}, Scope: &allScopes},
					},
				}}},
			attrs:         admission.NewAttributesRecord(nil, nil, schema.GroupVersionKind{"autoscaling", "v1", "Scale"}, "ns", "name", schema.GroupVersionResource{"apps", "v1", "deployments"}, "scale", admission.Create, &metav1.CreateOptions{}, false, nil),
			expectMatches: false,
		},
		{
			name: "specific rules, subresource exact match miss",
			criteria: &v1alpha1.MatchResources{
				MatchPolicy:       &exactMatch,
				NamespaceSelector: &metav1.LabelSelector{},
				ObjectSelector:    &metav1.LabelSelector{},
				ResourceRules: []v1alpha1.NamedRuleWithOperations{{
					RuleWithOperations: v1alpha1.RuleWithOperations{
						Operations: []v1alpha1.OperationType{"*"},
						Rule:       v1alpha1.Rule{APIGroups: []string{"extensions"}, APIVersions: []string{"v1beta1"}, Resources: []string{"deployments", "deployments/scale"}, Scope: &allScopes},
					},
				}, {
					RuleWithOperations: v1alpha1.RuleWithOperations{
						Operations: []v1alpha1.OperationType{"*"},
						Rule:       v1alpha1.Rule{APIGroups: []string{"apps"}, APIVersions: []string{"v1beta1"}, Resources: []string{"deployments", "deployments/scale"}, Scope: &allScopes},
					},
				}}},
			attrs:         admission.NewAttributesRecord(nil, nil, schema.GroupVersionKind{"autoscaling", "v1", "Scale"}, "ns", "name", schema.GroupVersionResource{"apps", "v1", "deployments"}, "scale", admission.Create, &metav1.CreateOptions{}, false, nil),
			expectMatches: false,
		},
		{
			name: "specific rules, subresource equivalent match, prefer extensions",
			criteria: &v1alpha1.MatchResources{
				MatchPolicy:       &equivalentMatch,
				NamespaceSelector: &metav1.LabelSelector{},
				ObjectSelector:    &metav1.LabelSelector{},
				ResourceRules: []v1alpha1.NamedRuleWithOperations{{
					RuleWithOperations: v1alpha1.RuleWithOperations{
						Operations: []v1alpha1.OperationType{"*"},
						Rule:       v1alpha1.Rule{APIGroups: []string{"extensions"}, APIVersions: []string{"v1beta1"}, Resources: []string{"deployments", "deployments/scale"}, Scope: &allScopes},
					},
				}, {
					RuleWithOperations: v1alpha1.RuleWithOperations{
						Operations: []v1alpha1.OperationType{"*"},
						Rule:       v1alpha1.Rule{APIGroups: []string{"apps"}, APIVersions: []string{"v1beta1"}, Resources: []string{"deployments", "deployments/scale"}, Scope: &allScopes},
					},
				}}},
			attrs:         admission.NewAttributesRecord(nil, nil, schema.GroupVersionKind{"autoscaling", "v1", "Scale"}, "ns", "name", schema.GroupVersionResource{"apps", "v1", "deployments"}, "scale", admission.Create, &metav1.CreateOptions{}, false, nil),
			expectMatches: true,
		},
		{
			name: "specific rules, subresource equivalent match, prefer apps",
			criteria: &v1alpha1.MatchResources{
				MatchPolicy:       &equivalentMatch,
				NamespaceSelector: &metav1.LabelSelector{},
				ObjectSelector:    &metav1.LabelSelector{},
				ResourceRules: []v1alpha1.NamedRuleWithOperations{{
					RuleWithOperations: v1alpha1.RuleWithOperations{
						Operations: []v1alpha1.OperationType{"*"},
						Rule:       v1alpha1.Rule{APIGroups: []string{"apps"}, APIVersions: []string{"v1beta1"}, Resources: []string{"deployments", "deployments/scale"}, Scope: &allScopes},
					},
				}, {
					RuleWithOperations: v1alpha1.RuleWithOperations{
						Operations: []v1alpha1.OperationType{"*"},
						Rule:       v1alpha1.Rule{APIGroups: []string{"extensions"}, APIVersions: []string{"v1beta1"}, Resources: []string{"deployments", "deployments/scale"}, Scope: &allScopes},
					},
				}}},
			attrs:         admission.NewAttributesRecord(nil, nil, schema.GroupVersionKind{"autoscaling", "v1", "Scale"}, "ns", "name", schema.GroupVersionResource{"apps", "v1", "deployments"}, "scale", admission.Create, &metav1.CreateOptions{}, false, nil),
			expectMatches: true,
		},
	}

	for _, testcase := range testcases {
		t.Run(testcase.name, func(t *testing.T) {
			matches, err := a.Matches(&fakeCriteria{matchResources: *testcase.criteria}, testcase.attrs, interfaces)
			if err != nil {
				if len(testcase.expectErr) == 0 {
					t.Fatal(err)
				}
				if !strings.Contains(err.Error(), testcase.expectErr) {
					t.Fatalf("expected error containing %q, got %s", testcase.expectErr, err.Error())
				}
				return
			} else if len(testcase.expectErr) > 0 {
				t.Fatalf("expected error %q, got no error", testcase.expectErr)
			}

			if matches != testcase.expectMatches {
				t.Fatalf("expected matches = %v; got %v", testcase.expectMatches, matches)
			}
		})
	}
}

type fakeNamespaceLister struct {
	namespaces map[string]*corev1.Namespace
}

func (f fakeNamespaceLister) List(selector labels.Selector) (ret []*corev1.Namespace, err error) {
	return nil, nil
}
func (f fakeNamespaceLister) Get(name string) (*corev1.Namespace, error) {
	ns, ok := f.namespaces[name]
	if ok {
		return ns, nil
	}
	return nil, errors.NewNotFound(corev1.Resource("namespaces"), name)
}

func BenchmarkMatcher(b *testing.B) {
	allScopes := v1alpha1.AllScopes
	equivalentMatch := v1alpha1.Equivalent

	namespace1Labels := map[string]string{"ns": "ns1"}
	namespace1 := corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "ns1",
			Labels: namespace1Labels,
		},
	}
	namespaceLister := fakeNamespaceLister{map[string]*corev1.Namespace{"ns": &namespace1}}

	mapper := runtime.NewEquivalentResourceRegistryWithIdentity(func(resource schema.GroupResource) string {
		if resource.Resource == "deployments" {
			// co-locate deployments in all API groups
			return "/deployments"
		}
		return ""
	})
	mapper.RegisterKindFor(schema.GroupVersionResource{"extensions", "v1beta1", "deployments"}, "", schema.GroupVersionKind{"extensions", "v1beta1", "Deployment"})
	mapper.RegisterKindFor(schema.GroupVersionResource{"apps", "v1", "deployments"}, "", schema.GroupVersionKind{"apps", "v1", "Deployment"})
	mapper.RegisterKindFor(schema.GroupVersionResource{"apps", "v1beta1", "deployments"}, "", schema.GroupVersionKind{"apps", "v1beta1", "Deployment"})
	mapper.RegisterKindFor(schema.GroupVersionResource{"apps", "v1alpha1", "deployments"}, "", schema.GroupVersionKind{"apps", "v1alpha1", "Deployment"})

	mapper.RegisterKindFor(schema.GroupVersionResource{"extensions", "v1beta1", "deployments"}, "scale", schema.GroupVersionKind{"extensions", "v1beta1", "Scale"})
	mapper.RegisterKindFor(schema.GroupVersionResource{"apps", "v1", "deployments"}, "scale", schema.GroupVersionKind{"autoscaling", "v1", "Scale"})
	mapper.RegisterKindFor(schema.GroupVersionResource{"apps", "v1beta1", "deployments"}, "scale", schema.GroupVersionKind{"apps", "v1beta1", "Scale"})
	mapper.RegisterKindFor(schema.GroupVersionResource{"apps", "v1alpha1", "deployments"}, "scale", schema.GroupVersionKind{"apps", "v1alpha1", "Scale"})

	mapper.RegisterKindFor(schema.GroupVersionResource{"apps", "v1", "statefulset"}, "", schema.GroupVersionKind{"apps", "v1", "StatefulSet"})
	mapper.RegisterKindFor(schema.GroupVersionResource{"apps", "v1beta1", "statefulset"}, "", schema.GroupVersionKind{"apps", "v1beta1", "StatefulSet"})
	mapper.RegisterKindFor(schema.GroupVersionResource{"apps", "v1beta2", "statefulset"}, "", schema.GroupVersionKind{"apps", "v1beta2", "StatefulSet"})

	mapper.RegisterKindFor(schema.GroupVersionResource{"apps", "v1", "statefulset"}, "scale", schema.GroupVersionKind{"apps", "v1", "Scale"})
	mapper.RegisterKindFor(schema.GroupVersionResource{"apps", "v1beta1", "statefulset"}, "scale", schema.GroupVersionKind{"apps", "v1beta1", "Scale"})
	mapper.RegisterKindFor(schema.GroupVersionResource{"apps", "v1alpha2", "statefulset"}, "scale", schema.GroupVersionKind{"apps", "v1beta2", "Scale"})

	nsSelector := make(map[string]string)
	for i := 0; i < 100; i++ {
		nsSelector[fmt.Sprintf("key-%d", i)] = fmt.Sprintf("val-%d", i)
	}

	mr := v1alpha1.MatchResources{
		MatchPolicy:       &equivalentMatch,
		NamespaceSelector: &metav1.LabelSelector{MatchLabels: nsSelector},
		ObjectSelector:    &metav1.LabelSelector{},
		ResourceRules: []v1alpha1.NamedRuleWithOperations{
			{
				RuleWithOperations: v1alpha1.RuleWithOperations{
					Operations: []v1alpha1.OperationType{"*"},
					Rule:       v1alpha1.Rule{APIGroups: []string{"apps"}, APIVersions: []string{"v1beta1"}, Resources: []string{"deployments", "deployments/scale"}, Scope: &allScopes},
				},
			},
			{
				RuleWithOperations: v1alpha1.RuleWithOperations{
					Operations: []v1alpha1.OperationType{"*"},
					Rule:       v1alpha1.Rule{APIGroups: []string{"extensions"}, APIVersions: []string{"v1beta1"}, Resources: []string{"deployments", "deployments/scale"}, Scope: &allScopes},
				},
			},
		},
	}

	criteria := &fakeCriteria{matchResources: mr}
	attrs := admission.NewAttributesRecord(nil, nil, schema.GroupVersionKind{"autoscaling", "v1", "Scale"}, "ns", "name", schema.GroupVersionResource{"apps", "v1", "deployments"}, "scale", admission.Create, &metav1.CreateOptions{}, false, nil)
	interfaces := &admission.RuntimeObjectInterfaces{EquivalentResourceMapper: mapper}
	matcher := &Matcher{namespaceMatcher: &namespace.Matcher{NamespaceLister: namespaceLister}, objectMatcher: &object.Matcher{}}

	for i := 0; i < b.N; i++ {
		matcher.Matches(criteria, attrs, interfaces)
	}
}

func BenchmarkShouldCallHookWithComplexRule(b *testing.B) {
	allScopes := v1alpha1.AllScopes
	equivalentMatch := v1alpha1.Equivalent

	namespace1Labels := map[string]string{"ns": "ns1"}
	namespace1 := corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "ns1",
			Labels: namespace1Labels,
		},
	}
	namespaceLister := fakeNamespaceLister{map[string]*corev1.Namespace{"ns": &namespace1}}

	mapper := runtime.NewEquivalentResourceRegistryWithIdentity(func(resource schema.GroupResource) string {
		if resource.Resource == "deployments" {
			// co-locate deployments in all API groups
			return "/deployments"
		}
		return ""
	})
	mapper.RegisterKindFor(schema.GroupVersionResource{"extensions", "v1beta1", "deployments"}, "", schema.GroupVersionKind{"extensions", "v1beta1", "Deployment"})
	mapper.RegisterKindFor(schema.GroupVersionResource{"apps", "v1", "deployments"}, "", schema.GroupVersionKind{"apps", "v1", "Deployment"})
	mapper.RegisterKindFor(schema.GroupVersionResource{"apps", "v1beta1", "deployments"}, "", schema.GroupVersionKind{"apps", "v1beta1", "Deployment"})
	mapper.RegisterKindFor(schema.GroupVersionResource{"apps", "v1alpha1", "deployments"}, "", schema.GroupVersionKind{"apps", "v1alpha1", "Deployment"})

	mapper.RegisterKindFor(schema.GroupVersionResource{"extensions", "v1beta1", "deployments"}, "scale", schema.GroupVersionKind{"extensions", "v1beta1", "Scale"})
	mapper.RegisterKindFor(schema.GroupVersionResource{"apps", "v1", "deployments"}, "scale", schema.GroupVersionKind{"autoscaling", "v1", "Scale"})
	mapper.RegisterKindFor(schema.GroupVersionResource{"apps", "v1beta1", "deployments"}, "scale", schema.GroupVersionKind{"apps", "v1beta1", "Scale"})
	mapper.RegisterKindFor(schema.GroupVersionResource{"apps", "v1alpha1", "deployments"}, "scale", schema.GroupVersionKind{"apps", "v1alpha1", "Scale"})

	mapper.RegisterKindFor(schema.GroupVersionResource{"apps", "v1", "statefulset"}, "", schema.GroupVersionKind{"apps", "v1", "StatefulSet"})
	mapper.RegisterKindFor(schema.GroupVersionResource{"apps", "v1beta1", "statefulset"}, "", schema.GroupVersionKind{"apps", "v1beta1", "StatefulSet"})
	mapper.RegisterKindFor(schema.GroupVersionResource{"apps", "v1beta2", "statefulset"}, "", schema.GroupVersionKind{"apps", "v1beta2", "StatefulSet"})

	mapper.RegisterKindFor(schema.GroupVersionResource{"apps", "v1", "statefulset"}, "scale", schema.GroupVersionKind{"apps", "v1", "Scale"})
	mapper.RegisterKindFor(schema.GroupVersionResource{"apps", "v1beta1", "statefulset"}, "scale", schema.GroupVersionKind{"apps", "v1beta1", "Scale"})
	mapper.RegisterKindFor(schema.GroupVersionResource{"apps", "v1alpha2", "statefulset"}, "scale", schema.GroupVersionKind{"apps", "v1beta2", "Scale"})

	mr := v1alpha1.MatchResources{
		MatchPolicy:       &equivalentMatch,
		NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"a": "b"}},
		ObjectSelector:    &metav1.LabelSelector{},
		ResourceRules:     []v1alpha1.NamedRuleWithOperations{},
	}

	for i := 0; i < 100; i++ {
		rule := v1alpha1.NamedRuleWithOperations{
			RuleWithOperations: v1alpha1.RuleWithOperations{
				Operations: []v1alpha1.OperationType{"*"},
				Rule: v1alpha1.Rule{
					APIGroups:   []string{fmt.Sprintf("app-%d", i)},
					APIVersions: []string{fmt.Sprintf("v%d", i)},
					Resources:   []string{fmt.Sprintf("resource%d", i), fmt.Sprintf("resource%d/scale", i)},
					Scope:       &allScopes,
				},
			},
		}
		mr.ResourceRules = append(mr.ResourceRules, rule)
	}

	criteria := &fakeCriteria{matchResources: mr}
	attrs := admission.NewAttributesRecord(nil, nil, schema.GroupVersionKind{"autoscaling", "v1", "Scale"}, "ns", "name", schema.GroupVersionResource{"apps", "v1", "deployments"}, "scale", admission.Create, &metav1.CreateOptions{}, false, nil)
	interfaces := &admission.RuntimeObjectInterfaces{EquivalentResourceMapper: mapper}
	matcher := &Matcher{namespaceMatcher: &namespace.Matcher{NamespaceLister: namespaceLister}, objectMatcher: &object.Matcher{}}

	for i := 0; i < b.N; i++ {
		matcher.Matches(criteria, attrs, interfaces)
	}
}

func BenchmarkShouldCallHookWithComplexSelectorAndRule(b *testing.B) {
	allScopes := v1alpha1.AllScopes
	equivalentMatch := v1alpha1.Equivalent

	namespace1Labels := map[string]string{"ns": "ns1"}
	namespace1 := corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "ns1",
			Labels: namespace1Labels,
		},
	}
	namespaceLister := fakeNamespaceLister{map[string]*corev1.Namespace{"ns": &namespace1}}

	mapper := runtime.NewEquivalentResourceRegistryWithIdentity(func(resource schema.GroupResource) string {
		if resource.Resource == "deployments" {
			// co-locate deployments in all API groups
			return "/deployments"
		}
		return ""
	})
	mapper.RegisterKindFor(schema.GroupVersionResource{"extensions", "v1beta1", "deployments"}, "", schema.GroupVersionKind{"extensions", "v1beta1", "Deployment"})
	mapper.RegisterKindFor(schema.GroupVersionResource{"apps", "v1", "deployments"}, "", schema.GroupVersionKind{"apps", "v1", "Deployment"})
	mapper.RegisterKindFor(schema.GroupVersionResource{"apps", "v1beta1", "deployments"}, "", schema.GroupVersionKind{"apps", "v1beta1", "Deployment"})
	mapper.RegisterKindFor(schema.GroupVersionResource{"apps", "v1alpha1", "deployments"}, "", schema.GroupVersionKind{"apps", "v1alpha1", "Deployment"})

	mapper.RegisterKindFor(schema.GroupVersionResource{"extensions", "v1beta1", "deployments"}, "scale", schema.GroupVersionKind{"extensions", "v1beta1", "Scale"})
	mapper.RegisterKindFor(schema.GroupVersionResource{"apps", "v1", "deployments"}, "scale", schema.GroupVersionKind{"autoscaling", "v1", "Scale"})
	mapper.RegisterKindFor(schema.GroupVersionResource{"apps", "v1beta1", "deployments"}, "scale", schema.GroupVersionKind{"apps", "v1beta1", "Scale"})
	mapper.RegisterKindFor(schema.GroupVersionResource{"apps", "v1alpha1", "deployments"}, "scale", schema.GroupVersionKind{"apps", "v1alpha1", "Scale"})

	mapper.RegisterKindFor(schema.GroupVersionResource{"apps", "v1", "statefulset"}, "", schema.GroupVersionKind{"apps", "v1", "StatefulSet"})
	mapper.RegisterKindFor(schema.GroupVersionResource{"apps", "v1beta1", "statefulset"}, "", schema.GroupVersionKind{"apps", "v1beta1", "StatefulSet"})
	mapper.RegisterKindFor(schema.GroupVersionResource{"apps", "v1beta2", "statefulset"}, "", schema.GroupVersionKind{"apps", "v1beta2", "StatefulSet"})

	mapper.RegisterKindFor(schema.GroupVersionResource{"apps", "v1", "statefulset"}, "scale", schema.GroupVersionKind{"apps", "v1", "Scale"})
	mapper.RegisterKindFor(schema.GroupVersionResource{"apps", "v1beta1", "statefulset"}, "scale", schema.GroupVersionKind{"apps", "v1beta1", "Scale"})
	mapper.RegisterKindFor(schema.GroupVersionResource{"apps", "v1alpha2", "statefulset"}, "scale", schema.GroupVersionKind{"apps", "v1beta2", "Scale"})

	nsSelector := make(map[string]string)
	for i := 0; i < 100; i++ {
		nsSelector[fmt.Sprintf("key-%d", i)] = fmt.Sprintf("val-%d", i)
	}

	mr := v1alpha1.MatchResources{
		MatchPolicy:       &equivalentMatch,
		NamespaceSelector: &metav1.LabelSelector{MatchLabels: nsSelector},
		ObjectSelector:    &metav1.LabelSelector{},
		ResourceRules:     []v1alpha1.NamedRuleWithOperations{},
	}

	for i := 0; i < 100; i++ {
		rule := v1alpha1.NamedRuleWithOperations{
			RuleWithOperations: v1alpha1.RuleWithOperations{
				Operations: []v1alpha1.OperationType{"*"},
				Rule: v1alpha1.Rule{
					APIGroups:   []string{fmt.Sprintf("app-%d", i)},
					APIVersions: []string{fmt.Sprintf("v%d", i)},
					Resources:   []string{fmt.Sprintf("resource%d", i), fmt.Sprintf("resource%d/scale", i)},
					Scope:       &allScopes,
				},
			},
		}
		mr.ResourceRules = append(mr.ResourceRules, rule)
	}

	criteria := &fakeCriteria{matchResources: mr}
	attrs := admission.NewAttributesRecord(nil, nil, schema.GroupVersionKind{"autoscaling", "v1", "Scale"}, "ns", "name", schema.GroupVersionResource{"apps", "v1", "deployments"}, "scale", admission.Create, &metav1.CreateOptions{}, false, nil)
	interfaces := &admission.RuntimeObjectInterfaces{EquivalentResourceMapper: mapper}
	matcher := &Matcher{namespaceMatcher: &namespace.Matcher{NamespaceLister: namespaceLister}, objectMatcher: &object.Matcher{}}

	for i := 0; i < b.N; i++ {
		matcher.Matches(criteria, attrs, interfaces)
	}
}
