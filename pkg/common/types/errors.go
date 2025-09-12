package types

import (
	"strings"
	"sync"
)

type MultiError struct {
	mu     sync.Mutex
	Errors []error
}

func (m *MultiError) Error() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	msgs := make([]string, len(m.Errors))
	for i, err := range m.Errors {
		msgs[i] = err.Error()
	}
	return strings.Join(msgs, "; ")
}

func (m *MultiError) Add(err error) {
	if err == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Errors = append(m.Errors, err)
}

func (m *MultiError) IsEmpty() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.Errors) == 0
}
