"""构建任务入口：python -m builder --lang de [--version 1.0.2]。"""

from __future__ import annotations

import argparse
import logging
import os
import sys
from datetime import datetime

from . import pipeline
from .config import BuildError, Settings


def main(argv: list[str] | None = None) -> int:
    logging.basicConfig(
        level=os.getenv("LOG_LEVEL", "INFO").upper(),
        format="%(asctime)s %(levelname)s %(name)s %(message)s",
    )

    parser = argparse.ArgumentParser(prog="builder", description="素材构建任务（一次性）")
    parser.add_argument("--lang", action="append", dest="langs", help="语种，可重复；缺省构建全部")
    parser.add_argument("--version", help="素材版本号；缺省按时间生成")
    parser.add_argument(
        "--import-vocab",
        action="store_true",
        help="构建前先把 content/<lang>/vocab.csv 导入数据库；默认只读数据库词表",
    )
    args = parser.parse_args(argv)

    settings = Settings.from_env()
    langs = pipeline.resolve_langs(settings, args.langs)
    if not langs:
        logging.error("no langs to build under %s", settings.content_dir)
        return 1
    version = args.version or datetime.now().strftime("%Y%m%d-%H%M%S")

    logging.info("build start langs=%s version=%s import_vocab=%s", langs, version, args.import_vocab)
    try:
        pipeline.run(settings, langs, version, import_vocab=args.import_vocab)
    except BuildError as exc:
        logging.error("build failed: %s", exc)
        return 1
    except Exception:  # 未预期错误；失败状态由 Go scheduler 依据退出码落库
        logging.exception("build crashed")
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
