// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import "context"

// AdmissionGroup cross-checks synthetic-commit ancestry against the live queue
// prefix. Open PR ancestry alone could omit a closed or missing preceding PR.
// Re-read the prefix after the bounded REST reads; changed membership refuses.
func (g AppReader) AdmissionGroup(ctx context.Context, head, base, ref string) ([]Pull, error) {
	before, err := g.App.AdmissionQueueMembers(ctx, head)
	if err != nil {
		return nil, err
	}
	pulls, err := g.Group(ctx, head, base, ref)
	if err != nil {
		return nil, err
	}
	after, err := g.App.AdmissionQueueMembers(ctx, head)
	if err != nil {
		return nil, err
	}
	if len(before) != len(after) || len(after) != len(pulls) {
		return nil, errRead
	}
	expected := map[int64]string{}
	for _, p := range before {
		expected[p.Number] = p.Head
	}
	for _, p := range after {
		if expected[p.Number] != p.Head {
			return nil, errRead
		}
	}
	for _, p := range pulls {
		if expected[p.Number] != p.Head {
			return nil, errRead
		}
	}
	return pulls, nil
}
