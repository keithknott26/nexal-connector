"""Fixed, digest-pinned entry point for `python -I /installed/nexal_mlx_entry.py`.

Only the owner-installed adjacent package is added; no job-supplied module path.
"""

from pathlib import Path
import sys

sys.path.insert(0, str(Path(__file__).resolve().parent))
from nexal_mlx.cli import main  # noqa: E402

raise SystemExit(main())
