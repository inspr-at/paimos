// SPDX-License-Identifier: AGPL-3.0-only
package modelprefs

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// KindText keeps one complete language together; an incomplete translation
// falls back to the complete English wording rather than mixing fields.
type KindText struct {
	Label    string   `json:"label"`
	Hint     string   `json:"hint"`
	Examples []string `json:"examples"`
}

// KindWordsDeSQL is a trusted expression for queries whose source is work_kinds.
// User-created/project kinds never acquire a built-in translation by slug.
const KindWordsDeSQL = `coalesce(words_de,(SELECT w.words_de FROM work_display_words w
 WHERE w.tenant_id=work_kinds.tenant_id AND w.subject_kind='work-kind' AND w.slug=work_kinds.slug
 AND work_kinds.project_id IS NULL AND work_kinds.created_by IS NULL))`

func (k *Kind) SelectDisplayWords(language string) {
	k.DisplayWords = &KindText{Label: k.Label, Hint: k.Hint, Examples: k.Examples}
	if language == "de" && k.WordsDe != nil && k.WordsDe.Label != "" && k.WordsDe.Hint != "" && k.WordsDe.Examples != nil {
		k.DisplayWords = k.WordsDe
	}
}

type SituationWords struct {
	Slug         string   `json:"slug"`
	WordsEn      KindText `json:"words_en"`
	WordsDe      KindText `json:"words_de"`
	DisplayWords KindText `json:"display_words"`
}

func LoadSituationWords(ctx context.Context, tx pgx.Tx, language string) ([]SituationWords, error) {
	rows, err := tx.Query(ctx, `SELECT slug,words_en,words_de FROM work_display_words WHERE subject_kind='situation' ORDER BY slug LIMIT 7`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SituationWords{}
	for rows.Next() {
		var word SituationWords
		if err := rows.Scan(&word.Slug, &word.WordsEn, &word.WordsDe); err != nil {
			return nil, err
		}
		word.DisplayWords = word.WordsEn
		if language == "de" {
			word.DisplayWords = word.WordsDe
		}
		out = append(out, word)
	}
	if len(out) > 6 {
		return nil, ErrBoardBounds
	}
	return out, rows.Err()
}
