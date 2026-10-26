package app

import (
	"sync"
	"time"

	"github.com/sairaph/rodin3d-mcp/internal/service"
)

// setSaver writes input sets to the store on its own goroutine, so a slow
// write (the sync daemon holding the database) never stalls the window.
// The newest content of each set wins: a set changed again before its
// write starts is written once, as it stands then. Every save returns a
// channel that receives the result of the write that covers it.
type setSaver struct {
	write   func(service.InputSet) error
	onError func(id string, err error) // a failed write, for the window

	mu      sync.Mutex
	pending map[string]*saveJob
	order   []string // set ids in the order they were first queued
	busy    bool     // a write is running
	idle    chan struct{}
	wake    chan struct{}
}

type saveJob struct {
	set     service.InputSet
	waiters []chan error
}

func newSetSaver(write func(service.InputSet) error, onError func(string, error)) *setSaver {
	s := &setSaver{write: write, onError: onError, pending: map[string]*saveJob{}, wake: make(chan struct{}, 1)}
	go s.loop()
	return s
}

// save queues set for writing.
func (s *setSaver) save(set service.InputSet) <-chan error {
	ch := make(chan error, 1)
	s.mu.Lock()
	if j, ok := s.pending[set.ID]; ok {
		j.set = set
		j.waiters = append(j.waiters, ch)
	} else {
		s.pending[set.ID] = &saveJob{set: set, waiters: []chan error{ch}}
		s.order = append(s.order, set.ID)
	}
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return ch
}

// next takes the oldest queued set, or reports that none is left.
func (s *setSaver) next() (*saveJob, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.order) == 0 {
		s.busy = false
		if s.idle != nil {
			close(s.idle)
			s.idle = nil
		}
		return nil, false
	}
	id := s.order[0]
	s.order = s.order[1:]
	j := s.pending[id]
	delete(s.pending, id)
	s.busy = true
	return j, true
}

func (s *setSaver) loop() {
	for range s.wake {
		for {
			j, ok := s.next()
			if !ok {
				break
			}
			err := s.write(j.set)
			if err != nil && s.onError != nil {
				s.onError(j.set.ID, err)
			}
			for _, ch := range j.waiters {
				ch <- err
			}
		}
	}
}

// flush waits up to d until every queued set is written; false when some
// were still being written.
func (s *setSaver) flush(d time.Duration) bool {
	s.mu.Lock()
	if len(s.order) == 0 && !s.busy {
		s.mu.Unlock()
		return true
	}
	if s.idle == nil {
		s.idle = make(chan struct{})
	}
	idle := s.idle
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
	select {
	case <-idle:
		return true
	case <-time.After(d):
		return false
	}
}
