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
                                   "backupport", "backupuser", "backuppassword"])

    return conf_set(_conf.get('mysql', 'db'), _conf.get('mysql', 'address'),
                    _conf.get('mysql', 'port'), _conf.get('mysql', 'username'),
                    _conf.get('mysql', 'password'), _conf.get('host', 'ipaddress'),
                    _conf.get('Inception', 'ip'), _conf.get('Inception', 'port'),
                    _conf.get('Inception', 'user'), _conf.get('Inception', 'password'),
                    _conf.get('Inception', 'backupdb'), _conf.get('Inception', 'backupport'),
                    _conf.get('Inception', 'backupuser'), _conf.get('Inception', 'backuppassword'))
