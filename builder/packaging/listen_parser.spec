# -*- mode: python ; coding: utf-8 -*-
"""PyInstaller 配置：生成单文件 listen-parser。

用法（在 builder 目录）：
    ./scripts/build_listen_parser.sh
"""

import os
from pathlib import Path

from PyInstaller.utils.hooks import collect_all

PACKAGING_DIR = Path(SPECPATH)
BUILDER_ROOT = PACKAGING_DIR.parent

datas = []
binaries = []
hiddenimports = []
# faster-whisper 的运行时依赖包含原生库与数据文件，需整体收集。
for package in ("faster_whisper", "ctranslate2", "av", "tokenizers", "huggingface_hub", "tqdm", "numpy"):
    try:
        package_datas, package_binaries, package_hidden = collect_all(package)
    except Exception:
        continue
    datas += package_datas
    binaries += package_binaries
    hiddenimports += package_hidden

a = Analysis(
    [str(PACKAGING_DIR / "listen_entry.py")],
    pathex=[str(BUILDER_ROOT)],
    binaries=binaries,
    datas=datas,
    hiddenimports=hiddenimports,
    hookspath=[],
    runtime_hooks=[],
    # 听力解析不需要 PDF / 数据库 / 对象存储依赖。
    excludes=["pymupdf", "psycopg", "pgvector", "minio", "markdown_it"],
    noarchive=False,
)
pyz = PYZ(a.pure)

exe = EXE(
    pyz,
    a.scripts,
    a.binaries,
    a.datas,
    [],
    name="listen-parser",
    debug=False,
    bootloader_ignore_signals=False,
    strip=False,
    upx=False,
    console=True,
)
