-- 单词配图：供幼儿英语单词题等场景直接下发图片地址。
-- 允许为空：绝大多数通用词条暂无配图，不阻塞既有写入；builder upsert 不覆盖该列。
ALTER TABLE vocab_words ADD COLUMN IF NOT EXISTS image_url TEXT;
