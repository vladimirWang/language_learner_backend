"""语种插件基类：把语种特有校验收敛在插件内，主流程不写 if/else。"""

from __future__ import annotations

import json
from dataclasses import dataclass


@dataclass(frozen=True)
class LangPlugin:
    lang: str
    levels: frozenset[str]
    word_types: frozenset[str]
    form_types: frozenset[str]
    cases: frozenset[str]
    supports_separable: bool = False

    def validate_vocab(self, row: dict[str, str], where: str) -> list[str]:
        """校验 vocab.csv 一行，返回错误描述列表。"""
        errors: list[str] = []
        if row.get("lang", "").strip() != self.lang:
            errors.append(f"{where}: lang must be {self.lang}")
        if not row.get("lemma", "").strip():
            errors.append(f"{where}: lemma is empty")
        if row.get("word_type", "").strip() not in self.word_types:
            errors.append(f"{where}: invalid word_type {row.get('word_type')!r}")
        if row.get("level", "").strip() not in self.levels:
            errors.append(f"{where}: invalid level {row.get('level')!r}")

        gender = row.get("gender", "").strip()
        if gender and gender not in {"m", "f", "n"}:
            errors.append(f"{where}: invalid gender {gender!r}")

        trans_intr = row.get("trans_intr", "").strip()
        if trans_intr and trans_intr not in {"vt", "vi"}:
            errors.append(f"{where}: invalid trans_intr {trans_intr!r}")

        separable = row.get("separable", "").strip().lower()
        if separable:
            if not self.supports_separable:
                errors.append(f"{where}: separable is not supported for {self.lang}")
            elif separable not in {"true", "false", "1", "0", "yes", "no"}:
                errors.append(f"{where}: invalid separable {separable!r}")

        extra = row.get("extra_json", "").strip()
        if extra:
            try:
                parsed = json.loads(extra)
            except json.JSONDecodeError as exc:
                errors.append(f"{where}: extra_json is not valid json ({exc.msg})")
            else:
                if not isinstance(parsed, dict):
                    errors.append(f"{where}: extra_json must be a json object")

        errors.extend(self.validate_vocab_specific(row, where))
        return errors

    def validate_vocab_specific(self, row: dict[str, str], where: str) -> list[str]:
        return []

    def validate_form(self, row: dict[str, str], where: str) -> list[str]:
        """校验 grammar_forms.csv 一行。"""
        errors: list[str] = []
        if row.get("lang", "").strip() != self.lang:
            errors.append(f"{where}: lang must be {self.lang}")
        if not row.get("root", "").strip():
            errors.append(f"{where}: root is empty")
        if row.get("form_type", "").strip() not in self.form_types:
            errors.append(f"{where}: invalid form_type {row.get('form_type')!r}")
        if not row.get("form", "").strip():
            errors.append(f"{where}: form is empty")

        case_tag = row.get("case_tag", "").strip()
        if case_tag and case_tag not in self.cases:
            errors.append(f"{where}: invalid case_tag {case_tag!r}")
        number_tag = row.get("number_tag", "").strip()
        if number_tag and number_tag not in {"sg", "pl"}:
            errors.append(f"{where}: invalid number_tag {number_tag!r}")
        gender = row.get("gender", "").strip()
        if gender and gender not in {"m", "f", "n"}:
            errors.append(f"{where}: invalid gender {gender!r}")
        return errors
