"""听力解析 CLI：backend 与 desktop 共用，可直接打包为独立二进制。

与 ``builder.listen`` 的区别：只依赖 ``WORK_DIR``，不加载完整的
``Settings``（不要求 CONTENT_DIR / PG_DSN），因此可在任意目录启动。
"""

from __future__ import annotations

import argparse
import json
import logging
import os
import sys
from pathlib import Path

from . import listening


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="listen-parser",
        description="听力素材导入：下载 → 转写 → 按题切分 → 抽帧 OCR",
    )
    parser.add_argument("--url", required=True, help="视频/音频页面地址")
    parser.add_argument("--exam-id", required=True, help="输出目录名，如 goethe-a1-hoeren-01")
    parser.add_argument("--lang", default="de", help="语种，默认 de")
    parser.add_argument("--target-lang", default="de", help="转写语言，默认 de")
    parser.add_argument("--answers", help="官方答案 CSV：题号,答案")
    parser.add_argument("--cookies", help="yt-dlp cookies 文件（B 站登录态）")
    parser.add_argument("--model", default=os.getenv("WHISPER_MODEL", "small"), help="whisper 模型")
    parser.add_argument("--profile", default="", help="考试规则 id，如 goethe-a1；留空使用通用规则")
    return parser


def main(argv: list[str] | None = None) -> int:
    logging.basicConfig(
        level=os.getenv("LOG_LEVEL", "INFO").upper(),
        format="%(asctime)s %(levelname)s %(name)s %(message)s",
    )
    args = build_parser().parse_args(argv)

    # 打包后没有 builder 目录可参考，工作目录必须由调用方显式指定。
    work_dir = Path(os.getenv("WORK_DIR", "") or "work").resolve()
    try:
        manifest = listening.import_from_url(
            args.url,
            work_dir,
            args.exam_id,
            lang=args.lang,
            answers=Path(args.answers) if args.answers else None,
            cookies=Path(args.cookies) if args.cookies else None,
            model_name=args.model,
            target_lang=args.target_lang,
            profile_id=args.profile or None,
        )
    except listening.ListeningError as exc:
        logging.error("import failed: %s", exc)
        return 1
    except Exception:
        logging.exception("import crashed")
        return 1

    print(json.dumps({"manifest": str(manifest)}, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    sys.exit(main())
