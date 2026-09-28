// SPDX-License-Identifier: AGPL-3.0-only

package events

import (
	"context"
	"encoding/json"
	"errors"
)

type operatorAnnotationKey struct{}

// OperatorAnnotation marks host-operator events. Brief is the fixed disposable
// brief number 1, 2 or 3. Production records a confirmed production host run.
type OperatorAnnotation struct {
	Production bool
	Brief      string
}

// WithOperatorAnnotation returns ctx carrying the host-operator event mark.
// An empty mark leaves ctx unchanged. Append copies the mark onto object
// snapshots so intake and requirements events share the journey seed tags.
func WithOperatorAnnotation(ctx context.Context, mark OperatorAnnotation) context.Context {
	if !mark.Production && mark.Brief == "" {
		return ctx
	}
	return context.WithValue(ctx, operatorAnnotationKey{}, mark)
}

func annotateOperator(ctx context.Context, after json.RawMessage) (json.RawMessage, error) {
	if len(after) == 0 {
		return after, nil
	}
	mark, ok := ctx.Value(operatorAnnotationKey{}).(OperatorAnnotation)
	if !ok || (!mark.Production && mark.Brief == "") {
		return after, nil
	}
	if mark.Brief != "" && mark.Brief != "1" && mark.Brief != "2" && mark.Brief != "3" {
		return nil, errors.New("operator brief mark must be 1, 2 or 3")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(after, &fields); err != nil || fields == nil {
		return after, nil
	}
	if mark.Production {
		if _, exists := fields["production"]; !exists {
			fields["production"] = json.RawMessage("true")
		}
	}
	if mark.Brief != "" {
		if _, exists := fields["disposable"]; !exists {
			fields["disposable"] = json.RawMessage("true")
		}
		if _, exists := fields["brief"]; !exists {
			fields["brief"] = json.RawMessage(mark.Brief)
		}
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}
