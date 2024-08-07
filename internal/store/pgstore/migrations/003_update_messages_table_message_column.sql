ALTER TABLE messages DROP column messages;
ALTER TABLE messages ADD COLUMN message VARCHAR(255) NOT NULL;
---- create above / drop below ----
ALTER TABLE messages DROP COLUMN message;
ALTER TABLE messages ADD COLUMN messages VARCHAR(255) NOT NULL;

