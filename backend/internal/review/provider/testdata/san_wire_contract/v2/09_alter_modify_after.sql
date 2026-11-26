ALTER TABLE shop.orders MODIFY COLUMN status VARCHAR(64) NOT NULL DEFAULT 'pending' AFTER customer_id;
