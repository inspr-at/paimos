// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

// CodexConversationAdapter returns the exact registered adapter only for the
// explicit conversation runner. It does not add conversation capability to
// ordinary managed coding sessions or their public capability advertisement.
func (s *Supervisor) CodexConversationAdapter() (*CodexAdapter, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	adapter, ok := s.adapters[AdapterCodex].(*CodexAdapter)
	return adapter, ok && adapter != nil
}
