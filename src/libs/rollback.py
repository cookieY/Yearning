from libs import con_database
from libs import util

conf = util.conf_path()


def rollbackSQL(db=None, opid=None):
    with con_database.SQLgo(
        ip=conf.backupdb,
        user=conf.backupuser,
        password=conf.backuppassword,
        db=db,
        port=conf.backupport
        ) as f:
        data = f.execute(
            sql=
            '''
            select tablename from $_$Inception_backup_information$_$ where opid_time =%s;
            ''' % opid)
        return data[0][0]


def roll(backdb=None, opid=None):
    with con_database.SQLgo(
        ip=conf.backupdb,
        user=conf.backupuser,
        password=conf.backuppassword,
        port=conf.backupport
        ) as f:
        data = f.dic_data(
            sql=
            '''
            select rollback_statement from %s where opid_time =%s;
            ''' % (backdb, opid))
        return data