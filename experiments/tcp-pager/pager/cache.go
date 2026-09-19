package pager

import (
	"context"
	"errors"
	"sync"
)

type Backend interface {
	Pages() int
	Get(context.Context, int) ([]byte, error)
	Put(context.Context, int, []byte) error
}
type frame struct {
	page  int
	data  []byte
	dirty bool
	used  uint64
}
type CacheStats struct {
	Faults            uint64 `json:"faults"`
	Evictions         uint64 `json:"evictions"`
	PeakResidentPages int    `json:"peakResidentPages"`
}
type Cache struct {
	mu       sync.Mutex
	backend  Backend
	capacity int
	frames   map[int]*frame
	tick     uint64
	stats    CacheStats
}

func NewCache(b Backend, slots int) (*Cache, error) {
	if slots < 1 || slots > 16 || slots >= b.Pages() {
		return nil, errors.New("cache must be 1..16 pages and smaller than donor")
	}
	return &Cache{backend: b, capacity: slots, frames: make(map[int]*frame)}, nil
}
func (c *Cache) load(ctx context.Context, page int) (*frame, error) {
	if page < 0 || page >= c.backend.Pages() {
		return nil, errors.New("page out of range")
	}
	c.tick++
	if f := c.frames[page]; f != nil {
		f.used = c.tick
		return f, nil
	}
	c.stats.Faults++
	data, err := c.backend.Get(ctx, page)
	if err != nil {
		return nil, err
	}
	if len(data) != PageSize {
		return nil, errors.New("backend returned invalid page size")
	}
	// At most capacity resident frames plus one incoming transport page.
	if len(c.frames) == c.capacity {
		var victim *frame
		for _, f := range c.frames {
			if victim == nil || f.used < victim.used {
				victim = f
			}
		}
		if victim.dirty {
			if err = c.backend.Put(ctx, victim.page, victim.data); err != nil {
				return nil, err
			}
		}
		delete(c.frames, victim.page)
		c.stats.Evictions++
	}
	f := &frame{page: page, data: data, used: c.tick}
	c.frames[page] = f
	if len(c.frames) > c.stats.PeakResidentPages {
		c.stats.PeakResidentPages = len(c.frames)
	}
	return f, nil
}
func (c *Cache) Read(ctx context.Context, page int) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f, err := c.load(ctx, page)
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), f.data...), nil
}
func (c *Cache) Write(ctx context.Context, page int, data []byte) error {
	if len(data) != PageSize {
		return errors.New("write must be exactly one page")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	f, err := c.load(ctx, page)
	if err != nil {
		return err
	}
	copy(f.data, data)
	f.dirty = true
	return nil
}
func (c *Cache) Flush(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, f := range c.frames {
		if f.dirty {
			if err := c.backend.Put(ctx, f.page, f.data); err != nil {
				return err
			}
			f.dirty = false
		}
	}
	return nil
}
func (c *Cache) Stats() CacheStats { c.mu.Lock(); defer c.mu.Unlock(); return c.stats }
