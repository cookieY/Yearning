import json
import logging
from django.http import HttpResponse
from rest_framework.response import Response
from libs import gen_ddl, baseview

CUSTOM_ERROR = logging.getLogger('Yearning.core.views')


class gen_sql(baseview.BaseView):

    '''

    :argument 调用gen_ddl库 生成DDL语句 生成索引语句。并将生成的sql返回

    :param

    :return 生成的sql语句

    '''

    def put(self, request, args=None):

        if args == "sql":
            try:
                data = json.loads(request.data['data'])
                base = request.data['basename']
            except KeyError as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            else:
                gen_sql = []
                try:
                    for i in data:
                        if 'edit' in i.keys():
                            info = gen_ddl.create_sql(select_name='edit',
                                                      column_name=i['edit']['Field'],
                                                      column_type=i['edit']['Type'],
                                                      default=i['edit']['Default'],
                                                      comment=i['edit']['Extra'],
                                                      null=i['edit']['Null'],
                                                      table_name=i['table_name'],
                                                      base_name=base)
                            gen_sql.append(info)

                        elif 'del' in i.keys():
                            info = gen_ddl.create_sql(select_name='del',
                                                      column_name=i['del']['Field'],
                                                      table_name=i['table_name'],
                                                      base_name=base)
                            gen_sql.append(info)
                        elif 'add' in i.keys() and i['add'] != []:
                            for n in i['add']:
                                info = gen_ddl.create_sql(select_name='add',
                                                          column_name=n['Field'],
                                                          base_name=base,
                                                          column_type=n['Type'],
                                                          default=n['Default'],
                                                          comment=n['Extra'],
                                                          null=n['Null'],
                                                          table_name=i['table_name'])

                                gen_sql.append(info)
                    return Response(gen_sql)
                except Exception as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return HttpResponse(status=500)

        elif args == "index":
            try:
                data = json.loads(request.data['data'])
            except KeyError as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            else:
                gen_sql = []
                try:
                    for i in data:
                        if 'delindex' in i.keys():
                            info = gen_ddl.index(select_name='delindex',
                                                 key_name=i['delindex']['key_name'],
                                                 table_name=i['table_name'])
                            gen_sql.append(info)
                        elif 'addindex' in i.keys() and i['addindex'] != []:
                            for n in i['addindex']:
                                if n['fulltext'] == "YES":
                                    info = gen_ddl.index(table_name=i['table_name'],
                                                         column_name=n['column_name'],
                                                         key_name=n['key_name'],
                                                         fulltext=n['fulltext'],
                                                         select_name='addindex')
                                    gen_sql.append(info)
                                elif n['Non_unique'] == "YES":
                                    info = gen_ddl.index(select_name='addindex',
                                                         key_name=n['key_name'],
                                                         non_unique='unique',
                                                         column_name=n['column_name'],
                                                         table_name=i['table_name'])
                                    gen_sql.append(info)
                                else:
                                    info = gen_ddl.index(select_name='addindex',
                                                         key_name=n['key_name'],
                                                         column_name=n['column_name'],
                                                         table_name=i['table_name'])
                                    gen_sql.append(info)
                    return Response(gen_sql)
                except Exception as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return HttpResponse(status=500)