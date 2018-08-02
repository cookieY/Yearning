from libs import con_database, util
import ast


def rollbackSQL(db=None, opid=None):
    un_init = util.init_conf()
    inception = ast.literal_eval(un_init['inception'])
    with con_database.SQLgo(
            ip=inception["back_host"],
            user=inception["back_user"],
            password=inception["back_password"],
            db=db,
            port=inception["back_port"]
    ) as f:
        data = f.query_info(
            sql=
            '''
            select tablename from $_$Inception_backup_information$_$ where opid_time =%s;
            ''' % opid)
        return data[0]


def roll(backdb=None, opid=None):
    un_init = util.init_conf()
    inception = ast.literal_eval(un_init['inception'])
    with con_database.SQLgo(
            ip=inception["back_host"],
            user=inception["back_user"],
            password=inception["back_password"],
            port=inception["back_port"]
    ) as f:
        data = f.query_info(
            sql=
            '''
            select rollback_statement from %s where opid_time =%s;
            ''' % (backdb, opid))
        return data
