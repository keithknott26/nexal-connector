/* CFAllocatorCreate scope test. Real Core Foundation, local backing only.
 * No default allocator replacement, interposition, remote paging or OS changes.
 */
#include <CoreFoundation/CoreFoundation.h>
#include <inttypes.h>
#include <stdio.h>
#include <sys/sysctl.h>
#include "bounded_alloc.h"

static void fail(const char *message) {
    fprintf(stderr,"CFAllocator diagnostic: %s\n",message);
    exit(1);
}
static uint64_t physical_memory(void) {
    uint64_t n=0;
    size_t len=sizeof n;
    if (sysctlbyname("hw.memsize",&n,&len,NULL,0) || len!=sizeof n || !n)
        fail("actual host memory measurement failed");
    return n;
}
static void *allocate(CFIndex size, CFOptionFlags hint, void *info) {
    (void)hint;
    return size>0?nx_allocate(info,(size_t)size):NULL;
}
static void *reallocate(void *ptr, CFIndex size, CFOptionFlags hint, void *info) {
    (void)hint;
    return size>=0?nx_reallocate(info,ptr,(size_t)size):NULL;
}
static void deallocate(void *ptr, void *info) { nx_deallocate(info,ptr); }

int main(void) {
    uint64_t before=physical_memory();
    nx_alloc_state state={.limit=262144};
    CFAllocatorContext context={0};
    context.info=&state;
    context.allocate=allocate;
    context.reallocate=reallocate;
    context.deallocate=deallocate;
    CFAllocatorRef allocator=CFAllocatorCreate(kCFAllocatorDefault,&context);
    if (!allocator) fail("CFAllocatorCreate returned NULL");
    unsigned char *p=CFAllocatorAllocate(allocator,4096,0);
    if (!p) fail("explicit allocation failed");
    for (size_t i=0;i<4096;i++) p[i]=(unsigned char)(i*37u);
    unsigned char *grown=CFAllocatorReallocate(allocator,p,8192,0);
    if (!grown) fail("reallocation failed");
    for (size_t i=0;i<4096;i++) if (grown[i]!=(unsigned char)(i*37u)) fail("reallocation corrupted bytes");
    size_t calls_before_object=state.allocations;
    CFDataRef data=CFDataCreate(allocator,grown,4096);
    if (!data || CFDataGetLength(data)!=4096) fail("Core Foundation data creation failed");
    if (state.allocations<=calls_before_object) fail("CFData did not invoke custom allocation");
    if (memcmp(CFDataGetBytePtr(data),grown,4096)) fail("CFData verification failed");
    uint64_t during=physical_memory();
    CFRelease(data);
    CFAllocatorDeallocate(allocator,grown);
    CFRelease(allocator);
    if (state.live) fail("custom allocations not fully released");
    uint64_t after=physical_memory();
    printf("{\"mode\":\"CFAllocatorCreate local-only scope diagnostic\","
           "\"callbackAndDataTestsPassed\":true,\"remoteMemoryTested\":false,"
           "\"hostRAMExpansionProven\":false,\"guestOSRAMExpansionProven\":false,"
           "\"gpuMemoryExpansionProven\":false,\"defaultAllocatorChanged\":false,"
           "\"allocationCalls\":%zu,\"deallocationCalls\":%zu,\"reallocationCalls\":%zu,"
           "\"peakCustomPayloadBytes\":%zu,\"finalLiveCustomPayloadBytes\":%zu,"
           "\"physicalBytesBefore\":%" PRIu64 ",\"physicalBytesDuring\":%" PRIu64 ","
           "\"physicalBytesAfter\":%" PRIu64 ",\"hostCounterUnchanged\":%s}\n",
           state.allocations,state.deallocations,state.reallocations,state.peak,state.live,
           before,during,after,(before==during && during==after)?"true":"false");
    return 0;
}
