"""构建流水线编排：增量比对 → 切片 → embedding → pgvector → 真值库 → 快照 → 上传。"""

from __future__ import annotations

import logging
import os

from . import ground_truth, snapshot
from .chunking import chunk_entry
from .config import BuildError, Settings
from .db import Store
from .embedding import Embedder
from .index import desired_sources
from .plugins import get_plugin
from .upload import Uploader, md5_file

logger = logging.getLogger(__name__)


def run(settings: Settings, langs: list[str], version: str, import_vocab: bool = False) -> None:
    for lang in langs:
        get_plugin(lang)

    store = Store(settings.pg_dsn)
    try:
        uploader = Uploader(settings)
        for lang in langs:
            lang_version = version if len(langs) == 1 else f"{version}-{lang}"
            try:
                _run_lang(settings, store, uploader, lang, lang_version, import_vocab)
            except Exception as exc:
                store.fail_build(lang_version, str(exc))
                raise
    finally:
        store.close()


def _run_lang(
    settings: Settings,
    store: Store,
    uploader: Uploader,
    lang: str,
    version: str,
    import_vocab: bool,
) -> None:
    source_commit = os.getenv("SOURCE_COMMIT", "").strip()
    store.record_build(version, lang, "building", source_commit)
    store.log(version, "parse", "start lang=" + lang)

    sources = desired_sources(settings, lang)
    existing = store.source_hashes(lang)
    desired = {source.path: source.source_hash for source in sources}
    deleted = set(existing) - set(desired)
    changed = [source for source in sources if existing.get(source.path) != source.source_hash]
    store.log(
        version,
        "parse",
        f"files={len(sources)} changed={len(changed)} deleted={len(deleted)}",
    )

    chunks = []
    for source in changed:
        source_chunks = chunk_entry(settings, source)
        if not source_chunks:
            logger.warning("no chunks extracted from %s", source.path)
        chunks.extend(source_chunks)

    vectors: list[list[float]] = []
    if chunks:
        store.log(version, "embed", f"chunks={len(chunks)}")
        embedder = Embedder(settings)
        try:
            vectors = embedder.embed([chunk.content for chunk in chunks])
        finally:
            embedder.close()

    store.replace_chunks(deleted | {source.path for source in changed}, chunks, vectors, version)
    store.create_vector_index()
    store.log(
        version,
        "index",
        f"inserted={len(chunks)} removed_sources={len(deleted) + len(changed)}",
    )

    # 词库以数据库为唯一真值：默认不回写，只有 --import-vocab 时才用 CSV 初始化/更新。
    if import_vocab:
        csv_rows = ground_truth.load_vocab_csv(settings, lang)
        store.log(version, "vocab", f"imported={store.upsert_vocab_words(lang, csv_rows)}")

    truth = ground_truth.build(settings, store, lang, version)
    store.log(version, "parse", f"ground_truth vocab={truth.vocab_count} forms={truth.form_count}")

    snap = snapshot.export(store, lang, version, settings.work_dir / version / lang)
    store.log(version, "snapshot", f"chunks={snap.chunk_count}")

    db_key = uploader.upload(truth.path, f"{lang}/{version}/ground_truth.db")
    vector_key = uploader.upload(snap.vectors_path, f"{lang}/{version}/vectors.jsonl")
    uploader.upload(snap.manifest_path, f"{lang}/{version}/manifest.json")

    store.finish_build(
        version=version,
        lang=lang,
        db_key=db_key,
        vector_key=vector_key,
        db_md5=md5_file(truth.path),
        vector_md5=md5_file(snap.vectors_path),
    )
    store.log(version, "upload", f"db={db_key} vector={vector_key}")
    store.log(version, "done", "build finished")
    logger.info("build finished: lang=%s version=%s", lang, version)


def resolve_langs(settings: Settings, requested: list[str] | None) -> list[str]:
    if requested:
        return requested
    from .plugins import SUPPORTED_LANGS

    return [lang for lang in SUPPORTED_LANGS if (settings.content_dir / lang).is_dir()]


__all__ = ["run", "resolve_langs", "BuildError"]
