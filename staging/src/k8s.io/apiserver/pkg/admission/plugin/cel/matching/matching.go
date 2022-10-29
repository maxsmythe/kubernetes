package matching

import (
	"fmt"

	v1 "k8s.io/api/admissionregistration/v1"
	"k8s.io/api/admissionregistration/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/admission/initializer"
	"k8s.io/client-go/kubernetes"
	listersv1 "k8s.io/client-go/listers/core/v1"

	"k8s.io/apiserver/pkg/admission/plugin/webhook/predicates/namespace"
	"k8s.io/apiserver/pkg/admission/plugin/webhook/predicates/object"
	"k8s.io/apiserver/pkg/admission/plugin/webhook/predicates/rules"
)

var _ initializer.WantsExternalKubeClientSet = &Matcher{}

type MatchCriteria interface {
	namespace.NamespaceSelectorProvider
	object.ObjectSelectorProvider

	GetMatchResources() v1alpha1.MatchResources
}

type Matcher struct {
	namespaceMatcher *namespace.Matcher
	objectMatcher    *object.Matcher
}

func (m *Matcher) SetNamespaceLister(lister listersv1.NamespaceLister) {
	m.namespaceMatcher.NamespaceLister = lister
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

func (m *Matcher) Matches(criteria MatchCriteria, attr admission.Attributes, o admission.ObjectInterfaces) (bool, error) {
	// TODO Note that namespace selector only matches against object if an existing namespace is being updated
	// this seems like a potential security hole... we should validate this behavior is intended
	matches, err := m.namespaceMatcher.MatchNamespaceSelector(criteria, attr)
	if err != nil {
		return false, err
	}
	if !matches {
		return false, nil
	}

	matches, err = m.objectMatcher.MatchObjectSelector(criteria, attr)
	if err != nil {
		return false, err
	}
	if !matches {
		return false, nil
	}

	matchResources := criteria.GetMatchResources()
	matchPolicy := matchResources.MatchPolicy
	if matchesResourceRules(matchResources.ExcludeResourceRules, matchPolicy, attr, o) {
		return false, nil
	}

	if !matchesResourceRules(matchResources.ResourceRules, matchPolicy, attr, o) {
		return false, nil
	}

	return true, nil
}

func matchesResourceRules(namedRules []v1alpha1.NamedRuleWithOperations, matchPolicy *v1alpha1.MatchPolicyType, attr admission.Attributes, o admission.ObjectInterfaces) bool {
	for _, namedRule := range namedRules {
		// TODO once we have type aliasing for RuleWithOperations, calling the convert function will be moot
		ruleMatcher := rules.Matcher{
			Rule: convertV1alpha1RuleToV1Rule(namedRule.RuleWithOperations),
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
			// TODO once we have type aliasing for RuleWithOperations, calling the convert function will be moot
			m := rules.Matcher{
				Rule: convertV1alpha1RuleToV1Rule(namedRule.RuleWithOperations),
				Attr: attrWithOverride,
			}
			if !m.Matches() {
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
	}
	return false
}

type attrWithResourceOverride struct {
	admission.Attributes
	resource schema.GroupVersionResource
}

func (a *attrWithResourceOverride) GetResource() schema.GroupVersionResource { return a.resource }

// TODO the below function is a temporary fix until type aliasing is implemented
func convertV1alpha1RuleToV1Rule(oldRule v1alpha1.RuleWithOperations) v1.RuleWithOperations {
	convertOperations := func(oldOperations []v1alpha1.OperationType) []v1.OperationType {
		ops := make([]v1.OperationType, len(oldOperations))
		for i := range oldOperations {
			ops[i] = v1.OperationType(oldOperations[i])
		}
		return ops
	}

	convertScope := func(oldScope v1alpha1.ScopeType) v1.ScopeType {
		return v1.ScopeType(oldScope)
	}

	newScope := convertScope(*oldRule.Scope)
	newRule := v1.RuleWithOperations{
		Rule: v1.Rule{
			APIGroups:   oldRule.APIGroups,
			APIVersions: oldRule.APIVersions,
			Resources:   oldRule.Resources,
			Scope:       &newScope,
		},
		Operations: convertOperations(oldRule.Operations),
	}
	return newRule
}
