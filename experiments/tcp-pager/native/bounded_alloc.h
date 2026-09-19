/* Single-threaded, local-only allocator for the CFAllocator scope diagnostic.
 * Not a remote-memory backend or a production allocator. */
#ifndef NEXAL_BOUNDED_ALLOC_H
#define NEXAL_BOUNDED_ALLOC_H
#include <stddef.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

typedef struct {
    size_t limit, live, peak, allocations, deallocations, reallocations;
} nx_alloc_state;
typedef union {
    max_align_t alignment;
    struct { size_t size; nx_alloc_state *owner; } info;
} nx_header;

static void *nx_allocate(nx_alloc_state *s, size_t n) {
    if (!n || n>SIZE_MAX-sizeof(nx_header) || s->live>s->limit || n>s->limit-s->live)
        return NULL;
    nx_header *h=malloc(sizeof(*h)+n);
    if (!h) return NULL;
    h->info.size=n; h->info.owner=s;
    s->live+=n; s->allocations++;
    if (s->live>s->peak) s->peak=s->live;
    return h+1;
}
static void nx_deallocate(nx_alloc_state *s, void *ptr) {
    if (!ptr) return;
    nx_header *h=(nx_header *)ptr-1;
    if (h->info.owner!=s || h->info.size>s->live) abort();
    s->live-=h->info.size; s->deallocations++;
    free(h);
}
static void *nx_reallocate(nx_alloc_state *s, void *ptr, size_t n) {
    s->reallocations++;
    if (!ptr) return nx_allocate(s,n);
    if (!n) { nx_deallocate(s,ptr); return NULL; }
    nx_header *h=(nx_header *)ptr-1;
    if (h->info.owner!=s) abort();
    void *next=nx_allocate(s,n);
    if (!next) return NULL; /* Original allocation remains valid. */
    memcpy(next,ptr,n<h->info.size?n:h->info.size);
    nx_deallocate(s,ptr);
    return next;
}
#endif
