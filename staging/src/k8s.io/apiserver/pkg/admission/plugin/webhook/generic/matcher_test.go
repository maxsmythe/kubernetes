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
	"errors"
	"testing"

	celtypes "github.com/google/cel-go/common/types"
	"github.com/stretchr/testify/require"

	admissionv1 "k8s.io/api/admission/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/admission/plugin/cel"
)

var _ cel.Filter = &fakeCelFilter{}

type fakeCelFilter struct {
	evaluations []cel.EvaluationResult
	throwError  bool
}

func (f *fakeCelFilter) ForInput(context.Context, *admission.VersionedAttributes, *admissionv1.AdmissionRequest, cel.OptionalVariableBindings, int64) ([]cel.EvaluationResult, error) {
	if f.throwError {
		return nil, errors.New("test error")
	}
	return f.evaluations, nil
}

func (f *fakeCelFilter) CompilationErrors() []error {
	return []error{}
}

func TestMatch(t *testing.T) {
	fakeAttr := admission.NewAttributesRecord(nil, nil, schema.GroupVersionKind{}, "default", "foo", schema.GroupVersionResource{}, "", admission.Create, nil, false, nil)
	fakeVersionedAttr, _ := admission.NewVersionedAttributes(fakeAttr, schema.GroupVersionKind{}, nil)

	cases := []struct {
		name         string
		evaluations  []cel.EvaluationResult
		throwError   bool
		shouldMatch  bool
		returnedName string
	}{
		{
			name: "test single matches",
			evaluations: []cel.EvaluationResult{
				{
					EvalResult:         celtypes.True,
					ExpressionAccessor: &MatchCondition{},
				},
			},
			shouldMatch: true,
		},
		{
			name: "test multiple match",
			evaluations: []cel.EvaluationResult{
				{
					EvalResult:         celtypes.True,
					ExpressionAccessor: &MatchCondition{},
				},
				{
					EvalResult:         celtypes.True,
					ExpressionAccessor: &MatchCondition{},
				},
			},
			shouldMatch: true,
		},
		{
			name:        "test empty evals",
			evaluations: []cel.EvaluationResult{},
			shouldMatch: true,
		},
		{
			name: "test single no match",
			evaluations: []cel.EvaluationResult{
				{
					EvalResult: celtypes.False,
					ExpressionAccessor: &MatchCondition{
						Name: "test1",
					},
				},
			},
			shouldMatch:  false,
			returnedName: "test1",
		},
		{
			name: "test multiple no match",
			evaluations: []cel.EvaluationResult{
				{
					EvalResult: celtypes.False,
					ExpressionAccessor: &MatchCondition{
						Name: "test1",
					},
				},
				{
					EvalResult: celtypes.False,
					ExpressionAccessor: &MatchCondition{
						Name: "test2",
					},
				},
			},
			shouldMatch:  false,
			returnedName: "test1",
		},
		{
			name: "test mixed with no match first",
			evaluations: []cel.EvaluationResult{
				{
					EvalResult: celtypes.False,
					ExpressionAccessor: &MatchCondition{
						Name: "test1",
					},
				},
				{
					EvalResult: celtypes.True,
					ExpressionAccessor: &MatchCondition{
						Name: "test2",
					},
				},
			},
			shouldMatch:  false,
			returnedName: "test1",
		},
		{
			name: "test mixed with no match last",
			evaluations: []cel.EvaluationResult{
				{
					EvalResult: celtypes.True,
					ExpressionAccessor: &MatchCondition{
						Name: "test2",
					},
				},
				{
					EvalResult: celtypes.False,
					ExpressionAccessor: &MatchCondition{
						Name: "test1",
					},
				},
			},
			shouldMatch:  false,
			returnedName: "test1",
		},
		{
			name: "test mixed with no match middle",
			evaluations: []cel.EvaluationResult{
				{
					EvalResult: celtypes.True,
					ExpressionAccessor: &MatchCondition{
						Name: "test2",
					},
				},
				{
					EvalResult: celtypes.False,
					ExpressionAccessor: &MatchCondition{
						Name: "test1",
					},
				},
				{
					EvalResult: celtypes.True,
					ExpressionAccessor: &MatchCondition{
						Name: "test2",
					},
				},
			},
			shouldMatch:  false,
			returnedName: "test1",
		},
		{
			name: "test error",
			evaluations: []cel.EvaluationResult{
				{
					EvalResult:         celtypes.True,
					ExpressionAccessor: &MatchCondition{},
				},
			},
			shouldMatch: true,
			throwError:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := matcher{
				filter: &fakeCelFilter{
					evaluations: tc.evaluations,
					throwError:  tc.throwError,
				},
			}
			ctx := context.TODO()
			matches, matchName, err := m.Match(ctx, fakeVersionedAttr, nil)

			if tc.throwError && err == nil {
				t.Errorf("expected error thrown when filter errors")
			} else if !tc.throwError && err != nil {
				t.Errorf("unexpected error thrown ")
			}

			require.Equal(t, tc.shouldMatch, matches)
			require.Equal(t, tc.returnedName, matchName)
		})
	}
}
