<style lang="less">
@import '../../styles/common.less';
@import '../Order/components/table.less';
</style>
<template>
<div>
  <Row>
    <Card>
      <p slot="title">
        <Icon type="person"></Icon>
        查询审计
      </p>
      <Row>
        <Col span="24">
        <Table border :columns="columns" :data="table_data" stripe size="small"></Table>
        </Col>
      </Row>
      <br>
      <Page :total="page_number" show-elevator @on-change="currentpage" :page-size="10"></Page>
    </Card>
  </Row>
</div>
</template>
<script>
import axios from 'axios'
import util from '../../libs/util'
export default {
  name: 'put',
  data () {
    return {
      columns: [
        {
          title: '工单编号:',
          key: 'work_id',
          sortable: true
        },
        {
          title: '查询人',
          key: 'username'
        },
        {
          title: '工单说明',
          key: 'instructions'
        },
        {
          title: '提交时间:',
          key: 'date',
          sortable: true
        },
        {
          title: '查询时限',
          key: 'timer'
        },
        {
          title: '操作',
          key: 'action',
          align: 'center',
          render: (h, params) => {
            return h('div', [
              h('Button', {
                props: {
                  size: 'small',
                  type: 'text'
                },
                on: {
                  click: () => {
                    this.$router.push({
                      name: 'querylist',
                      query: {workid: params.row.work_id, user: params.row.username}
                    })
                  }
                }
              }, '详细信息')
            ])
          }
        }
      ],
      page_number: 1,
      computer_room: util.computer_room,
      table_data: []
    }
  },
  methods: {
    currentpage (vl = 1) {
      axios.get(`${util.url}/query_worklf?page=${vl}`)
        .then(res => {
          this.table_data = res.data.data
          this.page_number = parseInt(res.data.page.alter_number)
        })
        .catch(error => {
          util.ajanxerrorcode(this, error)
        })
    }
  },
  mounted () {
    this.currentpage()
  }
}
</script>
<!-- remove delete request -->
