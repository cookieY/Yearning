'''
url table
'''
from django.conf.urls import url, include
from rest_framework.urlpatterns import format_suffix_patterns
from rest_framework_jwt.views import obtain_jwt_token
from core.views import (
    Addressing_Api,
    GenerationOrder_Api,
    SqlDic,
    AuthModel,
    SQLSyntax,
    MainData,
    messages,
    OtherUser,
    Orderdetail
)
from core.adminview import (
    Userinfo_Api,
    ManagementSql_Api,
    AuditSql_Api,
    Admin_dic,
    RecordC,
    ExportDoc,
    DingDing,
    downloadFile
)

urlpatterns = [
    url(r'^api/v1/userinfo/(.*)', Userinfo_Api.as_view()),
    url(r'^api/v1/workorder/(.*)', Addressing_Api.as_view()),
    url(r'^api/v1/sqlorder/(.*)', GenerationOrder_Api.as_view()),
    url(r'^api/v1/mamagement_sql/(.*)', ManagementSql_Api.as_view()),
    url(r'^api/v1/audit_sql', AuditSql_Api.as_view()),
    url(r'^api/v1/sqldic/(.*)', SqlDic.as_view()),
    url(r'^api/v1/auth_twice', AuthModel.as_view()),
    url(r'^api/v1/sqlsyntax/(.*)', SQLSyntax.as_view()),
    url(r'^api/v1/adminsql/(.*)', Admin_dic.as_view()),
    url(r'^api/v1/record/(.*)', RecordC.as_view()),
    url(r'^api/v1/homedata/(.*)', MainData.as_view()),
    url(r'^api/v1/messages/(.*)', messages.as_view()),
    url(r'^api/v1/otheruser/(.*)', OtherUser.as_view()),
    url(r'^api/v1/exportdocx/', ExportDoc.as_view()),
    url(r'^api/v1/dingding', DingDing.as_view()),
    url(r'^api/v1/detail', Orderdetail.as_view()),
    url(r'^api/v1/download', downloadFile),
    url(r'^api-auth/', include('rest_framework.urls', namespace='rest_framework')),
    url(r'^api-token-auth/', obtain_jwt_token),
]
urlpatterns = format_suffix_patterns(urlpatterns)
