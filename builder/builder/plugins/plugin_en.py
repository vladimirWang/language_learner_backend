"""英语插件：时态变形、单复数、冠词与代词。"""

from __future__ import annotations

from .base import LangPlugin


class EnPlugin(LangPlugin):
    def __init__(self) -> None:
        super().__init__(
            lang="en",
            levels=frozenset({"A1", "A2", "B1", "B2"}),
            word_types=frozenset({"noun", "verb", "adj", "adv", "prep", "particle"}),
            form_types=frozenset({"pronoun", "verb_conj", "article", "plural"}),
            cases=frozenset({"subjective", "objective", "possessive"}),
        )

    def validate_vocab_specific(self, row: dict[str, str], where: str) -> list[str]:
        if row.get("gender", "").strip():
            return [f"{where}: english words must not set gender"]
        return []
