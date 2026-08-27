package archive

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"
)

// MemStore is the in-memory Store for tests and -dry-run planning. Puts
// records every write so tests can assert idempotence.
type MemStore struct {
	mu      sync.Mutex
	objects map[string]memObject
	Puts    []string // keys actually written (skips excluded)
	clock   time.Time
}

type memObject struct {
	data    []byte
	sha     string
	modTime time.Time
}

// NewMemStore returns an empty in-memory archive.
func NewMemStore() *MemStore {
	return &MemStore{objects: map[string]memObject{}, clock: time.Unix(1_700_000_000, 0)}
}

func (m *MemStore) Put(_ context.Context, class Class, name, localPath string) error {
	data, err := os.ReadFile(localPath)
	if err != nil {
		return err
	}
	sha, _, err := FileSHA256(localPath)
	if err != nil {
		return err
	}
	key := Key(class, name)
	m.mu.Lock()
	defer m.mu.Unlock()
	if prev, ok := m.objects[key]; ok && len(prev.data) == len(data) && prev.sha == sha {
		return nil
	}
	m.clock = m.clock.Add(time.Second)
	m.objects[key] = memObject{data: append([]byte(nil), data...), sha: sha, modTime: m.clock}
	m.Puts = append(m.Puts, key)
	return nil
}

func (m *MemStore) Get(_ context.Context, class Class, name, localPath string) error {
	m.mu.Lock()
	obj, ok := m.objects[Key(class, name)]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("%s: not in archive", Key(class, name))
	}
	return writePart(localPath, bytes.NewReader(obj.data))
}

func (m *MemStore) List(_ context.Context, class Class) ([]Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	prefix := string(class) + "/"
	var out []Object
	for k, o := range m.objects {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			out = append(out, Object{Name: k[len(prefix):], Size: int64(len(o.data)), LastModified: o.modTime, SHA256: o.sha})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *MemStore) Exists(ctx context.Context, class Class, name string) (bool, error) {
	_, ok, err := m.Stat(ctx, class, name)
	return ok, err
}

func (m *MemStore) Stat(_ context.Context, class Class, name string) (Object, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.objects[Key(class, name)]
	if !ok {
		return Object{}, false, nil
	}
	return Object{Name: name, Size: int64(len(o.data)), LastModified: o.modTime, SHA256: o.sha}, true, nil
}
