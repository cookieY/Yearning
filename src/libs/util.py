'''

Some tool sets

2017-11-23

cookie

'''

from urllib import request
from collections import namedtuple
import json
import random
import ssl
import time
import ldap3
import configparser


def dingding(content: str = None, url: str = None):
    '''
    dingding webhook 
    '''
    pdata = {"msgtype": "text", "text": {"content": content}}
    binary_data = json.dumps(pdata).encode(encoding='UTF8')
    headers = {"Content-Type": "application/json"}
    req = request.Request(url, headers=headers)
    context = ssl._create_unverified_context()
    request.urlopen(req, data=binary_data, context=context).read()


def date() -> str:
    '''
    datetime
    '''
    now = time.strftime('%Y-%m-%d %H:%M', time.localtime(time.time()))
    return now


def workId() -> str:
    '''
    工单
    '''
    now = time.strftime('%Y%m%d%H%M%S', time.localtime(time.time()))
    _ran = ''.join(random.sample('0123456789', 4))

    now = f'{now}{_ran}'
    return now


def ser(_obj: object) -> list:
    '''
    orm.raw 序列化
    '''
    _list = []
    _get = []
    for i in _obj:
        _list.append(i.__dict__)

    for i in _list:
        del i['_state']
        _get.append(i)
    return _get


def conf_path() -> object:
    '''
    读取配置文件属性
    '''
    _conf = configparser.ConfigParser()
    _conf.read('deploy.conf')
    conf_set = namedtuple("name", ["db", "address", "port", "username", "password", "ipaddress",
                                   "inc_host", "inc_port", "inc_user", "inc_pwd", "backupdb",
                                   "backupport", "backupuser", "backuppassword","ladp_server",
                                   "ldap_scbase","ladp_domain", "ladp_type","mail_user","mail_password","smtp",
                                   "smtp_port", "limit"])

    return conf_set(_conf.get('mysql', 'db'), _conf.get('mysql', 'address'),
                    _conf.get('mysql', 'port'), _conf.get('mysql', 'username'),
                    _conf.get('mysql', 'password'), _conf.get('host', 'ipaddress'),
                    _conf.get('Inception', 'ip'), _conf.get('Inception', 'port'),
                    _conf.get('Inception', 'user'), _conf.get('Inception', 'password'),
                    _conf.get('Inception', 'backupdb'), _conf.get('Inception', 'backupport'),
                    _conf.get('Inception', 'backupuser'), _conf.get('Inception', 'backuppassword'),
                    _conf.get('LDAP','LDAP_SERVER'),_conf.get('LDAP','LDAP_SCBASE'),_conf.get('LDAP','LDAP_DOMAIN'),_conf.get('LDAP','LDAP_TYPE'),
                    _conf.get('email', 'username'), _conf.get('email', 'password'), _conf.get('email', 'smtp_server'),
                    _conf.get('email', 'smtp_port'),_conf.get('sql', 'limit'))

def auth(username, password):
    conf = conf_path()
    LDAP_SERVER = conf.ladp_server
    LDAP_DOMAIN = conf.ladp_domain
    LDAP_TYPE = conf.ladp_type
    LDAP_SCBASE = conf.ldap_scbase
    if LDAP_TYPE == '1':
        user = username + '@' + LDAP_DOMAIN
    elif LDAP_TYPE == '2':
        user = "uid=%s,%s" % (username, LDAP_SCBASE)
    else:
        user = "cn=%s,%s" % (username, LDAP_SCBASE)
    c = ldap3.Connection(
        ldap3.Server(LDAP_SERVER, get_info=ldap3.ALL),
        user=user,
        password=password)
    ret = c.bind()
    if ret:
        c.unbind()
        return True
    else:
        return False
