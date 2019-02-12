'''

Some tool sets

2017-11-23

cookie

'''

import json
import random
import ssl
import time
import configparser
import ast
from urllib import request
from collections import namedtuple
from ldap3 import Server, Connection, SUBTREE, ALL
from libs import con_database

_conf = configparser.ConfigParser()
_conf.read('deploy.conf')


def dingding(content: str = None, url: str = None):
    '''

    dingding webhook

    '''

    pdata = {"msgtype": "markdown", "markdown": {"title": "Yearning sql审计平台", "text": content}}
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
    conf_set = namedtuple(
        "name", ["db", "address", "port", "username", "password", "ipaddress"])

    return conf_set(_conf.get('mysql', 'db'), _conf.get('mysql', 'address'),
                    _conf.get('mysql', 'port'), _conf.get('mysql', 'username'),
                    _conf.get('mysql', 'password'), _conf.get('host', 'ipaddress'))


class LDAPConnection(object):
    def __init__(self, url, user, password):
        server = Server(url, get_info=ALL)
        self.conn = Connection(server, user=user, password=password, check_names=True, lazy=False,
                               raise_exceptions=False)

    def __enter__(self):
        self.conn.bind()
        return self.conn

    def __exit__(self, exc_type, exc_val, exc_tb):
        self.conn.unbind()


def test_auth(url, user, password):
    with LDAPConnection(url, user, password) as conn:
        if conn.bind():
            return True
    return False


def auth(username, password):
    un_init = init_conf()
    ldap = ast.literal_eval(un_init['ldap'])

    LDAP_TYPE = ldap['type']
    LDAP_SCBASE = ldap['sc']

    if LDAP_TYPE == '1':
        search_filter = '(sAMAccountName={})'.format(username)
    elif LDAP_TYPE == '2':
        search_filter = '(uid={})'.format(username)
    else:
        search_filter = '(cn={})'.format(username)

    with LDAPConnection(ldap['url'], ldap['user'], ldap['password']) as conn:

        res = conn.search(
            search_base=LDAP_SCBASE,
            search_filter=search_filter,
            search_scope=SUBTREE,
            attributes=['cn', 'uid', 'mail'],
        )
        if res:
            entry = conn.response[0]
            # check password by dn
            try:
                if conn.rebind(user=entry['dn'], password=password):
                    attr_dict = entry['attributes']
                    print((True, attr_dict["mail"], attr_dict["cn"], attr_dict["uid"]))
                    return True
            except Exception as e:
                print(str(e))
    return False


def init_conf():
    with con_database.SQLgo(
            ip=_conf.get('mysql', 'address'),
            user=_conf.get('mysql', 'username'),
            password=_conf.get('mysql', 'password'),
            db=_conf.get('mysql', 'db'),
            port=_conf.get('mysql', 'port')) as f:
        res = f.query_info(
            "select * from core_globalpermissions where authorization = 'global'")

    return res[0]
