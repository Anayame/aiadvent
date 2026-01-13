package auth

import "sync"

// MemoryStore простое in-memory хранилище сессий, потокобезопасное.
type MemoryStore struct {
	mu       sync.RWMutex
	sessions map[int64]Session
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		sessions: make(map[int64]Session),
	}
}

func (s *MemoryStore) Save(session Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[session.UserID] = session
	return nil
}

func (s *MemoryStore) Get(userID int64) (Session, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, ok := s.sessions[userID]
	return session, ok
}

func (s *MemoryStore) Delete(userID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, userID)
}
