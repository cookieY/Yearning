ALTER TABLE shop.orders ADD COLUMN remark VARCHAR(128) NULL;
UPDATE shop.orders SET remark = 'backfilled' WHERE id = 1;
