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

package generic

import (
	"context"

	"github.com/google/cel-go/cel"
	celtypes "github.com/google/cel-go/common/types"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/admission"
	admissionmetrics "k8s.io/apiserver/pkg/admission/metrics"
	celplugin "k8s.io/apiserver/pkg/admission/plugin/cel"
	celconfig "k8s.io/apiserver/pkg/apis/cel"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/klog/v2"
)

var _ celplugin.ExpressionAccessor = &MatchCondition{}

// MatchCondition contains the inputs needed to compile, evaluate and match a cel expression
type MatchCondition struct {
	Expression      string
	Name            string
	WebhookStepType string
}

func (v *MatchCondition) GetExpression() string {
	return v.Expression
}

func (v *MatchCondition) ReturnTypes() []*cel.Type {
	return []*cel.Type{cel.BoolType}
}

var _ Matcher = &matcher{}

// matcher evaluates compiled cel expressions and determines if they match the given request or not
type matcher struct {
	filter     celplugin.Filter
	authorizer authorizer.Authorizer
}

func NewMatcher(filter celplugin.Filter, authorizer authorizer.Authorizer) Matcher {
	return &matcher{
		filter:     filter,
		authorizer: authorizer,
	}
}

// Match takes a list of Evaluation converts them into actionable a decision of whether they match or not.
// If no match returns name of failed condition, versionedParams can be nil for no additional params
func (m *matcher) Match(ctx context.Context, versionedAttr *admission.VersionedAttributes, versionedParams runtime.Object) (bool, string, error) {
	evalResults, err := m.filter.ForInput(ctx, versionedAttr, celplugin.CreateAdmissionRequest(versionedAttr.Attributes), celplugin.OptionalVariableBindings{
		VersionedParams: versionedParams,
		Authorizer:      m.authorizer,
	}, celconfig.PerCallLimit)

	//TODO: add event to webhook object for error
	if err != nil {
		// on error ignore the match condition, don't apply matching
		// filter returning error is unexpected and not an evaluation error so not incrementing metric here
		return true, "", err
	}

	for _, evalResult := range evalResults {
		matchCondition, ok := evalResult.ExpressionAccessor.(*MatchCondition)
		if !ok {
			klog.Error("Invalid type conversion to MatchCondition")
			continue
		}
		if evalResult.Error != nil {
			admissionmetrics.Metrics.ObserveMatchConditionEvalError(ctx, matchCondition.Name, matchCondition.WebhookStepType)
		}
		if evalResult.EvalResult == celtypes.False {
			return false, matchCondition.Name, nil
		}
	}
	// if no failures or empty list return true
	return true, "", nil
}
