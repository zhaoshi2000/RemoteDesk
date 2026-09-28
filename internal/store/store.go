// Package store is the single-process, durable bootstrap store. It is not a PostgreSQL adapter.
package store

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"remotedesk.local/remotedesk/internal/identity"
	"remotedesk.local/remotedesk/internal/protocol"
)

// Registry implementations must persist device identity without permitting key replacement.
type Registry interface {
	Get(string) (protocol.Device, bool)
	List() []protocol.Device
	Register(protocol.Device) (protocol.Device, error)
	Update(protocol.Device) error
	Delete(string) error
}
type Store struct {
	mu      sync.RWMutex
	path    string
	devices map[string]protocol.Device
}

func Open(path string) (*Store, error) {
	s := &Store{path: path, devices: map[string]protocol.Device{}}
	if path == "" {
		return s, nil
	}
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return nil, e
	}
	if e := identity.ReadJSON(path, &s.devices); e != nil && !os.IsNotExist(e) {
		return nil, e
	}
	return s, nil
}
func (s *Store) Get(id string) (protocol.Device, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.devices[id]
	return d, ok
}
func (s *Store) List() []protocol.Device {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r := make([]protocol.Device, 0, len(s.devices))
	for _, d := range s.devices {
		r = append(r, d)
	}
	sort.Slice(r, func(i, j int) bool { return r[i].ID < r[j].ID })
	return r
}
func (s *Store) Register(d protocol.Device) (protocol.Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.devices[d.ID]; ok {
		if old.PublicKey != d.PublicKey {
			return d, errors.New("device ID collision")
		}
		return old, nil
	}
	if len(s.devices) >= 10000 {
		return d, errors.New("device capacity reached")
	}
	s.devices[d.ID] = d
	if s.path != "" {
		if e := identity.WriteJSON(s.path, s.devices); e != nil {
			delete(s.devices, d.ID)
			return d, e
		}
	}
	return d, nil
}

func (s *Store) Update(d protocol.Device) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.devices[d.ID]
	if !ok || old.PublicKey != d.PublicKey {
		return errors.New("unknown device or attempted key replacement")
	}
	s.devices[d.ID] = d
	if s.path != "" {
		if e := identity.WriteJSON(s.path, s.devices); e != nil {
			s.devices[d.ID] = old
			return e
		}
	}
	return nil
}
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.devices[id]
	if !ok {
		return errors.New("unknown device")
	}
	delete(s.devices, id)
	if s.path != "" {
		if e := identity.WriteJSON(s.path, s.devices); e != nil {
			s.devices[id] = old
			return e
		}
	}
	return nil
}
