<template>
  <div style="margin-left: 2%">
    <h1>Yearning v1.4.1</h1>
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
          '1.修复 在多级审核的情况下 用户角色从管理员更改为执行人之后 依然存在于审核人范围内的逻辑问题',
          '2.登陆页面用户名输入框增加自动焦点',
          '3.废除之前的延时执行,更改为定时执行'
        ]
      }
    }
  }
</script>

<style scoped>

</style>
