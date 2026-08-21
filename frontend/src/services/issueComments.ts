import { ApiError, api } from '@/api/client'

// PAI-475: every comment is either 'internal' (team-only) or 'external'
// (also visible on the Customer Portal sidebar). NEW comments default to
// 'internal' — explicit opt-in is required for customer visibility.
export type CommentVisibility = 'internal' | 'external'

export interface IssueComment {
  id: number
  issue_id: number
  author_id: number | null
  author: string | null
  avatar_path: string | null
  body: string
  visibility: CommentVisibility
  created_at: string
}

export interface CreateIssueCommentOptions {
  /** Optional durable exact-once key. Accepted only for internal comments. */
  clientRequestId?: string
  signal?: AbortSignal
}

export function loadIssueComments(issueId: number): Promise<IssueComment[]> {
  return api.get<IssueComment[]>(`/issues/${issueId}/comments`)
}

export function createIssueComment(
  issueId: number,
  body: string,
  visibility: CommentVisibility = 'internal',
  options: CreateIssueCommentOptions = {},
): Promise<IssueComment> {
  if (visibility !== 'internal' && options.clientRequestId !== undefined) {
    throw new ApiError(400, 'Client request ids are only valid for internal comments')
  }
  const payload: { body: string; visibility: CommentVisibility; client_request_id?: string } = {
    body,
    visibility,
  }
  if (options.clientRequestId !== undefined) payload.client_request_id = options.clientRequestId
  const requestOptions = options.signal ? { signal: options.signal } : undefined
  return requestOptions
    ? api.post<IssueComment>(`/issues/${issueId}/comments`, payload, requestOptions)
    : api.post<IssueComment>(`/issues/${issueId}/comments`, payload)
}

export function updateIssueCommentVisibility(
  commentId: number,
  visibility: CommentVisibility,
): Promise<{ id: number; visibility: CommentVisibility }> {
  return api.patch(`/comments/${commentId}`, { visibility })
}

export function deleteIssueComment(commentId: number): Promise<void> {
  return api.delete(`/comments/${commentId}`)
}
