CREATE TABLE shop.legacy_notes (
  id BIGINT NOT NULL,
  body VARCHAR(200) NOT NULL DEFAULT '',
  PRIMARY KEY (id)
) ENGINE=InnoDB COMMENT='charset=custom_legacy_cs';
