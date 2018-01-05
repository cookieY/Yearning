<template>
<div style="width:100%;height:100%;" id="data_source_con"></div>
</template>

<script>
import axios from 'axios'
import util from '../../../libs/util'
const echarts = require('echarts');
export default {
  name: 'dataSourcePie',
  mounted () {
    this.$nextTick(() => {
      var dataSourcePie = echarts.init(document.getElementById('data_source_con'));
      axios.get(`${util.url}/homedata/pie`)
        .then(res => {
          let piedata = [{
              value: parseInt(res.data[0].alter_number),
              name: '申请表结构变更数',
              itemStyle: {
                normal: {
                  color: '#9bd598'
                }
              }
            },
            {
              value: parseInt(res.data[1].sql_number),
              name: '申请SQL变更数',
              itemStyle: {
                normal: {
                  color: '#ffd58f'
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
              data: ['申请表结构变更数', '申请SQL变更数']
            },
            series: [{
              name: '变更数',
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
          util.ajanxerrorcode(this, error)
        })
    });
  }
};
</script>
