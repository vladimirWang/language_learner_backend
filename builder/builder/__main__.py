"""构建任务入口：python -m builder --lang de [--version 1.0.2]。"""

from __future__ import annotations

import argparse
import logging
import os
import sys
from datetime import datetime

from . import pipeline, vocab_sync
from .config import BuildError, Settings
from .db import Store


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
    parser.add_argument(
        "--export-vocab",
        action="store_true",
        help="把数据库词表导出为 content/<lang>/vocab.csv 快照与 manifest 后退出",
    )
    parser.add_argument(
        "--check-vocab",
        action="store_true",
        help="校验数据库与 content/<lang>/vocab.csv 的 lemma 是否一致；不一致时退出码为 1",
    )
    parser.add_argument(
        "--out-dir",
        help="导出目录，默认使用 content 目录",
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
        if args.export_vocab or args.check_vocab:
            return _sync_vocab(settings, langs, args, version)
        pipeline.run(settings, langs, version, import_vocab=args.import_vocab)
    except BuildError as exc:
        logging.error("build failed: %s", exc)
        return 1
    except Exception:  # 未预期错误；失败状态由 Go scheduler 依据退出码落库
        logging.exception("build crashed")
        return 1
    return 0


def _sync_vocab(settings: Settings, langs: list[str], args, version: str) -> int:
    """词库快照导出与一致性校验；不触发 embedding / 上传。"""
    store = Store(settings.pg_dsn)
    try:
        if args.export_vocab:
            manifest = vocab_sync.export_csv(store, settings, langs, args.out_dir, version)
            for lang, info in manifest["langs"].items():
                logging.info(
                    "vocab export %s rows=%d sha256=%s", lang, info["rows"], info["sha256"]
                )
            logging.info("vocab manifest version=%s", manifest["version"])
            return 0
        issues = vocab_sync.check(store, settings, langs)
    finally:
        store.close()

    if issues:
        for issue in issues:
            logging.error("vocab check: %s", issue)
        return 1
    logging.info("vocab check ok langs=%s", ",".join(langs))
    return 0


if __name__ == "__main__":
    sys.exit(main())
