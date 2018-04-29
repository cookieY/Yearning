#!/usr/bin/env python
from multiprocessing import Process
import subprocess
import os
import configparser

_conf = configparser.ConfigParser()
_conf.read('src/deploy.conf')
OutIp = _conf.get('host', 'ipaddress')
BASEPATH = os.path.dirname(os.path.abspath(__file__))

def startdjango():
    os.chdir(os.path.join(BASEPATH, 'src'))
    subprocess.call('python3 manage.py runserver 0.0.0.0:8000', shell=True)

def startnode():
    os.chdir(os.path.join(BASEPATH, 'webpage'))
    subprocess.call('npm run dev', shell=True)

def main():
    print('请访问%s'%OutIp)
    django = Process(target=startdjango, args=())
    node = Process(target=startnode, args=())
    django.start()
    node.start()

    django.join()
    node.join()

if __name__ == "__main__":
    main()
