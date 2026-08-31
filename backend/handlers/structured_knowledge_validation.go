// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"unicode"

	"github.com/inspr-at/paimos/backend/models"
)

const structuredKnowledgeProposalMaxBytes = 64 * 1024

func structuredKnowledgeValidationForTx(
	ctx context.Context,
	tx *sql.Tx,
	projectID, excludeKnowledgeID int64,
	title, body string,
	shortBodyLimit int,
) (models.StructuredKnowledgeValidation, error) {
	result := models.StructuredKnowledgeValidation{
		Flags:               []string{},
		LikelyDuplicateIDs:  []int64{},
		BodyBytes:           len([]byte(body)),
		ShortBodyLimitBytes: shortBodyLimit,
	}
	if result.BodyBytes > shortBodyLimit {
		result.Flags = append(result.Flags, "essay")
	}
	if looksLikeChatNote(body) {
		result.Flags = append(result.Flags, "chat_note_prose")
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT i.id,i.title,COALESCE(i.description,'')
		FROM issues i
		WHERE i.project_id=? AND i.deleted_at IS NULL AND i.slug IS NOT NULL
		  AND i.type IN ('memory','runbook','external_system','related_project','guideline')
		  AND i.id<>?
		ORDER BY i.id`, projectID, excludeKnowledgeID)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	candidateTitle := normalizeKnowledgeText(title)
	candidateBody := normalizeKnowledgeText(body)
	candidateTokens := knowledgeTokens(candidateTitle + " " + candidateBody)
	for rows.Next() {
		var id int64
		var existingTitle, existingBody string
		if err := rows.Scan(&id, &existingTitle, &existingBody); err != nil {
			return result, err
		}
		sameTitle := candidateTitle != "" && candidateTitle == normalizeKnowledgeText(existingTitle)
		sameBody := candidateBody != "" && candidateBody == normalizeKnowledgeText(existingBody)
		similar := tokenJaccard(candidateTokens, knowledgeTokens(existingTitle+" "+existingBody)) >= 0.9
		if sameTitle || sameBody || similar {
			result.LikelyDuplicateIDs = append(result.LikelyDuplicateIDs, id)
		}
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	if len(result.LikelyDuplicateIDs) > 0 {
		result.Flags = append(result.Flags, "likely_duplicate")
	}
	sort.Strings(result.Flags)
	return result, nil
}

func normalizeKnowledgeText(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(value))), " ")
}

func knowledgeTokens(value string) map[string]struct{} {
	tokens := map[string]struct{}{}
	for _, token := range strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len([]rune(token)) >= 3 {
			tokens[token] = struct{}{}
		}
	}
	return tokens
}

func tokenJaccard(left, right map[string]struct{}) float64 {
	if len(left) < 5 || len(right) < 5 {
		return 0
	}
	intersection := 0
	for token := range left {
		if _, ok := right[token]; ok {
			intersection++
		}
	}
	union := len(left) + len(right) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

func looksLikeChatNote(body string) bool {
	turns := 0
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		line = strings.ToLower(strings.TrimSpace(line))
		for _, prefix := range []string{"amy:", "user:", "assistant:", "human:", "agent:", "me:", "you:"} {
			if strings.HasPrefix(line, prefix) {
				turns++
				break
			}
		}
	}
	if turns >= 2 {
		return true
	}
	normalized := normalizeKnowledgeText(body)
	return strings.HasPrefix(normalized, "quick note ") ||
		strings.Contains(normalized, " as we discussed ") ||
		strings.Contains(normalized, " in this chat ")
}
