package pager

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
)

// Pattern mirrors the guest's xorshift workload. It is deterministic test data,
// not a cryptographic RNG and must never be used for keys.
func Pattern(page int) []byte {
	out := make([]byte, PageSize)
	x := uint64(0x9e3779b97f4a7c15) ^ uint64(page+1)*0xd1b54a32d192ed03
	for i := 0; i < PageSize; i += 8 {
		binary.LittleEndian.PutUint64(out[i:], x)
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
	}
	return out
}

type Report struct {
	Mode                   string         `json:"mode"`
	Verified               bool           `json:"verified"`
	LogicalBytes           int            `json:"logicalBytes"`
	CachePayloadLimitBytes int            `json:"cachePayloadLimitBytes"`
	VerifiedBytes          int            `json:"verifiedBytes"`
	Cache                  CacheStats     `json:"cache"`
	Transport              TransportStats `json:"transport"`
	NativeHVFExecuted      bool           `json:"nativeHVFExecuted"`
	MacOSGuestBooted       bool           `json:"macOSGuestBooted"`
	HostRAMExpanded        bool           `json:"hostRAMExpanded"`
	GPUMemoryExpanded      bool           `json:"gpuMemoryExpanded"`
}

func Portable(ctx context.Context, c *Client, slots int) (Report, error) {
	r := Report{Mode: "portable TCP pager; not CPU-fault-driven", LogicalBytes: c.Pages() * PageSize, CachePayloadLimitBytes: slots * PageSize}
	cache, err := NewCache(c, slots)
	if err != nil {
		return r, err
	}
	for i := 0; i < c.Pages(); i++ {
		if err = cache.Write(ctx, i, Pattern(i)); err != nil {
			return r, err
		}
	}
	if err = cache.Flush(ctx); err != nil {
		return r, err
	}
	// A new empty cache forces every verification page back through TCP.
	writes := cache.Stats()
	cache, err = NewCache(c, slots)
	if err != nil {
		return r, err
	}
	for i := c.Pages() - 1; i >= 0; i-- {
		got, e := cache.Read(ctx, i)
		if e != nil {
			return r, e
		}
		if !bytes.Equal(got, Pattern(i)) {
			return r, errors.New("workload data verification failed")
		}
		r.VerifiedBytes += PageSize
	}
	r.Cache = cache.Stats()
	r.Cache.Faults += writes.Faults
	r.Cache.Evictions += writes.Evictions
	r.Transport = c.Stats()
	r.Verified = true
	return r, nil
}
