DELETE FROM speaking_items
WHERE item_key IN (
    'de-a1-presentation-familie',
    'de-a1-presentation-tagesablauf',
    'de-a1-discussion-fest',
    'de-a1-discussion-wochenende',
    'de-a2-presentation-wohnung',
    'de-a2-presentation-reise',
    'de-a2-discussion-geschenk',
    'de-a2-discussion-verkehr'
);
