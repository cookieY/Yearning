import logging
import json
from libs import send_email
from libs import baseview
from django.db.models import Count
from libs import util
from core.task import submit_push_messages
from rest_framework.response import Response
from django.http import HttpResponse
from core.models import (
ArchiveInfo,
ArchiveLog,
DatabaseList
)
from libs import call_archive

CUSTOM_ERROR = logging.getLogger('Yearning.core.views')

conf = util.conf_path()
addr_ip = conf.ipaddress


class sqlarchive(baseview.BaseView):
    '''

    :argument 手动模式工单提交相关接口api
    post 提交工单

    '''

    def post(self, request, args=None):
        try:
            user = request.data['user']
            source_table = request.data['source_table']
            dest_table= request.data['dest_table']
            archive_condition= request.data['archive_condition']
            soure_dbname= request.data['source_dbname']
            dest_dbname = request.data['dest_dbname']
            source_id = request.data['source_id']
            dest_id = request.data['dest_id']
            source_id = request.data['source_id']
            status = request.data['status']
            type = request.data['type']
            ssh_hostid = request.data['ssh_hostid']
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            return HttpResponse(status=500)
        else:
            try:
                workId = util.workId()
                ArchiveInfo.objects.get_or_create(
                source_id =  source_id,
                db_source=soure_dbname,
                table_source= source_table,
                dest_id = dest_id,
                db_dest= dest_dbname,
                dest_table = dest_table,
                date=util.date(),
                archive_condition=archive_condition,
                status= status,
                type=type,
                ssh_hostid=ssh_hostid,
                )
                submit_push_messages(
                    workId=workId,
                    user=user,
                    addr_ip=addr_ip,
                    text='',
                    assigned='',
                    id=id
                ).start()
                return Response('已提交!')
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)

    def get(self, request, args: str=None):
        try:
            username = request.GET.get('user')
            page = request.GET.get('page')
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
        else:
            try:
                page_number = ArchiveInfo.objects.filter(
                create_username=username).aggregate(alter_number=Count('id'))
                start = (int(page) - 1) * 20
                end = int(page) * 20
                info = ArchiveInfo.objects.raw(
                    "select core_archive_info.*,core_databaselist.connection_name,\
                    core_databaselist.computer_room from core_archive_info INNER JOIN \
                    core_databaselist on core_archive_info.source_id = core_databaselist.id \
                    WHERE core_archive_info.created_user = '%s'ORDER BY core_archive_info.id DESC "
                    % username)[start:end]
                data = util.ser(info)
                return Response({'page': page_number, 'data': data})
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)

    def put(self, request, args=None):
        if args == 'check':
            try:
                source_id = request.data['source_id']
                dest_id = request.data['dest_id']
                source_table = request.data['source_table']
                source_dbname = request.data['source_dbname']
                dest_table= request.data['dest_table']
                dest_dbname = request.data['dest_dbname']
                archive_condition = request.data['archive_condition']
                ssh_host = request.data['ssh_host']
                ssh_port = request.data['ssh_port']
                ssh_user = request.data['ssh_user']
                ssh_keyfile = request.data['ssh_keyfile']
                source_data = DatabaseList.objects.filter(id=source_id).first()
                dest_data = DatabaseList.objects.filter(id=dest_id).first()
            except KeyError as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            else:
                try:
                    res = call_archive.Check()
                    return HttpResponse(res)
                except Exception as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return HttpResponse(status=500)

        elif args == 'archive':
            try:
                id = request.data['id']
                source_id = request.data['source_id']
                dest_id = request.data['dest_id']
                source_table = request.data['source_table']
                source_dbname = request.data['source_dbname']
                dest_table= request.data['dest_table']
                dest_dbname = request.data['dest_dbname']
                archive_condition = request.data['archive_condition']
                archive = ArchiveInfo.objects.filter(id=id)
                source_data = DatabaseList.objects.filter(id=archive["source_id"]).first()
                dest_data = DatabaseList.objects.filter(id=archive["dest_id"]).first()

                info = {
                    'source_host': source_data.ip,
                    'source_user': source_data.username,
                    'source_password': source_data.password,
                    'source_db': source_dbname,
                    'source_port': source_data.port,
                    'dest_host': dest_data.ip,
                    'dest_user': dest_data.username,
                    'dest_password': dest_data.password,
                    'dest_db': dest_dbname,
                    'dest_port': dest_data.port,
                    'archvie_condition': archive_condition
                    }
            except KeyError as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            else:
                try:
                    with call_archive.Execute(LoginDic=info) as test:
                        res = test.Check()
                        return Response({'result': res, 'status': 200})
                except Exception as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return Response({'status': '500'})

class sqlarchivelog(baseview.BaseView):
    '''

    :argument 手动模式工单提交相关接口api
    post 提交工单

    '''


    def post(self, request, args=None):
        try:
            data = json.loads(request.data['data'])
            tmp = json.loads(request.data['sql'])
            user = request.data['user']
            source_table = request.data['source_table']
            dest_table= request.data['dest_table']
            archive_condition= request.data['archive_condition']
            soure_dbname= request.data['source_dbname']
            dest_dbname = request.data['dest_dbname']
            source_id = request.data['source_id']
            dest_id = request.data['dest_id']
            source_id = request.data['source_id']
            status = request.data['status']
            type = request.data['type']
            ssh_hostid = request.data['ssh_hostid']
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            return HttpResponse(status=500)
        else:
            try:
                workId = util.workId()
                ArchiveLog.objects.get_or_create(
                source_id =  source_id,
                db_source=soure_dbname,
                table_source= source_table,
                dest_id = dest_id,
                db_dest= dest_dbname,
                dest_table = dest_table,
                date=util.date(),
                archive_condition=archive_condition,
                status= status,
                type=type,
                ssh_hostid=ssh_hostid,
                )
                submit_push_messages(
                    workId=workId,
                    user=user,
                    addr_ip=addr_ip,
                    text=data['text'],
                    assigned=data['assigned'],
                    id=id
                ).start()
                return Response('已提交!')
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)
    def get(self, request, args: str=None):
        try:
            username = request.GET.get('user')
            page = request.GET.get('page')
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
        else:
            try:
                page_number = ArchiveLog.objects.filter(
                username=username).aggregate(alter_number=Count('id'))
                start = (int(page) - 1) * 20
                end = int(page) * 20
                info = ArchiveInfo.objects.raw(
                    "select ArchiveLog.*,core_databaselist.connection_name,\
                    core_databaselist.computer_room from core_archive_log INNER JOIN \
                    core_databaselist on ArchiveLog.source_id = core_databaselist.id \
                    WHERE core_archive_log.created_user = '%s'ORDER BY core_archive_log.id DESC "
                    % username)[start:end]
                data = util.ser(info)
                return Response({'page': page_number, 'data': data})
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)