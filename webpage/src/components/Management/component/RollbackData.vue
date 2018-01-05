<template>
<table style='width: 100%'>
  <tr style="font-size: 15px">
    <th style="border: 0;width: 60%">执行sql:</th>
    <th style="border: 0;width: 10%" >状态:</th>
    <th style="border: 0;width: 15%">错误信息:</th>
    <th style="border: 0;width: 8%">影响行数:</th>
  </tr>
  <tr v-for="(i,key) in row.sql" style="">
    <td style="background-color: #f8f8f9;font-size: 13px;border: 0">{{row.sql[key]}}</td>
    <td style="background-color: #f8f8f9;font-size: 15px;color: #ff9900;border: 0">{{row.state[key]}}</td>
    <td style="background-color: #f8f8f9;font-size: 10px;border: 0">{{row.error[key]}}</td>
    <td style="background-color: #f8f8f9;font-size: 10px;border: 0">{{row.affectrow[key]}}</td>
  </tr>
</table>
</template>
<script>
export default {
  name: 'RollbackData',
  props: {
    row: Object
  }
}
</script>
