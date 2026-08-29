package readstate

// WriteCount reports how many times the file has been written. It exists so
// tests can pin down that a bulk change costs exactly one write.
func (s *Store) WriteCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.writes
}
