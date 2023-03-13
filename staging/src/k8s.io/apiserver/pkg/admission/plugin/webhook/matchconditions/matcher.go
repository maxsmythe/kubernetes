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

package matchconditions

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/cel-go/cel"
	celtypes "github.com/google/cel-go/common/types"

	v1 "k8s.io/api/admissionregistration/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apiserver/pkg/admission"
	admissionmetrics "k8s.io/apiserver/pkg/admission/metrics"
	celplugin "k8s.io/apiserver/pkg/admission/plugin/cel"
	celconfig "k8s.io/apiserver/pkg/apis/cel"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/klog/v2"
)

var _ celplugin.ExpressionAccessor = &MatchCondition{}

// MatchCondition contains the inputs needed to compile, evaluate and match a cel expression
type MatchCondition v1.MatchCondition

func (v *MatchCondition) GetExpression() string {
	return v.Expression
}

func (v *MatchCondition) ReturnTypes() []*cel.Type {
	return []*cel.Type{cel.BoolType}
}

var _ Matcher = &matcher{}

// matcher evaluates compiled cel expressions and determines if they match the given request or not
type matcher struct {
	filter      celplugin.Filter
	authorizer  authorizer.Authorizer
	failPolicy  *v1.FailurePolicyType
	matcherType string
	objectName  string
}

func NewMatcher(filter celplugin.Filter, authorizer authorizer.Authorizer, failPolicy *v1.FailurePolicyType, matcherType, objectName string) Matcher {
	return &matcher{
		filter:      filter,
		authorizer:  authorizer,
		failPolicy:  failPolicy,
		matcherType: matcherType,
		objectName:  objectName,
	}
}

func (m *matcher) Match(ctx context.Context, versionedAttr *admission.VersionedAttributes, versionedParams runtime.Object) MatchResult {
	var f v1.FailurePolicyType
	if m.failPolicy == nil {
		f = v1.Fail
	} else {
		f = *m.failPolicy
	}

	evalResults, err := m.filter.ForInput(ctx, versionedAttr, celplugin.CreateAdmissionRequest(versionedAttr.Attributes), celplugin.OptionalVariableBindings{
		VersionedParams: versionedParams,
		Authorizer:      m.authorizer,
	}, celconfig.RuntimeCELCostBudgetMatchConditions)

	//TODO: add event to webhook object for error
	if err != nil {
		// filter returning error is unexpected and not an evaluation error so not incrementing metric here
		if f == v1.Fail {
			return MatchResult{
				Error: err,
			}
		} else {
			return MatchResult{
				Matches: true,
			}
		}
	}

	errorList := []error{}
	for _, evalResult := range evalResults {
		matchCondition, ok := evalResult.ExpressionAccessor.(*MatchCondition)
		if !ok {
			klog.Error("Invalid type conversion to MatchCondition")
			continue
		}
		if evalResult.Error != nil {
			errorList = append(errorList, evalResult.Error)
			//TODO: what's the best way to handle this metric since its reused by VAP for match conditions
			admissionmetrics.Metrics.ObserveMatchConditionEvalError(ctx, m.objectName, m.matcherType)
		}
		if evalResult.EvalResult == celtypes.False {
			// If any condition false, skip calling webhook always
			return MatchResult{
				Matches:             false,
				FailedConditionName: matchCondition.Name,
			}
		}
	}
	if len(errorList) > 0 {
		// If mix of true and eval errors then resort to fail policy
		if f == v1.Fail {
			// mix of true and errors with fail policy fail should fail request without calling webhook
			if len(errorList) > 1 {
				for i := 1; i < len(errorList); i++ {
					// TODO: merge errors; until then, just return the first one.
					utilruntime.HandleError(errorList[i])
				}
			}
			err = errors.New(fmt.Sprintf("Error evaluating match conditions for %v: %v with failurePolicyType fail", m.objectName, errorList[0]))
			return MatchResult{
				Error: err,
			}
		} else if f == v1.Ignore {
			// if fail policy ignore then call webhook
			return MatchResult{
				Matches: true,
			}
		}
	}
	// if no results eval to false, return matches true with list of any errors encountered
	return MatchResult{
		Matches: true,
	}
}
