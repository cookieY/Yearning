'''

Automatically generate DDL

2017-11-23

cookie

'''


def AutomaticallyDDL(sql: str = ''):

    sql = sql.rstrip(';').split(' ')

    if sql[3] == 'CHANGE':
        Base = sql[2].rstrip('`').lstrip('`').split('.')
        COMMENT = ''
        default = ''
        Null = 'YES'
        for i, item in enumerate(sql):
            if sql[i] == 'DEFAULT':
                default = sql[i + 1]

            elif sql[i] == 'NULL':
                Null = 'NO'

            elif sql[i] == 'COMMENT':
                COMMENT = sql[i + 1]

        return {'BaseName': Base[0].rstrip('`'),
                'TableName': Base[1].lstrip('`'),
                'Field': sql[5].lstrip('`').rstrip('`'),
                'Default': default.rstrip('\'').lstrip('\''),
                'Null': Null, 'COMMENT': COMMENT.rstrip('\'').lstrip('\''),
                'Type': sql[7], 'mode': 'edit'
               }

    elif sql[3] == 'ADD':
        Base = sql[2].rstrip('`').lstrip('`').split('.')
        COMMENT = ''
        default = ''
        Null = 'YES'
        for i, item in enumerate(sql):
            if sql[i] == 'DEFAULT':
                default = sql[i + 1]

            elif sql[i] == 'NULL':
                Null = 'NO'

            elif sql[i] == 'COMMENT':
                COMMENT = sql[i + 1]

        return {'BaseName': Base[0].rstrip('`'),
                'TableName': Base[1].lstrip('`'),
                'Field': sql[5].lstrip('`').rstrip('`'),
                'Default': default.rstrip('\'').lstrip('\''),
                'Null': Null, 'COMMENT': COMMENT.rstrip('\'').lstrip('\''),
                'Type': sql[6], 'mode': 'add'
               }

    elif sql[3] == 'DROP':
        Base = sql[2].rstrip('`').lstrip('`').split('.')
        return {'BaseName': Base[0].rstrip('`'),
                'TableName': Base[1].lstrip('`'),
                'Field': sql[5].lstrip('`').rstrip('`'), 'mode': 'del'}