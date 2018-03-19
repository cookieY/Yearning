'''
 Create your models here.

'''
from django.db import models
from django.contrib.auth.models import AbstractUser
import ast


class JSONField(models.TextField):
    description = "Json"

    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)

    def from_db_value(self, value, expression, connection, context):
        if not value:
            value = {}
        return ast.literal_eval(value)

    def get_prep_value(self, value):
        if value is None:
            return value
        return str(value)

    def value_to_string(self, obj):
        value = self._get_val_from_obj(obj)
        return self.get_db_prep_save(value)


class Account(AbstractUser):
    '''
    User table
    '''
    group = models.CharField(max_length=40)   #权限组 guest/admin
    department = models.CharField(max_length=40) #部门


class SqlDictionary(models.Model):   
    '''
    数据库字典表
    '''
    BaseName = models.CharField(max_length=100)  #库名
    TableName = models.CharField(max_length=100) #表名
    Field = models.CharField(max_length=100) #字段名
    Type = models.CharField(max_length=100) #类型
    Extra = models.TextField() #备注
    TableComment = models.CharField(max_length=100) #表备注
    Name = models.CharField(max_length=100, null=True) #连接名

    def __str__(self):
        return self.TableName


class SqlOrder(models.Model):
    '''
    工单提交表
    '''
    work_id = models.CharField(max_length=50, blank=True) #工单id
    username = models.CharField(max_length=50, blank=True) #账号
    status = models.IntegerField(blank=True) # 工单状态 0 disagree 1 agree 2 indeterminate 3 ongoing
    type = models.SmallIntegerField(blank=True) #工单类型 0 DDL 1 DML
    backup = models.SmallIntegerField(blank=True)  # 工单是否备份 0 not backup 1 backup
    bundle_id = models.IntegerField(db_index=True, null=True) # Matching with Database_list id Field
    date = models.CharField(max_length=100, blank=True) # 提交日期
    basename = models.CharField(max_length=50, blank=True) #数据库名
    sql = models.TextField(blank=True) #sql语句
    text = models.CharField(max_length=100) # 工单备注
    assigned = models.CharField(max_length=50, blank=True)# 工单执行人


class DatabaseList(models.Model):
    '''
    数据库连接信息表
    '''
    connection_name = models.CharField(max_length=50) #连接名
    computer_room = models.CharField(max_length=50) #机房
    ip = models.CharField(max_length=100) #ip地址
    username = models.CharField(max_length=150) #数据库用户名
    port = models.IntegerField() #端口
    password = models.CharField(max_length=50) #数据库密码
    before = models.TextField(null=True) #提交工单 钉钉webhook发送内容
    after = models.TextField(null=True)  #工单执行成功后 钉钉webhook发送内容
    url = models.TextField(blank=True)    #钉钉webhook url地址


class SqlRecord(models.Model):
    '''
    工单执行记录表
    '''
    date = models.CharField(max_length=50) #执行时间 下个版本可废弃
    state = models.CharField(max_length=100) #执行状态
    sql = models.TextField(blank=True) #
    area = models.CharField(max_length=50)#下个版本可废弃
    name = models.CharField(max_length=50)#下个版本可废弃
    base = models.CharField(max_length=50)#下个版本可废弃
    error = models.TextField(null=True)
    workid = models.CharField(max_length=50, null=True)
    person = models.CharField(max_length=50, null=True) #下个版本可废弃
    reviewer = models.CharField(max_length=50, null=True) #下个版本可废弃
    affectrow = models.CharField(max_length=100, null=True)
    sequence = models.CharField(max_length=50, null=True)
    backup_dbname = models.CharField(max_length=100, null=True) #下个版本可废弃
    execute_time = models.CharField(max_length=150, null=True)
    SQLSHA1 = models.TextField(null=True)


class Todolist(models.Model):
    '''
    todo info 
    '''
    username = models.CharField(max_length=50) #账户
    content = models.CharField(max_length=200) #内容


class Usermessage(models.Model):
    '''
    user  message
    '''
    to_user = models.CharField(max_length=50) #收信人
    from_user = models.CharField(max_length=50) #发件人
    content = models.TextField(max_length=500) #内容
    time = models.CharField(max_length=50) #发送时间
    state = models.CharField(max_length=10) #发送状态
    title = models.CharField(max_length=100) # 站内信标题


class globalpermissions(models.Model):
    '''

    globalpermissions

    '''
    authorization = models.CharField(max_length=50, null=True, db_index=True)
    dingding = models.SmallIntegerField(default=0)
    email = models.SmallIntegerField(default=0)


class grained(models.Model):
    username = models.CharField(max_length=50,db_index=True)
    permissions = JSONField()