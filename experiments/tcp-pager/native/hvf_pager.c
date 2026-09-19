/*
 * Isolated Apple-silicon CPU paging experiment. Public Hypervisor entitlement
 * only; no macOS boot, firmware, private API, GPU, kernel extension or SIP change.
 * stdout/stdin are a bounded binary protocol to the Go page broker.
 */
#include <Hypervisor/Hypervisor.h>
#include <libkern/OSCacheControl.h>
#include <errno.h>
#include <signal.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mman.h>
#include <time.h>
#include <unistd.h>
#include "guest_code.h"

#define PAGE 16384u
#define CODE_GPA UINT64_C(0x10000)
#define DATA_GPA UINT64_C(0x200000)
#define MAX_SLOTS 16
typedef struct { void *mem; uint32_t page; bool valid, dirty; } slot_t;
static slot_t slots[MAX_SLOTS];
static unsigned next_slot, slot_count, page_count, resident, peak;
static uint64_t faults, evictions, verified;
static hv_vcpu_t cpu;
static hv_vcpu_exit_t *exit_info;

static void die(const char *msg) { fprintf(stderr, "HVF pager: %s\n", msg); exit(1); }
static void hvcheck(hv_return_t r, const char *op) {
    if (r != HV_SUCCESS) {
        fprintf(stderr, "HVF pager: %s failed (0x%x); no security bypass attempted\n", op, (unsigned)r);
        exit(1);
    }
}
static void transfer(int fd, void *buf, size_t n, bool writing) {
    unsigned char *p = buf;
    while (n) {
        ssize_t r = writing ? write(fd, p, n) : read(fd, p, n);
        if (r < 0 && errno == EINTR) continue;
        if (r <= 0) die("broker disconnected; refusing to continue");
        p += r; n -= (size_t)r;
    }
}
static void be32(unsigned char *p, uint32_t x) {
    for (int i=3;i>=0;i--) { p[i]=(unsigned char)x; x >>= 8; }
}
static void be64(unsigned char *p, uint64_t x) {
    for (int i=7;i>=0;i--) { p[i]=(unsigned char)x; x >>= 8; }
}
static void remote_page(char op, uint32_t page, void *buf) {
    unsigned char h[5], status;
    h[0]=(unsigned char)op; be32(h+1,page);
    transfer(STDOUT_FILENO,h,sizeof h,true);
    if (op=='P') transfer(STDOUT_FILENO,buf,PAGE,true);
    transfer(STDIN_FILENO,&status,1,false);
    if (status) die("broker rejected page");
    if (op=='G') transfer(STDIN_FILENO,buf,PAGE,false);
}
static void *allocate_page(void) {
    void *p=mmap(NULL,PAGE,PROT_READ|PROT_WRITE,MAP_PRIVATE|MAP_ANON,-1,0);
    if (p==MAP_FAILED) die("bounded local allocation failed");
    return p;
}
static void remove_slot(slot_t *s) {
    if (!s->valid) return;
    /* Single vCPU, currently stopped. Unmap before transferring authoritative data. */
    hvcheck(hv_vm_unmap(DATA_GPA+(uint64_t)s->page*PAGE,PAGE),"unmap");
    if (s->dirty) remote_page('P',s->page,s->mem);
    s->valid=false; s->dirty=false; resident--;
}
static void fault_page(uint64_t address, bool writing) {
    if (address<DATA_GPA || address>=DATA_GPA+(uint64_t)page_count*PAGE)
        die("fault outside experimental data range");
    uint32_t page=(uint32_t)((address-DATA_GPA)/PAGE);
    for (unsigned i=0;i<slot_count;i++)
        if (slots[i].valid && slots[i].page==page) die("unexpected fault on resident page");
    slot_t *s=&slots[next_slot++ % slot_count];
    if (s->valid) { remove_slot(s); evictions++; }
    remote_page('G',page,s->mem);
    hvcheck(hv_vm_map(s->mem,DATA_GPA+(uint64_t)page*PAGE,PAGE,
                     HV_MEMORY_READ|HV_MEMORY_WRITE),"map data");
    s->page=page; s->valid=true; s->dirty=writing;
    resident++; if (resident>peak) peak=resident;
    faults++;
    /* Do not advance PC: retry the actual load/store that caused the fault. */
}
static uint64_t seed(unsigned page) {
    return UINT64_C(0x9e3779b97f4a7c15) ^
           ((uint64_t)page+1)*UINT64_C(0xd1b54a32d192ed03);
}
static void run_page(unsigned page, bool writing) {
    hvcheck(hv_vcpu_set_reg(cpu,HV_REG_PC,CODE_GPA+(writing?0:128)),"set PC");
    hvcheck(hv_vcpu_set_reg(cpu,HV_REG_X0,seed(page)),"set seed");
    hvcheck(hv_vcpu_set_reg(cpu,HV_REG_X1,DATA_GPA+(uint64_t)page*PAGE),"set pointer");
    hvcheck(hv_vcpu_set_reg(cpu,HV_REG_X2,PAGE/8),"set count");
    hvcheck(hv_vcpu_set_reg(cpu,HV_REG_X5,0),"set comparison");
    for (unsigned exits=0;exits<16;exits++) {
        hvcheck(hv_vcpu_run(cpu),"run");
        if (exit_info->reason!=HV_EXIT_REASON_EXCEPTION)
            die("unexpected VM exit; no synthetic success");
        uint64_t esr=exit_info->exception.syndrome;
        unsigned ec=(unsigned)(esr>>26)&63u;
        if (ec==0x16) { /* HVC64 completion */
            uint64_t remaining, mismatch;
            hvcheck(hv_vcpu_get_reg(cpu,HV_REG_X2,&remaining),"get count");
            hvcheck(hv_vcpu_get_reg(cpu,HV_REG_X5,&mismatch),"get comparison");
            if (remaining || (!writing && mismatch)) die("guest data comparison failed");
            if (!writing) verified+=PAGE;
            return;
        }
        unsigned dfsc=(unsigned)esr&63u;
        if ((ec==0x24||ec==0x25) && dfsc>=4 && dfsc<=7) {
            if (((esr>>6)&1u)!=(unsigned)writing) die("unexpected fault access type");
            fault_page(exit_info->exception.physical_address,writing);
        } else {
            fprintf(stderr,"HVF pager: unsupported syndrome 0x%llx\n",(unsigned long long)esr);
            die("only translation faults in the fixed workload are handled");
        }
    }
    die("VM-exit limit exceeded");
}
static unsigned parse(const char *s, unsigned max) {
    char *end=NULL; errno=0; unsigned long n=strtoul(s,&end,10);
    if (errno || !*s || *end || n<1 || n>max) die("invalid bounded argument");
    return (unsigned)n;
}
int main(int argc, char **argv) {
    if (argc!=3 && argc!=4) die("launch via nexal-pager, not directly");
    page_count=parse(argv[1],256); slot_count=parse(argv[2],MAX_SLOTS);
    unsigned hold_ms=argc==4?parse(argv[3],2000):0;
    if (page_count<=slot_count || sysconf(_SC_PAGESIZE)!=PAGE) die("unsupported page/cache configuration");
    signal(SIGPIPE,SIG_IGN);
    alarm(90); /* Independent fail-stop watchdog, including a stuck guest. */
    void *code=allocate_page();
    memcpy(code,fill_code,sizeof fill_code);
    memcpy((unsigned char *)code+128,check_code,sizeof check_code);
    sys_icache_invalidate(code,PAGE);
    hvcheck(hv_vm_create(NULL),"create VM");
    hvcheck(hv_vm_map(code,CODE_GPA,PAGE,HV_MEMORY_READ|HV_MEMORY_EXEC),"map code");
    hvcheck(hv_vcpu_create(&cpu,&exit_info,NULL),"create vCPU");
    hvcheck(hv_vcpu_set_reg(cpu,HV_REG_CPSR,0x3c5),"set EL1h");
    /* MMU disabled: guest virtual addresses are the chosen guest physical addresses. */
    hvcheck(hv_vcpu_set_sys_reg(cpu,HV_SYS_REG_SCTLR_EL1,UINT64_C(0x30d00800)),"disable guest MMU");
    hvcheck(hv_vcpu_set_vtimer_mask(cpu,true),"mask guest timer");
    for (unsigned i=0;i<slot_count;i++) slots[i].mem=allocate_page();
    for (unsigned p=0;p<page_count;p++) run_page(p,true);
    /* Flush and unmap every frame: verification must fetch every page over TCP. */
    for (unsigned i=0;i<slot_count;i++) remove_slot(&slots[i]);
    for (unsigned p=page_count;p>0;p--) run_page(p-1,false);
    /* Optional acceptance observation window: vCPU is stopped, final cache
     * pages remain mapped. This does not add memory to any OS allocator. */
    if (hold_ms) {
        struct timespec remaining = { (time_t)(hold_ms/1000), (long)(hold_ms%1000)*1000000L };
        while (nanosleep(&remaining,&remaining)<0) {
            if (errno!=EINTR) die("observation wait failed");
        }
    }
    for (unsigned i=0;i<slot_count;i++) remove_slot(&slots[i]);
    hvcheck(hv_vcpu_destroy(cpu),"destroy vCPU");
    hvcheck(hv_vm_unmap(CODE_GPA,PAGE),"unmap code");
    hvcheck(hv_vm_destroy(),"destroy VM");
    for (unsigned i=0;i<slot_count;i++) munmap(slots[i].mem,PAGE);
    munmap(code,PAGE);
    unsigned char done[33];done[0]='D';
    be64(done+1,faults);be64(done+9,evictions);be64(done+17,verified);be64(done+25,peak);
    transfer(STDOUT_FILENO,done,sizeof done,true);
    return 0;
}
