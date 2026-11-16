"""德语插件：名词词性 / 复数、可分动词、支配格与三四格变位。"""

from __future__ import annotations

from .base import LangPlugin


class DePlugin(LangPlugin):
    def __init__(self) -> None:
        super().__init__(
            lang="de",
            levels=frozenset({"A1", "A2", "B1", "B2"}),
            word_types=frozenset({"noun", "verb", "adj", "adv", "prep", "particle"}),
            form_types=frozenset({"possessive", "pronoun", "article", "verb_conj", "adj_decl"}),
            cases=frozenset({"nom", "dat", "acc"}),
            supports_separable=True,
        )

    def validate_vocab_specific(self, row: dict[str, str], where: str) -> list[str]:
        errors: list[str] = []
        word_type = row.get("word_type", "").strip()
        if word_type == "noun" and not row.get("gender", "").strip():
            errors.append(f"{where}: german noun requires gender (m/f/n)")
        if word_type != "noun" and row.get("gender", "").strip():
            errors.append(f"{where}: gender only allowed for nouns")
        if word_type != "verb" and row.get("separable", "").strip():
            errors.append(f"{where}: separable only allowed for verbs")
        return errors
