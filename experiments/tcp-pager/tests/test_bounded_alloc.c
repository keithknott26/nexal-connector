#include <assert.h>
#include "../native/bounded_alloc.h"
int main(void) {
    nx_alloc_state s={.limit=256};
    assert(nx_allocate(&s,0)==NULL);
    assert(nx_allocate(&s,SIZE_MAX)==NULL);
    unsigned char *p=nx_allocate(&s,32);
    assert(p && (uintptr_t)p%_Alignof(max_align_t)==0);
    memset(p,0x5a,32);
    assert(nx_reallocate(&s,p,256)==NULL); /* Old data preserved on failure. */
    for (size_t i=0;i<32;i++) assert(p[i]==0x5a);
    p=nx_reallocate(&s,p,64);
    assert(p);
    for (size_t i=0;i<32;i++) assert(p[i]==0x5a);
    assert(s.live==64 && s.peak==96);
    p=nx_reallocate(&s,p,16);
    assert(p && s.live==16);
    assert(nx_reallocate(&s,p,0)==NULL && s.live==0);
    p=nx_reallocate(&s,NULL,256);
    assert(p && s.live==256 && nx_allocate(&s,1)==NULL);
    nx_deallocate(&s,p);
    nx_deallocate(&s,NULL);
    assert(s.live==0 && s.allocations==s.deallocations);
    return 0;
}
