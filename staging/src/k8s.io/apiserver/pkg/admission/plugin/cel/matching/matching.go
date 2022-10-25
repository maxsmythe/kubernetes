package matching

import (
	"fmt"
	"sync"

	v1 "k8s.io/api/admissionregistration/v1"
	"k8s.io/api/admissionregistration/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/admission/initializer"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"

	"k8s.io/apiserver/pkg/admission/plugin/webhook/predicates/namespace"
	"k8s.io/apiserver/pkg/admission/plugin/webhook/predicates/object"
	"k8s.io/apiserver/pkg/admission/plugin/webhook/predicates/rules"
)

// TODO some of this code is meant to be embedded in ValidatingAdmissionPolicyBinding
// but developing it separately as a mixin here

var _ initializer.WantsExternalKubeInformerFactory = &Matcher{}
var _ initializer.WantsExternalKubeClientSet = &Matcher{}

type Matcher struct {
	namespaceMatcher *namespace.Matcher
	objectMatcher    *object.Matcher

	// TODO this is temporary to avoid import cleanup... rules matchers are created dynamically
	rulesMatcher rules.Matcher
}

func (m *Matcher) SetExternalKubeInformerFactory(f informers.SharedInformerFactory) {
	namespaceInformer := f.Core().V1().Namespaces()
	m.namespaceMatcher.NamespaceLister = namespaceInformer.Lister()

	// We need to figure out how to plumb this through so we don't attempt to validate
	// requests before namespaces have been listed
	// a.SetReadyFunc(func() bool {
	// 	return namespaceInformer.Informer().HasSynced() && a.hookSource.HasSynced()
	// })
}

func (m *Matcher) SetExternalKubeClientSet(client kubernetes.Interface) {
	m.namespaceMatcher.Client = client
}

func (m *Matcher) ValidateInitialization() error {
	if err := m.namespaceMatcher.Validate(); err != nil {
		return fmt.Errorf("namespaceMatcher is not properly setup: %v", err)
	}
	return nil
}

func (m *Matcher) Matches(binding *BindingWrapper, attr admission.Attributes, o admission.ObjectInterfaces) (bool, error) {
	// TODO Note that namespace selector only matches against object if an existing namespace is being updated
	// this seems like a potential security hole... we should validate this behavior is intended
	matches, err := m.namespaceMatcher.MatchNamespaceSelector(binding, attr)
	if err != nil {
		return false, err
	}
	if !matches {
		return false, nil
	}

	matches, err = m.objectMatcher.MatchObjectSelector(binding, attr)
	if err != nil {
		return false, err
	}
	if !matches {
		return false, nil
	}

	// TODO I assume excluded rules should use fuzzy matching if rules is using fuzzy matching
	matchPolicy := binding.binding.Spec.MatchResources.MatchPolicy
	excludedResourceRules := binding.binding.Spec.MatchResources.ExcludeResourceRules
	if matchesResourceRules(excludedResourceRules, matchPolicy, attr, o) {
		return false, nil
	}

	// TODO I'm guessing the `resourceRules` field is where we are getting conversion information, unless we are not doing conversion?
	resourceRules := binding.binding.Spec.MatchResources.ResourceRules
	if !matchesResourceRules(resourceRules, matchPolicy, attr, o) {
		return false, nil
	}

	return true, nil
}

// TODO fuzzy matching in the webhook object returns an invocation, which tells the admission process what resource to
// convert the object to, simply returning a bool doesn't match this behavior... the interface for Matches() needs to
// be updated to include this information
func matchesResourceRules(namedRules []v1alpha1.NamedRuleWithOperations, matchPolicy *v1alpha1.MatchPolicyType, attr admission.Attributes, o admission.ObjectInterfaces) bool {
	for _, namedRule := range namedRules {
		// TODO figure out how to do schema conversion to a v1 Rule
		// v1Rule := namedRule.RuleWithOperations
		v1Rule := v1.RuleWithOperations{}
		ruleMatcher := rules.Matcher{
			Rule: v1Rule,
			Attr: attr,
		}
		if !ruleMatcher.Matches() {
			continue
		}
		// an empty name list always matches
		if len(namedRule.ResourceNames) == 0 {
			return true
		}
		// TODO: GetName() can return an empty string if the user is relying on
		// the API server to generate the name... figure out what to do for this edge case
		name := attr.GetName()
		for _, matchedName := range namedRule.ResourceNames {
			if name == matchedName {
				return true
			}
		}
	}

	// if match policy is exact, don't perform fuzzy matching
	if matchPolicy != nil && *matchPolicy == v1alpha1.Exact {
		return false
	}

	attrWithOverride := &attrWithResourceOverride{Attributes: attr}
	equivalents := o.GetEquivalentResourceMapper().EquivalentResourcesFor(attr.GetResource(), attr.GetSubresource())
	for _, namedRule := range namedRules {
		for _, equivalent := range equivalents {
			if equivalent == attr.GetResource() {
				// we have already checked the original resource
				continue
			}
			attrWithOverride.resource = equivalent
			// TODO leverage schema conversion
			m := rules.Matcher{
				Rule: v1.RuleWithOperations{},
				Attr: attrWithOverride,
			}
			if !m.Matches() {
				continue
			}

			// TODO: GetName() can return an empty string if the user is relying on
			// the API server to generate the name... figure out what to do for this edge case
			name := attr.GetName()
			for _, matchedName := range namedRule.ResourceNames {
				if name == matchedName {
					// TODO return an invocation-type object that returns the new resources and kind
					return true
				}
			}

		}
	}
	return false
}

type attrWithResourceOverride struct {
	admission.Attributes
	resource schema.GroupVersionResource
}

func (a *attrWithResourceOverride) GetResource() schema.GroupVersionResource { return a.resource }

// temporary wrapper that adds the necessary methods for evaluating match criteria
type BindingWrapper struct {
	binding *v1alpha1.ValidatingAdmissionPolicyBinding

	namespaceSelectorError error
	namespaceSelector      labels.Selector
	initNamespaceSelector  sync.Once

	objectSelectorError error
	objectSelector      labels.Selector
	initObjectSelector  sync.Once
}

func (w *BindingWrapper) GetParsedNamespaceSelector() (labels.Selector, error) {
	w.initNamespaceSelector.Do(func() {
		w.namespaceSelector, w.namespaceSelectorError = metav1.LabelSelectorAsSelector(w.binding.Spec.MatchResources.NamespaceSelector)
	})
	return w.namespaceSelector, w.namespaceSelectorError
}

func (w *BindingWrapper) GetParsedObjectSelector() (labels.Selector, error) {
	w.initObjectSelector.Do(func() {
		w.objectSelector, w.objectSelectorError = metav1.LabelSelectorAsSelector(w.binding.Spec.MatchResources.ObjectSelector)
	})
	return w.objectSelector, w.objectSelectorError
}
