package engine

import (
	"container/list"
	"sync"
)

const maxRenderCacheBytes = 32 << 20

const maxRenderCacheEntries = 128

type renderCacheEntry[T any] struct {
	key    string
	value  T
	weight int
}

type renderCache[T any] struct {
	mu      sync.Mutex
	entries map[string]*list.Element
	order   list.List
	weight  int
}

func (cache *renderCache[T]) get(key string) (T, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	var zero T
	if cache.entries == nil {
		return zero, false
	}
	element, exists := cache.entries[key]
	if !exists {
		return zero, false
	}
	cache.order.MoveToFront(element)

	return element.Value.(renderCacheEntry[T]).value, true
}

func (cache *renderCache[T]) set(key string, value T, weight int) {
	if weight <= 0 || weight > maxRenderCacheBytes {
		return
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.entries == nil {
		cache.entries = make(map[string]*list.Element)
	}
	if element, exists := cache.entries[key]; exists {
		cache.weight -= element.Value.(renderCacheEntry[T]).weight
		cache.order.Remove(element)
		delete(cache.entries, key)
	}
	weight += len(key) + 512
	entry := renderCacheEntry[T]{key: key, value: value, weight: weight}
	cache.entries[key] = cache.order.PushFront(entry)
	cache.weight += weight
	for cache.weight > maxRenderCacheBytes || cache.order.Len() > maxRenderCacheEntries {
		oldest := cache.order.Back()
		if oldest == nil {
			break
		}
		removed := oldest.Value.(renderCacheEntry[T])
		delete(cache.entries, removed.key)
		cache.weight -= removed.weight
		cache.order.Remove(oldest)
	}
}
