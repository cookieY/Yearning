UPDATE shop.orders SET status = 'paid' WHERE id = 1;
INSERT INTO shop.audit_events (order_id, event_type) VALUES (1, 'paid');
UPDATE shop.orders SET status = 'shipped' WHERE id = 2;
