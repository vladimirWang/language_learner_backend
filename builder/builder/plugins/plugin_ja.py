"""日语插件：动词活用、助词、音读训读与振假名。"""

from __future__ import annotations

import json

from .base import LangPlugin


class JaPlugin(LangPlugin):
    def __init__(self) -> None:
        super().__init__(
            lang="ja",
            levels=frozenset({"N5", "N4", "N3"}),
            word_types=frozenset({"noun", "verb", "adj", "adv", "particle"}),
            form_types=frozenset({"verb_conj", "adj_conj", "particle"}),
            cases=frozenset(),
        )

    def validate_vocab_specific(self, row: dict[str, str], where: str) -> list[str]:
        errors: list[str] = []
        if row.get("gender", "").strip():
            errors.append(f"{where}: japanese words must not set gender")

        word_type = row.get("word_type", "").strip()
        extra_raw = row.get("extra_json", "").strip()
        extra: dict = {}
        if extra_raw:
            try:
                extra = json.loads(extra_raw)
            except json.JSONDecodeError:
                return errors  # 基类已报错，避免重复
        if not isinstance(extra, dict):
            return errors

        if word_type == "noun" and not extra.get("kana"):
            errors.append(f"{where}: japanese noun requires extra_json.kana")
        if word_type == "verb":
            verb_class = extra.get("verb_class")
            if verb_class not in {"godan", "ichidan", "suru", "kuru"}:
                errors.append(f"{where}: japanese verb requires extra_json.verb_class (godan/ichidan/suru/kuru)")
        return errors
