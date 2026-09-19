/* Generated from the adjacent guest.S, independently checked by the test tool.
 * Fixed workload only. No arbitrary guest payloads or firmware are loaded. */
#ifndef NEXAL_GUEST_CODE_H
#define NEXAL_GUEST_CODE_H
#include <stdint.h>
static const uint32_t fill_code[] = {
    0xf8008420, /* str x0, [x1], #8 */
    0xca003400, /* eor x0, x0, x0, lsl #13 */
    0xca401c00, /* eor x0, x0, x0, lsr #7 */
    0xca004400, /* eor x0, x0, x0, lsl #17 */
    0xf1000442, /* subs x2, x2, #1 */
    0x54ffff61, /* b.ne fill */
    0xd4000002  /* hvc #0 */
};
static const uint32_t check_code[] = {
    0xf8408424, /* ldr x4, [x1], #8 */
    0xca000084, /* eor x4, x4, x0 */
    0xaa0400a5, /* orr x5, x5, x4 */
    0xca003400,
    0xca401c00,
    0xca004400,
    0xf1000442,
    0x54ffff21, /* b.ne check */
    0xd4000002
};
#endif
