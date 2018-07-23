<template>
  <div style="margin-left: 2%">
    <h1>Yearning v1.3.0</h1>
    <h2>更新内容:</h2>
    <p></p>
    <p v-for="i in word">{{i}}</p>
  </div>

</template>

<script>
  export default {
    name: 'version',
    data () {
      return {
        word: [
          '1.修改了报错提示',
          '2.新增细粒化权限组，废除之前的个人权限划分',
          '3.连接名webook合并至配置页面webhook',
          '4.删除查询时限,新增管理员结束用户查询权限',
          '4.查询页面支持自动补全库名及表名',
          '5.新增版本号接口',
          '6.修复权限审核页面,数据库管理页面低版本chrome或其他浏览器401报错的问题',
          '7.修复查询审核时查询工单提交没有消息推送的问题'
        ]
      }
    }
  }
</script>

<style scoped>

</style>
