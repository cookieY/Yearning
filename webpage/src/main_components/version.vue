<template>
  <div style="margin-left: 2%">
    <h1>Yearning v1.3.5</h1>
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
          '1.修复查询无法使用show create table语句',
          '2.修复了查询使用as语句可以逃避脱敏规则的问题',
          '3.修复了查询相关的安全漏洞',
          '4.修复查询字段类型为time,timestamp时查询异常的问题',
          '5.前端代码重构一期',
          '6.修复osc进度条无法展示的问题',
          '7.修复ldap下角色为执行人的用户无法执行审批好的工单'
        ]
      }
    }
  }
</script>

<style scoped>

</style>
