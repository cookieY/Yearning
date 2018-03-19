'''
serializers 
'''
from rest_framework import serializers
from core.models import Usermessage
from core.models import DatabaseList
from core.models import SqlDictionary
from core.models import SqlRecord
from core.models import Account
from core.models import SqlOrder


class MessagesUser(serializers.HyperlinkedModelSerializer):
    '''
    站内信列表serializers
    '''
    class Meta:
        model = Usermessage
        fields = ('title', 'time')


class UserINFO(serializers.HyperlinkedModelSerializer):
    '''
    平台用户信息列表serializers
    '''
    class Meta:
        model = Account
        fields = ('id','username', 'group', 'department', 'email')


class SQLGeneratDic(serializers.HyperlinkedModelSerializer):
    '''
    数据库字典信息serializers
    '''
    class Meta:
        model = SqlDictionary
        fields = (
            'BaseName', 'TableName', 'Field', 'Type','Extra', 'TableComment'
            )


class Sqllist(serializers.HyperlinkedModelSerializer):
    '''
    数据库连接信息serializers
    '''
    class Meta:
        model = DatabaseList
        fields = ('id', 'connection_name', 'ip', 'computer_room', 'password', 'port', 'username')


class Area(serializers.HyperlinkedModelSerializer):
    '''
    SQL提交及表结构修改页面数据库连接信息返回serializers
    '''
    class Meta:
        model = DatabaseList
        fields = ('id', 'connection_name', 'ip', 'computer_room')

class Record(serializers.HyperlinkedModelSerializer):
    '''
    执行工单的详细信息serializers
    '''
    class Meta:
        model = SqlRecord
        fields = ('sql', 'state', 'error', 'affectrow','sequence','execute_time')


class Getdingding(serializers.HyperlinkedModelSerializer):
    '''
    dingding webhook serializers
    '''
    class Meta:
        model = DatabaseList
        fields = ('id', 'before', 'after', 'url')


class Recordinfo(serializers.HyperlinkedModelSerializer):
    '''

    执行记录 返回

    '''

    class Meta:
        model = SqlOrder
        fields = ('workid', 'username', 'text', 'data', 'basename', 'assigned')