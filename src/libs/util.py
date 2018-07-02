'''

Some tool sets

2017-11-23

cookie

'''

from urllib import request
from collections import namedtuple
from libs import con_database
import json
import random
import ssl
import time
import ldap3
import configparser
import ast

_conf = configparser.ConfigParser()
_conf.read('deploy.conf')


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
    conf_set = namedtuple("name", ["db", "address", "port", "username", "password", "ipaddress"])

    return conf_set(_conf.get('mysql', 'db'), _conf.get('mysql', 'address'),
                    _conf.get('mysql', 'port'), _conf.get('mysql', 'username'),
                    _conf.get('mysql', 'password'), _conf.get('host', 'ipaddress'))


def test_auth(username, password, host, type, sc, domain):
    if type == '1':
        user = username + '@' + domain
    elif type == '2':
        user = "uid=%s,%s" % (username, sc)
    else:
        user = "cn=%s,%s" % (username, sc)
    c = ldap3.Connection(
        ldap3.Server(host, get_info=ldap3.ALL),
        user=user,
        password=password)
    ret = c.bind()
    if ret:
        c.unbind()
        return True
    else:
        return False


def auth(username, password):
    un_init = init_conf()
    ldap = ast.literal_eval(un_init['ldap'])
    LDAP_SERVER = ldap['host']
    LDAP_DOMAIN = ldap['domain']
    LDAP_TYPE = ldap['type']
    LDAP_SCBASE = ldap['sc']
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


def init_conf():
    with con_database.SQLgo(
            ip=_conf.get('mysql', 'address'),
            user=_conf.get('mysql', 'username'),
            password=_conf.get('mysql', 'password'),
            db=_conf.get('mysql', 'db'),
            port=_conf.get('mysql', 'port')) as f:
        res = f.query_info("select * from core_globalpermissions where authorization = 'global'")

    return res[0]
