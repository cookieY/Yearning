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
    group = models.CharField(max_length=40)  # 权限组 guest/admin
    department = models.CharField(max_length=40)  # 部门
    auth_group = models.CharField(max_length=100, null=True)  # 细粒化权限组
    real_name = models.CharField(max_length=100, null=True, default='请添加真实姓名')  # 真实姓名


class SqlOrder(models.Model):
    '''
    工单提交表
    '''
    work_id = models.CharField(max_length=50, blank=True)  # 工单id
    username = models.CharField(max_length=50, blank=True)  # 提交人
    status = models.IntegerField(blank=True)  # 工单状态 0 disagree 1 agree 2 indeterminate 3 ongoing 4 faild
    type = models.SmallIntegerField(blank=True)  # 工单类型 0 DDL 1 DML
    backup = models.SmallIntegerField(blank=True)  # 工单是否备份 0 not backup 1 backup
    bundle_id = models.IntegerField(db_index=True, null=True)  # Matching with Database_list id Field
    date = models.CharField(max_length=100, blank=True)  # 提交日期
    basename = models.CharField(max_length=50, blank=True)  # 数据库名
    sql = models.TextField(blank=True)  # sql语句
    text = models.TextField(blank=True)  # 工单备注
    assigned = models.CharField(max_length=50, blank=True)  # 工单执行人
    delay = models.IntegerField(null=True, default=0)  # 延迟时间
    rejected = models.TextField(blank=True)  # 驳回说明
    real_name = models.CharField(max_length=100, null=True)  # 姓名


class DatabaseList(models.Model):
    '''
    数据库连接信息表
    '''
    connection_name = models.CharField(max_length=50)  # 连接名
    computer_room = models.CharField(max_length=50)  # 机房
    ip = models.CharField(max_length=100)  # ip地址
    username = models.CharField(max_length=150)  # 数据库用户名
    port = models.IntegerField()  # 端口
    password = models.CharField(max_length=50)  # 数据库密码
    before = models.TextField(null=True)  # 提交工单 钉钉webhook发送内容
    after = models.TextField(null=True)  # 工单执行成功后 钉钉webhook发送内容


class SqlRecord(models.Model):
    '''
    工单执行记录表
    '''
    state = models.CharField(max_length=100)  # 执行状态
    sql = models.TextField(blank=True)  #
    error = models.TextField(null=True)
    workid = models.CharField(max_length=50, null=True)
    affectrow = models.CharField(max_length=100, null=True)
    sequence = models.CharField(max_length=50, null=True)
    execute_time = models.CharField(max_length=150, null=True)
    backup_dbname = models.CharField(max_length=100, null=True)
    SQLSHA1 = models.TextField(null=True)


class Todolist(models.Model):
    '''
    todo info 
    '''
    username = models.CharField(max_length=50)  # 账户
    content = models.CharField(max_length=200)  # 内容


class globalpermissions(models.Model):
    '''

    globalpermissions

    '''
    authorization = models.CharField(max_length=50, null=True, db_index=True)
    inception = JSONField(null=True)
    ldap = JSONField(null=True)
    message = JSONField(null=True)
    other = JSONField(null=True)


class grained(models.Model):
    username = models.CharField(max_length=50, db_index=True)
    permissions = JSONField()


class applygrained(models.Model):
    username = models.CharField(max_length=50, db_index=True)
    work_id = models.CharField(max_length=50, null=True)
    status = models.IntegerField(blank=True, null=True)  # 工单状态 0 disagree 1 agree 2 indeterminate
    permissions = JSONField()
    auth_group = models.CharField(max_length=50, null=True)
    real_name = models.CharField(max_length=100, null=True)  # 真实姓名


class querypermissions(models.Model):
    work_id = models.CharField(max_length=50, null=True, db_index=True)
    username = models.CharField(max_length=100, null=True)
    statements = models.TextField()


class query_order(models.Model):
    work_id = models.CharField(max_length=50, null=True, db_index=True)
    username = models.CharField(max_length=100, null=True)
    date = models.CharField(max_length=50)
    instructions = models.TextField(null=True)
    query_per = models.SmallIntegerField(null=True, default=0)  # 0拒绝 1同意 2待审核 3完毕
    connection_name = models.CharField(max_length=50, null=True)  # 连接名
    computer_room = models.CharField(max_length=50, null=True)  # 机房
    export = models.SmallIntegerField(null=True, default=0)
    audit = models.CharField(max_length=100, null=True)
    time = models.CharField(max_length=100, null=True)
    real_name = models.CharField(max_length=100, null=True)  # 真实姓名
