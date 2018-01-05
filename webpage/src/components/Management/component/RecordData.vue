<template>
<table style='width: 100%'>
  <tr style="font-size: 15px">
    <th style="border: 0;width: 60%">执行sql:</th>
    <th style="border: 0;width: 10%" >状态:</th>
    <th style="border: 0;width: 15%">错误信息:</th>
    <th style="border: 0;width: 8%">影响行数:</th>
    <th style="border: 0;width: 7%">opid:</th>
  </tr>
  <tr v-for="(i,key) in row.sql" style="">
    <td style="background-color: #f8f8f9;font-size: 13px;border: 0;">{{row.sql[key]}}</td>
    <td style="background-color: #f8f8f9;font-size: 15px;color: #ff9900;border: 0">{{row.state[key]}}</td>
    <td style="background-color: #f8f8f9;font-size: 10px;border: 0">{{row.error[key]}}</td>
    <td style="background-color: #f8f8f9;font-size: 10px;border: 0">{{row.affectrow[key]}}</td>
    <td style="background-color: #f8f8f9;font-size: 10px;border: 0">{{row.sequence[key]}}</td>
  </tr>
</table>
</template>
<script>
export default {
  name: 'RecordData',
  props: {
    row: Object
  },
  data () {
    return {
      columns: [{
          title: 'sql语句',
          key: 'sql'
        },
        {
          title: '状态',
          key: 'state',
          render: (h, params) => {
            const row = params.row
            const color = row.state === 'Execute Successfully' ? 'green' : row.state !== 'Execute Successfully' ? 'red' : 'red'
            const text = row.state === 'Execute Successfully' ? '执行成功' : row.state !== 'Execute Successfully' ? 'red' : 'red'

            return h('Tag', {
              props: {
                type: 'dot',
                color: color
              }
            }, text)
          },
          sortable: true,
          filters: [{
              label: '同意',
              value: 1
            },
            {
              label: '驳回',
              value: 2
            },
            {
              label: '待批准',
              value: 3
            }
          ],
          //            filterMultiple: false 禁止多选,
          filterMethod (value, row) {
            if (value === 1) {
              return row.state === '同意'
            } else if (value === 2) {
              return row.state === '驳回'
            } else if (value === 3) {
              return row.state === '待批准'
            }
          }
        },
        {
          title: '错误信息',
          key: 'error'
        }
      ],
      dataSet: []
    }
  },
  mounted () {
    for (let i = 0; i < this.row.sql.length; i++) {
      this.dataSet.push({
        'sql': this.row.sql[i],
        'state': this.row.state[i],
        'error': this.row.error[i]
      })
    }
  }
}
</script>
