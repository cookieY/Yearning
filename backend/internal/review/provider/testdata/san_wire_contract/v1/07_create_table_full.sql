CREATE TABLE shop.payments (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT 'primary id',
  order_id BIGINT UNSIGNED NOT NULL COMMENT 'order reference',
  currency CHAR(3) NOT NULL DEFAULT 'USD' COMMENT 'ISO currency',
  reference_code VARCHAR(128) NOT NULL DEFAULT '' COMMENT 'external reference',
  PRIMARY KEY (id),
  UNIQUE KEY uniq_payments_order (order_id),
  KEY idx_payments_order_currency (order_id, currency)
) ENGINE=InnoDB AUTO_INCREMENT=1 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='payment ledger';
