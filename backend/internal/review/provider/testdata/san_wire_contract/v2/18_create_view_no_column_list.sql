CREATE OR REPLACE VIEW shop.v_active_orders AS SELECT * FROM shop.orders WHERE status = 'active';
