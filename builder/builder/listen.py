"""兼容入口：``python -m builder.listen`` 与 ``listen-parser`` 共用实现。"""

from __future__ import annotations

import sys

from .listen_cli import main

if __name__ == "__main__":
    sys.exit(main())
