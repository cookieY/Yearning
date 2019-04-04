import logging
import json
from libs import baseview, rollback, util
from rest_framework.response import Response
from django.http import HttpResponse
from core.models import SqlOrder, SqlRecord
from libs.serializers import Record

CUSTOM_ERROR = logging.getLogger('Yearning.core.views')


class record_order(baseview.SuperUserpermissions):
    '''

    :argument 记录展示请求接口api

    :return 记录及记录总数

    '''

    def get(self, request, args=None):
        try:
            page = request.GET.get('page')
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            return HttpResponse(status=500)
        else:
            try:
                pagenumber = SqlOrder.objects.filter(status=1, assigned=request.user).count()
                start = (int(page) - 1) * 20
                end = int(page) * 20
                sql = SqlOrder.objects.raw(
                    '''
                    select o.id,o.work_id,o.text,o.backup,o.date,o.assigned,
                    o.username,o.real_name,o.basename,core_databaselist.connection_name, \
                    core_databaselist.computer_room from core_sqlorder as o \
                    INNER JOIN core_databaselist on \
                    o.bundle_id = core_databaselist.id where o.status = 1 and o.assigned = '%s'\
                    ORDER BY o.id desc
                    ''' % request.user
                )[start:end]
                return Response({'data': util.ser(sql), 'page': pagenumber})
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)


class order_detail(baseview.BaseView):
    '''

    :argument 执行工单的详细信息请求接口api

    '''

    def get(self, request, args: str = None):

        '''

        :argument 详细信息数据展示

        :param args: 根据获得的work_id  status order_id 查找相关数据并返回

        :return:

        '''
        try:
            work_id = request.GET.get('workid')
            status = request.GET.get('status')
            order_id = request.GET.get('id')
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
        else:
            type_id = SqlOrder.objects.filter(id=order_id).first()
            try:
                if status == '1' or status == '4':
                    data = SqlRecord.objects.filter(workid=work_id).all()
                    _serializers = Record(data, many=True)
                    return Response({'data': _serializers.data, 'type': type_id.type})
                else:
                    data = SqlOrder.objects.filter(work_id=work_id).first()
                    _in = {'data': [{'sql': x} for x in data.sql.split(';')], 'type': type_id.type}
                    return Response(_in)
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__} : {e}')
                return HttpResponse(status=500)

    def put(self, request, args: str = None):

        '''

        :argument 当工单驳回后重新提交功能接口api

        :param args: 根据获得order_id 返回对应被驳回的sql

        :return:

        '''

        try:
            order_id = request.data['id']
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
        else:
            try:
                info = SqlOrder.objects.raw(
                    "select core_sqlorder.*,core_databaselist.connection_name,\
                    core_databaselist.computer_room from core_sqlorder INNER JOIN \
                    core_databaselist on core_sqlorder.bundle_id = core_databaselist.id \
                    WHERE core_sqlorder.id = %s" % order_id)
                data = util.ser(info)
                sql = data[0]['sql'].split(';')
                _tmp = ''
                for i in sql:
                    _tmp += i + ";\n"
                return Response({'data': data[0], 'sql': _tmp.strip('\n'), 'type': 0})
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)

    def post(self, request, args: str = None):

        '''

        :argument 当工单执行后sql回滚功能接口api

        :param args: 根据获得order_id 返回对应的回滚sql

        :return: {'data': data[0], 'sql': rollback_sql, 'type': 1}

        '''

        try:
            order_id = request.data['id']
            tmp = list(set(json.loads(request.data['opid'])))
            info = [x for x in tmp if x.find("0_0_") == -1]
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            return HttpResponse(status=500)
        else:
            try:
                sql = []
                rollback_sql = []
                dataset = SqlOrder.objects.raw(
                    "select core_sqlorder.*,core_databaselist.connection_name,\
                    core_databaselist.computer_room from core_sqlorder INNER JOIN \
                    core_databaselist on core_sqlorder.bundle_id = core_databaselist.id \
                    WHERE core_sqlorder.id = %s"
                    % order_id)
                data = util.ser(dataset)
                for i in info:
                    _data = SqlRecord.objects.filter(sequence=i).first()
                    roll = rollback.rollbackSQL(db=_data.backup_dbname, opid=i)
                    link = _data.backup_dbname + '.' + roll['tablename']
                    sql.append(rollback.roll(backdb=link, opid=i))
                for i in sql:
                    for c in i:
                        rollback_sql.append(c['rollback_statement'])
                rollback_sql = sorted(rollback_sql)
                if rollback_sql == []: return HttpResponse(status=500)
                rollback_sql = [{'sql': x} for x in rollback_sql]
                return Response({'data': data[0], 'sql': rollback_sql, 'type': 1})
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)
