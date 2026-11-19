UPDATE speaking_items
SET target_grammar = '[]'::jsonb
WHERE item_key IN (
    'de-a1-intro-name', 'de-a1-intro-alter', 'de-a1-intro-herkunft',
    'de-a1-intro-wohnen', 'de-a1-intro-sprachen', 'de-a1-intro-arbeit',
    'de-a1-intro-freizeit', 'de-a1-qa-essen', 'de-a1-qa-einkaufen',
    'de-a1-qa-wochenende', 'de-a1-gen-1795031402508630003-4',
    'de-a1-qa-geschwister', 'de-a1-request-buch', 'de-a1-request-kino',
    'de-a1-request-wort', 'de-a1-gen-1795031402508630003-5',
    'de-a1-discussion-wochenende',
    'de-a2-intro-geburtstag', 'de-a2-intro-wohnort', 'de-a2-intro-hobby',
    'de-a2-intro-beruf', 'de-a2-plan-weekend', 'de-a2-mono-geld',
    'de-a2-reise', 'de-a2-essen', 'de-a2-termin',
    'de-a2-presentation-wohnung', 'de-a2-presentation-reise',
    'de-a2-plan-ausflug', 'de-a2-discussion-geschenk',
    'de-a2-discussion-verkehr',
    'de-b1-intro-vorstellung', 'de-b1-intro-freizeit',
    'de-b1-presentation-umwelt', 'de-b1-presentation-medien',
    'de-b1-discussion-reisen', 'de-b1-discussion-arbeit',
    'de-b2-intro-beruf', 'de-b2-intro-lernen',
    'de-b2-presentation-digitalisierung', 'de-b2-presentation-mobilitaet',
    'de-b2-discussion-homeoffice', 'de-b2-discussion-ernaehrung'
);
