<template>
<div style="width:100%;height:100%;" id="data_source_con"></div>
</template>

<script>
import axios from 'axios'
const echarts = require('echarts');
export default {
  name: 'dataSourcePie',
  mounted () {
    this.$nextTick(() => {
      var dataSourcePie = echarts.init(document.getElementById('data_source_con'));
      axios.get(`${this.$config.url}/homedata/pie`)
        .then(res => {
          let piedata = [{
              value: parseInt(res.data[0]),
              name: 'DDL工单提交数',
              itemStyle: {
                normal: {
                  color: '#80848f'
                }
              }
            },
            {
              value: parseInt(res.data[1]),
              name: 'DML工单提交数',
              itemStyle: {
                normal: {
                  color: '#5cadff'
                }
              }
            }
          ]
          const option = {
            tooltip: {
              trigger: 'item',
              formatter: '{a} <br/>{b} : {c} ({d}%)'
            },
            legend: {
              orient: 'vertical',
              left: 'right',
              data: ['DDL工单提交数', 'DML工单提交数']
            },
            series: [{
              name: '提交工单数',
              type: 'pie',
              radius: '66%',
              center: ['50%', '60%'],
              data: piedata,
              itemStyle: {
                emphasis: {
                  shadowBlur: 10,
                  shadowOffsetX: 0,
                  shadowColor: 'rgba(0, 0, 0, 0.5)'
                }
              }
            }]
          }
          dataSourcePie.setOption(option);
          window.addEventListener('resize', function () {
            dataSourcePie.resize();
          });
        })
        .catch(error => {
          this.$config.err_notice(this, error)
        })
    });
  }
};
</script>
