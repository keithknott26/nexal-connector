# internal/qr test fixtures

## golden-masks.txt

Module matrices for four payloads at error correction level M, byte mode, each
encoded with all eight mask patterns FORCED. They were produced by the python
`qrcode` package 8.2 — an INDEPENDENT implementation of ISO/IEC 18004 — and NOT
by the Go package under test. `TestGoldenMatricesMatchIndependentEncoder`
requires byte-for-byte agreement, so a bug in the Go encoder cannot make the test
pass by agreeing with itself.

Forcing the mask separates two questions that a single "encode and compare" would
confuse: whether the symbol is built correctly, and whether the mask-selection
policy picks the same mask. Agreement on all eight masks answers the first;
`TestChosenMaskIsTheLowestPenalty` answers the second against this package's own
penalty implementation, which was separately confirmed to produce identical
scores to python's for every mask of every payload in the verification sweep
below.

Regenerate (requires `pip install qrcode`; NOT needed to run `go test`, and no
test shells out to python):

```python
import qrcode, qrcode.util as u
from qrcode.constants import ERROR_CORRECT_M
q = qrcode.QRCode(error_correction=ERROR_CORRECT_M, mask_pattern=mask, border=0)
q.add_data(u.QRData(payload, mode=u.MODE_8BIT_BYTE))  # one byte segment, no
q.make(fit=True)                                      # mixed-mode optimisation
rows = [''.join('1' if v else '0' for v in row) for row in q.get_matrix()]
```

`u.QRData(..., mode=MODE_8BIT_BYTE)` matters. `add_data(payload)` alone lets
python split the input into mixed numeric/alphanumeric/byte segments, which is a
legal encoding this package deliberately does not implement, and the matrices
then differ for reasons that are not bugs.

## Verification performed beyond these fixtures

Recorded here because the evidence is not reproducible inside `go test`:

- 28 random payloads from 1 to 2331 bytes (versions 1 through 40), each encoded
  with all eight masks: 224 symbols, ZERO module differences from python
  `qrcode`, and identical mask penalty scores for all 224.
- 13 ASCII payloads from 11 to 2331 bytes decoded with OpenCV 5.0's
  `QRCodeDetector` (a third implementation, and a real-world decoder rather than
  an encoder): 10 decoded byte-identically. The other 3 also failed to decode
  when OpenCV was fed python `qrcode`'s OWN matrix for the same payload, so those
  are detector limitations on large symbols rather than encoder faults.
- The pairing-shaped payload (291 bytes, version 13) decodes correctly through
  OpenCV.
