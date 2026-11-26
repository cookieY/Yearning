CREATE OR REPLACE VIEW shop.v_active_orders (id, status, amount) AS SELECT * FROM shop.orders WHERE status = 'active';
