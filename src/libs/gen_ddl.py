def create_sql(select_name=None, base_name=None, column_name=None, column_type=None,
               table_name=None, default=None, comment=None, null=None) -> str:
    if default:
        if default.isdigit():
            pass
        else:
            default = f''' \'{default}\''''
    if select_name == "add":
        if default is None:
            if null == 'YES':
                if comment is None:
                    return f"ALTER TABLE `{base_name}`.`{table_name}` \
                             ADD COLUMN `{column_name}` {column_type}"
                else:
                    return f"ALTER TABLE `{base_name}`.`{table_name}`  ADD COLUMN `{column_name}` \
                            {column_type} COMMENT '{comment}'"
            else:
                if comment is None:
                    return f"ALTER TABLE `{base_name}`.`{table_name}` ADD COLUMN `{column_name}` \
                            {column_type} NOT NULL"
                else:
                    return f"ALTER TABLE `{base_name}`.`{table_name}` ADD COLUMN `{column_name}` " \
                           f"{column_type} NOT NULL COMMENT '{comment}'"
        else:
            if null == 'NO':
                if comment is None:
                    return f"ALTER TABLE `{base_name}`.`{table_name}` ADD COLUMN `{column_name}` " \
                           f"{column_type} NOT NULL DEFAULT {default}"
                else:
                    return f"ALTER TABLE `{base_name}`.`{table_name}` ADD COLUMN `{column_name}` " \
                           f"{column_type} NOT NULL DEFAULT {default} COMMENT '{comment}'"
            else:
                if comment is None:
                    return f"ALTER TABLE `{base_name}`.`{table_name}` ADD COLUMN  `{column_name}` \
                           {column_type}  DEFAULT {default}"
                else:
                    return f"ALTER TABLE `{base_name}`.`{table_name}` ADD COLUMN `{column_name}` " \
                           f"{column_type}  DEFAULT {default} COMMENT '{comment}'"
    if select_name == 'edit':
        if default is None:
            if null == 'YES':
                if comment is None:
                    return f"ALTER TABLE `{base_name}`.`{table_name}` " \
                           f"CHANGE COLUMN `{column_name}` `{column_name}` {column_type}"
                else:
                    return f"ALTER TABLE `{base_name}`.`{table_name}` \
                           CHANGE COLUMN `{column_name}` `{column_name}` \
                           {column_type} COMMENT '{comment}'"
            else:
                if comment == '':
                    return f"ALTER TABLE `{base_name}`.`{table_name}` " \
                           f"CHANGE COLUMN `{column_name}` `{column_name}` {column_type} NOT NULL"
                else:
                    return f"ALTER TABLE `{base_name}`.`{table_name}` \
                           CHANGE COLUMN `{column_name}` `{column_name}` \
                           {column_type} NOT NULL COMMENT '{comment}'"
        else:
            if null == 'NO':
                if comment is None:
                    return f"ALTER TABLE `{base_name}`.`{table_name}` \
                           CHANGE COLUMN `{column_name}` `{column_name}` \
                           {column_type} NOT NULL DEFAULT {default}"
                else:
                    return f"ALTER TABLE `{base_name}`.`{table_name}` \
                           CHANGE COLUMN `{column_name}` `{column_name}` \
                           {column_type} NOT NULL DEFAULT {default} COMMENT '{comment}'"
            else:
                if comment is None:
                    return f"ALTER TABLE `{base_name}`.`{table_name}` \
                           CHANGE COLUMN `{column_name}` `{column_name}` \
                           {column_type} DEFAULT {default}"
                else:
                    return f"ALTER TABLE `{base_name}`.`{table_name}` \
                           CHANGE COLUMN `{column_name}` `{column_name}` \
                           {column_type} DEFAULT {default} COMMENT '{comment}'"

    if select_name == 'del':
        return f"ALTER TABLE `{base_name}`.`{table_name}` DROP COLUMN {column_name}"


def index(key_name=None, table_name=None, non_unique=None,
          column_name=None, select_name=None, fulltext=None):
    if select_name == 'addindex':
        if fulltext == 'YES':
            return f'''ALTER TABLE `{table_name}` ADD FULLTEXT {key_name} ({column_name}) '''
        else:
            if non_unique is not None:
                return f'''ALTER TABLE `{table_name}` ADD \
                            UNIQUE {key_name}(`{column_name}`)'''
            else:
                return f'''ALTER TABLE `{table_name}` ADD INDEX {key_name}(`{column_name}`)'''
    if select_name == "delindex":
        return f'''ALTER TABLE `{table_name}` DROP INDEX {key_name}'''
