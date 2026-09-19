/* Documentation and independent assembler-test input, not a linked OS kernel. */
fill:
    str x0, [x1], #8
    eor x0, x0, x0, lsl #13
    eor x0, x0, x0, lsr #7
    eor x0, x0, x0, lsl #17
    subs x2, x2, #1
    b.ne fill
    hvc #0
check:
    ldr x4, [x1], #8
    eor x4, x4, x0
    orr x5, x5, x4
    eor x0, x0, x0, lsl #13
    eor x0, x0, x0, lsr #7
    eor x0, x0, x0, lsl #17
    subs x2, x2, #1
    b.ne check
    hvc #0
