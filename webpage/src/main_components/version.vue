<template>
  <div style="margin-left: 2%">
    <h1>Yearning v1.4.2</h1>
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
          '1.修复我的工单如法重新提交工单的问题',
          '2.增加DML DDL 提交工单时定时执行的防误操作设置',
          '3.新增工单说明项的tip功能',
          '4.大幅优化查询逻辑，提交查询效率',
          '5.新增查询审核，查询审计的模糊查询功能'
        ]
      }
    }
  }
</script>

<style scoped>

</style>
