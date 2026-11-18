-- 口语追问/搭档回应的兜底话术：可由管理台维护，表为空时后端使用代码内默认值。
CREATE TABLE IF NOT EXISTS speaking_reply_templates (
    id          BIGSERIAL PRIMARY KEY,
    lang        TEXT NOT NULL DEFAULT 'de',
    exam_part   TEXT NOT NULL,               -- part1_intro / part2_presentation / part3_discussion
    role        TEXT NOT NULL,               -- examiner / partner
    text        TEXT NOT NULL,
    active      BOOLEAN NOT NULL DEFAULT true,
    sort_order  INT NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ DEFAULT now(),
    updated_at  TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_speaking_reply_templates_filter
    ON speaking_reply_templates (lang, exam_part, role, active, sort_order);

INSERT INTO speaking_reply_templates (exam_part, role, text, sort_order)
SELECT v.exam_part, v.role, v.text, v.sort_order
FROM (VALUES
    ('part1_intro', 'examiner', 'Können Sie das bitte noch etwas genauer sagen?', 1),
    ('part1_intro', 'examiner', 'Und warum ist das für Sie wichtig?', 2),
    ('part1_intro', 'examiner', 'Wie oft machen Sie das?', 3),
    ('part1_intro', 'examiner', 'Haben Sie ein Beispiel dafür?', 4),
    ('part1_intro', 'examiner', 'Und wie war das früher bei Ihnen?', 5),
    ('part1_intro', 'examiner', 'Wer macht das bei Ihnen zu Hause?', 6),
    ('part1_intro', 'examiner', 'Was gefällt Ihnen daran am besten?', 7),
    ('part1_intro', 'examiner', 'Gibt es dabei auch etwas Schwieriges?', 8),
    ('part1_intro', 'examiner', 'Wie lange machen Sie das schon?', 9),
    ('part1_intro', 'examiner', 'Mit wem machen Sie das meistens?', 10),

    ('part2_presentation', 'examiner', 'Was ist dabei der wichtigste Punkt für Sie?', 11),
    ('part2_presentation', 'examiner', 'Können Sie dazu ein Beispiel geben?', 12),
    ('part2_presentation', 'examiner', 'Welche Erfahrung haben Sie damit gemacht?', 13),
    ('part2_presentation', 'examiner', 'Was würden Sie anderen in dieser Situation empfehlen?', 14),
    ('part2_presentation', 'examiner', 'Gibt es dabei auch Nachteile?', 15),
    ('part2_presentation', 'examiner', 'Wie hat sich das in den letzten Jahren verändert?', 16),
    ('part2_presentation', 'examiner', 'Welche Rolle spielt das für Ihre Familie?', 17),
    ('part2_presentation', 'examiner', 'Was könnte man in Zukunft besser machen?', 18),

    ('part3_discussion', 'examiner', 'Das ist ein guter Punkt. Wie sehen Sie das?', 19),
    ('part3_discussion', 'examiner', 'Können Sie das noch etwas erklären?', 20),
    ('part3_discussion', 'examiner', 'Was würden Sie in dieser Situation machen?', 21),
    ('part3_discussion', 'examiner', 'Ich sehe das etwas anders. Was meinen Sie dazu?', 22),
    ('part3_discussion', 'examiner', 'Und wie wichtig ist Ihnen das persönlich?', 23),
    ('part3_discussion', 'examiner', 'Was spricht aus Ihrer Sicht dagegen?', 24),
    ('part3_discussion', 'examiner', 'Haben Sie das selbst schon erlebt?', 25),
    ('part3_discussion', 'examiner', 'Wie würden Ihre Freunde darüber denken?', 26),

    ('part3_discussion', 'partner', 'Das sehe ich ähnlich. Aber ich finde, es gibt auch Nachteile.', 31),
    ('part3_discussion', 'partner', 'Interessant. Bei mir ist das ein bisschen anders.', 32),
    ('part3_discussion', 'partner', 'Guter Punkt. Was würden Sie in dieser Situation machen?', 33),
    ('part3_discussion', 'partner', 'Ich stimme Ihnen teilweise zu. Wie wichtig ist das für Sie?', 34),
    ('part3_discussion', 'partner', 'Das kann ich gut verstehen. Haben Sie ein Beispiel dafür?', 35),
    ('part3_discussion', 'partner', 'Da bin ich anderer Meinung. Wie würden Sie das begründen?', 36),
    ('part3_discussion', 'partner', 'Das überrascht mich. Wie kam es dazu?', 37),
    ('part3_discussion', 'partner', 'Bei uns ist das ähnlich. Was gefällt Ihnen daran am besten?', 38),
    ('part3_discussion', 'partner', 'Das finde ich spannend. Wie oft machen Sie das?', 39),
    ('part3_discussion', 'partner', 'Ehrlich gesagt sehe ich das anders. Warum ist das für Sie so wichtig?', 40)
) AS v(exam_part, role, text, sort_order)
WHERE NOT EXISTS (SELECT 1 FROM speaking_reply_templates);
