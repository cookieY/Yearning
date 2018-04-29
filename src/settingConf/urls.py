'''
url table
'''
from django.conf.urls import url
from rest_framework.urlpatterns import format_suffix_patterns
from core.api.sqldic import (
    adminpremisson,
    exportdoc,
    dictionary,
    downloadFile
)
from core.api.user import (
    userinfo,
    generaluser,
    authgroup,
    ldapauth,
    login_auth
)
from core.api.dashboard import (
    dashboard,
    messages
)
from core.api.managerdb import (
    management_db,
    push_permissions,
    dingding,
    management_ssh
)
from core.api.auditorder import (
    audit,
    del_order
)
from core.api.record import (
    record_order,
    order_detail
)
from core.api.applygrained import (
    audit_grained,
    apply_grained
)
from core.api.sqlorder import sqlorder
from core.api.serachsql import search
from core.api.osc import osc_step
from core.api.myorder import order
from core.api.gensql import gen_sql
from core.api.general import addressing
#from rest_framework_swagger.views import get_swagger_view
from libs.swagger_views import SwaggerSchemaView

from core.api.advisorsql import advisorsql
from core.api.sqlarchive import sqlarchive


#schema_view = get_swagger_view(title='Yearning API')

urlpatterns = [
    #url('^$', schema_view),
    url(r'^api-doc/$', SwaggerSchemaView.as_view(), name='docs'),
    url(r'^api/v1/userinfo/(.*)', userinfo.as_view()),
    url(r'^api/v1/audit_grained/(.*)', audit_grained.as_view()),
    url(r'^api/v1/apply_grained/(.*)', apply_grained.as_view()),
    url(r'^api/v1/workorder/(.*)', addressing.as_view()),
    url(r'^api/v1/myorder', order.as_view()),
    url(r'^api/v1/gensql/(.*)', gen_sql.as_view()),
    url(r'^api/v1/management_db', management_db.as_view()),
    url(r'^api/v1/management_ssh', management_ssh.as_view()),
    url(r'^api/v1/audit_sql', audit.as_view()),
    url(r'^api/v1/sqldic/(.*)', dictionary.as_view()),
    url(r'^api/v1/auth_twice', authgroup.as_view()),
    url(r'^api/v1/sqlsyntax/(.*)', sqlorder.as_view()),
    url(r'^api/v1/adminsql/(.*)', adminpremisson.as_view()),
    url(r'^api/v1/record/(.*)', record_order.as_view()),
    url(r'^api/v1/homedata/(.*)', dashboard.as_view()),
    url(r'^api/v1/messages/(.*)', messages.as_view()),
    url(r'^api/v1/otheruser/(.*)', generaluser.as_view()),
    url(r'^api/v1/exportdocx/', exportdoc.as_view()),
    url(r'^api/v1/dingding', dingding.as_view()),
    url(r'^api/v1/detail', order_detail.as_view()),
    url(r'^api/v1/search', search.as_view()),
    url(r'^api/v1/ldapauth', ldapauth.as_view()),
    url(r'^api/v1/global_switch', push_permissions.as_view()),
    url(r'^api/v1/undoOrder', del_order.as_view()),
    url(r'^api/v1/osc/(.*)', osc_step.as_view()),
    url(r'^api/v1/download', downloadFile),
    url(r'^api-token-auth/', login_auth.as_view()),
    url(r'^api/v1/adviorsql',advisorsql.as_view()),
    url(r'^api/v1/sql-archive', sqlarchive.as_view()),
]
urlpatterns = format_suffix_patterns(urlpatterns)
