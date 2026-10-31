package edge

import "sync"

// settings is what the core tells the edge: bot ids and webhook secrets.
// The edge holds no tokens and no master key.
type settings struct {
	mu      sync.RWMutex
	secrets map[int64]string
}

func (s *settings) secret(id int64) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.secrets[id]
	return v, ok
}

func (s *settings) setSecrets(m map[int64]string) {
	s.mu.Lock()
	s.secrets = m
	s.mu.Unlock()
}
