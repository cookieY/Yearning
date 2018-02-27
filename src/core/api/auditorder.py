import logging
import json
from libs import baseview
from libs import util
from libs import call_inception
from libs import rollback
from rest_framework.response import Response
from django.db.models import Count
from django.http import HttpResponse
from core.models import (
    SqlOrder,
    Usermessage,
    DatabaseList,
    SqlRecord
)
from libs.serializers import (
    Record
)

from core.task import order_push_message,rejected_push_messages

conf = util.conf_path()
addr_ip = conf.ipaddress
CUSTOM_ERROR = logging.getLogger('Yearning.core.views')


class audit(baseview.SuperUserpermissions):
    '''
    SQL审核相关
    '''

    def get(self, request, args=None):
        try:
            page = request.GET.get('page')
            username = request.GET.get('username')
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            return HttpResponse(status=500)
        else:
            try:
                pagenumber = SqlOrder.objects.filter(assigned=username).aggregate(alter_number=Count('id'))
                start = (int(page) - 1) * 20
                end = int(page) * 20
                info = SqlOrder.objects.raw(
                    '''
                    select core_sqlorder.*,core_databaselist.connection_name, \
                    core_databaselist.computer_room from core_sqlorder \
                    INNER JOIN core_databaselist on \
                    core_sqlorder.bundle_id = core_databaselist.id where core_sqlorder.assigned = '%s'\
                    ORDER BY core_sqlorder.id desc
                    '''%username
                )[start:end]
                data = util.ser(info)
                return Response({'page': pagenumber, 'data': data})
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)

    def put(self, request, args=None):
        try:
            type = request.data['type']
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
        else:
            if type == 0:
                try:
                    from_user = request.data['from_user']
                    to_user = request.data['to_user']
                    text = request.data['text']
                    id = request.data['id']
                except KeyError as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return HttpResponse(status=500)
                else:
                    try:
                        SqlOrder.objects.filter(id=id).update(status=0)
                        _tmpData = SqlOrder.objects.filter(id=id).values(
                            'work_id',
                            'bundle_id'
                        ).first()
                        title = '工单:' + _tmpData['work_id'] + '驳回通知'
                        Usermessage.objects.get_or_create(
                            from_user=from_user,
                            time=util.date(),
                            title=title,
                            content=text,
                            to_user=to_user,
                            state='unread'
                        )
                        rejected_push_messages(_tmpData, to_user, addr_ip, text).start()
                        return Response('操作成功，该请求已驳回！')
                    except Exception as e:
                        CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                        return HttpResponse(status=500)

            elif type == 1:
                try:
                    from_user = request.data['from_user']
                    to_user = request.data['to_user']
                    id = request.data['id']
                except KeyError as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return HttpResponse(status=500)
                else:
                    try:
                        SqlOrder.objects.filter(id=id).update(status=3)
                        order_push_message(addr_ip, id, from_user, to_user).start()
                        return Response('工单执行成功!请通过记录页面查看具体执行结果')
                    except Exception as e:
                        CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                        return HttpResponse(status=500)

            elif type == 'test':
                try:
                    base = request.data['base']
                    id = request.data['id']
                except KeyError as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return HttpResponse(status=500)
                else:
                    sql = SqlOrder.objects.filter(id=id).first()
                    data = DatabaseList.objects.filter(id=sql.bundle_id).first()
                    info = {
                        'host': data.ip,
                        'user': data.username,
                        'password': data.password,
                        'db': base,
                        'port': data.port
                    }
                    try:
                        with call_inception.Inception(LoginDic=info) as test:
                            res = test.Check(sql=sql.sql)
                            return Response({'result': res, 'status': 200})
                    except Exception as e:
                        CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                        return Response({'status': '500'})

    def post(self, request, args: str = None):
        try:
            dataid = json.loads(request.data['id'])
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
        else:
            try:
                for i in dataid:
                    if i['status'] == 1:
                        workid = SqlOrder.objects.filter(id=i['id']).first()
                        SqlRecord.objects.filter(workid=workid.work_id).delete()
                        SqlOrder.objects.filter(id=i['id']).delete()
                    else:
                        SqlOrder.objects.filter(id=i['id']).delete()
                return Response('工单数据删除成功!')
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)


class orderdetail(baseview.BaseView):

    '''

    执行工单的详细信息

    '''

    def get(self, request, args: str = None):
        try:
            workid = request.GET.get('workid')
            status = request.GET.get('status')
            id = request.GET.get('id')
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
        else:
            type_id = SqlOrder.objects.filter(id=id).first()
            try:
                if status == '1':
                    data = SqlRecord.objects.filter(workid=workid).all()
                    _serializers = Record(data, many=True)
                    return Response({'data':_serializers.data, 'type':type_id.type})
                else:
                    data = SqlOrder.objects.filter(work_id=workid).first()
                    _in = {'data':[{'sql': x} for x in data.sql.split(';')], 'type':type_id.type}
                    return Response(_in)
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__} : {e}')
                return HttpResponse(status=500)

    def put(self, request, args: str = None):
        try:
            id = request.data['id']
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
        else:
            try:
                info = SqlOrder.objects.raw(
                    "select core_sqlorder.*,core_databaselist.connection_name,\
                    core_databaselist.computer_room from core_sqlorder INNER JOIN \
                    core_databaselist on core_sqlorder.bundle_id = core_databaselist.id \
                    WHERE core_sqlorder.id = %s"
                    %id)
                data = util.ser(info)
                sql = data[0]['sql'].split(';')
                _tmp = ''
                for i in sql:
                    _tmp += i + ";\n"
                return Response({'data':data[0], 'sql':_tmp.strip('\n'), 'type': 0})
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)

    def post(self, request, args: str = None):
        try:
            id = request.data['id']
            info = json.loads(request.data['opid'])
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            return HttpResponse(status=500)
        else:
            try:
                sql = []
                for i in info:
                    info = SqlOrder.objects.raw(
                        "select core_sqlorder.*,core_databaselist.connection_name,\
                        core_databaselist.computer_room from core_sqlorder INNER JOIN \
                        core_databaselist on core_sqlorder.bundle_id = core_databaselist.id \
                        WHERE core_sqlorder.id = %s"
                        % id)
                    data = util.ser(info)
                    _data = SqlRecord.objects.filter(sequence=i).first()
                    roll = rollback.rollbackSQL(db=_data.backup_dbname, opid=i)
                    link = _data.backup_dbname + '.' + roll
                    spa = rollback.roll(backdb=link, opid=i)
                    sql.append(spa)
                _h = sorted([i[0][0] for i in sql])
                return Response({'data': data[0], 'sql': _h, 'type': 1})
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)
