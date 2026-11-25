"""PyInstaller 入口：把 builder.listen_cli 打成独立二进制。"""

from builder.listen_cli import main

if __name__ == "__main__":
    raise SystemExit(main())
