package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

// FileStore хранит сессии в памяти и синхронизирует их с JSON-файлом на диске.
// Формат файла: JSON-объект map[string]Session, где ключ — строковый userID.
type FileStore struct {
	mu       sync.RWMutex
	sessions map[int64]Session
	path     string
}

// NewFileStore создает FileStore и загружает данные из указанного файла.
// При ошибке чтения файла логирует предупреждение и стартует с пустой картой.
func NewFileStore(path string) (*FileStore, error) {
	if path == "" {
		return nil, fmt.Errorf("filestore path is empty")
	}

	fs := &FileStore{
		sessions: make(map[int64]Session),
		path:     path,
	}
	if err := fs.load(); err != nil {
		return nil, err
	}
	return fs, nil
}

// Save сохраняет/обновляет сессию и атомарно записывает состояние на диск.
func (s *FileStore) Save(session Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.sessions[session.UserID] = session
	return s.persistLocked()
}

// Get возвращает сессию пользователя, если она существует.
func (s *FileStore) Get(userID int64) (Session, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	session, ok := s.sessions[userID]
	return session, ok
}

// Delete удаляет сессию и записывает новое состояние на диск.
// Ошибка записи логируется, но не возвращается (интерфейс совместим с MemoryStore).
func (s *FileStore) Delete(userID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.sessions, userID)
	if err := s.persistLocked(); err != nil {
		log.Printf("filestore: persist after delete failed: %v", err)
	}
}

func (s *FileStore) load() error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create store dir: %w", err)
	}

	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		log.Printf("filestore: read file %s: %v", s.path, err)
		return nil
	}
	if len(data) == 0 {
		return nil
	}

	var raw map[string]Session
	if err := json.Unmarshal(data, &raw); err != nil {
		log.Printf("filestore: unmarshal %s: %v", s.path, err)
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for key, session := range raw {
		id, err := strconv.ParseInt(key, 10, 64)
		if err != nil {
			log.Printf("filestore: skip invalid user id %q: %v", key, err)
			continue
		}
		s.sessions[id] = session
	}
	return nil
}

func (s *FileStore) persistLocked() error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create store dir: %w", err)
	}

	payload := make(map[string]Session, len(s.sessions))
	for id, session := range s.sessions {
		payload[strconv.FormatInt(id, 10)] = session
	}

	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal sessions: %w", err)
	}

	tmpFile, err := os.CreateTemp(dir, filepath.Base(s.path)+".tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}

	tmpName := tmpFile.Name()
	if err := os.Chmod(tmpName, 0o600); err != nil && !errors.Is(err, os.ErrPermission) {
		tmpFile.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("chmod temp file: %w", err)
	}

	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		tmpFile.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Rename(tmpName, s.path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("rename temp file: %w", err)
	}

	return nil
}
