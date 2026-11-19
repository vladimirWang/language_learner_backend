-- 一次性修正历史数据：把现有启用方案中没有使用场景的阻断型语法要求降为提示。
-- 规则与保存时的校验保持一致：等级、判定规则、语法点存在性、题目承载。
WITH ranks(level, rank) AS (
    VALUES ('A1', 1), ('A2', 2), ('B1', 3), ('B2', 4), ('C1', 5), ('C2', 6)
),
grammar_points AS (
    SELECT DISTINCT lang, source_path, COALESCE(level::text, '') AS level
    FROM rag_chunks
    WHERE chunk_type = 'grammar'
),
grammar_levels AS (
    SELECT gp.lang, gp.source_path, MIN(r.rank) AS min_rank
    FROM grammar_points gp
    JOIN LATERAL (
        SELECT ranks.rank
        FROM unnest(string_to_array(gp.level::text, '-')) AS part
        JOIN ranks ON ranks.level = UPPER(TRIM(part))
    ) r ON TRUE
    GROUP BY gp.lang, gp.source_path
),
evaluated AS (
    SELECT
        sp.id,
        elem,
        ord,
        CASE
            WHEN COALESCE(elem->>'enforce', '') <> 'block' THEN FALSE
            WHEN jsonb_typeof(elem->'patterns') <> 'array'
                 OR jsonb_array_length(elem->'patterns') = 0 THEN TRUE
            WHEN NOT EXISTS (
                SELECT 1 FROM grammar_points gp
                WHERE gp.lang = sp.lang AND gp.source_path = elem->>'source_path'
            ) THEN TRUE
            WHEN COALESCE(pr.plan_rank, 0) > 0
                 AND gl.min_rank IS NOT NULL
                 AND gl.min_rank > pr.plan_rank THEN TRUE
            WHEN EXISTS (
                SELECT 1
                FROM speaking_exam_plan_items pi
                JOIN speaking_items si ON si.item_key = pi.item_key
                WHERE pi.plan_id = sp.id AND si.target_grammar <> '[]'::jsonb
            ) AND NOT EXISTS (
                SELECT 1
                FROM speaking_exam_plan_items pi
                JOIN speaking_items si ON si.item_key = pi.item_key
                CROSS JOIN LATERAL jsonb_array_elements_text(si.target_grammar) AS tag
                WHERE pi.plan_id = sp.id AND tag = elem->>'key'
            ) THEN TRUE
            ELSE FALSE
        END AS downgrade
    FROM speaking_exam_plans sp
    CROSS JOIN LATERAL jsonb_array_elements(sp.grammar_requirements)
        WITH ORDINALITY AS t(elem, ord)
    LEFT JOIN LATERAL (
        SELECT ranks.rank AS plan_rank
        FROM ranks
        WHERE ranks.level = UPPER(TRIM(sp.level::text))
    ) pr ON TRUE
    LEFT JOIN grammar_levels gl
        ON gl.lang = sp.lang AND gl.source_path = elem->>'source_path'
    WHERE sp.active
),
aggregated AS (
    SELECT id, jsonb_agg(
        CASE WHEN downgrade THEN jsonb_set(elem, '{enforce}', '"warn"'::jsonb) ELSE elem END
        ORDER BY ord
    ) AS requirements
    FROM evaluated
    GROUP BY id
)
UPDATE speaking_exam_plans sp
SET grammar_requirements = a.requirements
FROM aggregated a
WHERE sp.id = a.id
  AND sp.grammar_requirements IS DISTINCT FROM a.requirements;
