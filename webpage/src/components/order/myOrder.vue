<style lang="less">
  @import '../../styles/common.less';
  @import '/components/table.less';
</style>
<template>
  <div>
    <Row>
      <Card>
        <p slot="title">
          <Icon type="md-person"></Icon>
          我的工单
        </p>
        <Form inline ref="queryForm">
          <FormItem>
            <Input placeholder="工单说明" v-model="find.text" @on-keyup.enter="queryData"></Input>
          </FormItem>
          <FormItem>
            <DatePicker format="yyyy-MM-dd HH:mm" type="datetimerange" placeholder="请选择查询的时间范围"
                        v-model="find.picker" @on-change="find.picker=$event" style="width: 250px"></DatePicker>
          </FormItem>
          <FormItem>
            <Button type="success" @click="queryData">查询</Button>
            <Button type="primary" @click="queryCancel">重置</Button>
          </FormItem>
        </Form>
        <Row>
          <Col span="24">
            <Table border :columns="columns" :data="table_data" stripe size="small"></Table>
          </Col>
        </Row>
        <br>
        <Page :total="page_number" show-elevator @on-change="currentpage" :page-size="20"></Page>
      </Card>
    </Row>
  </div>
</template>
<script>
  //
  import axios from 'axios'

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
            title: '工单说明',
            key: 'text'
          },
          {
            title: '是否备份',
            key: 'backup'
          },
          {
            title: '提交时间:',
            key: 'date',
            sortable: true
          },
          {
            title: '提交人',
            key: 'username',
            sortable: true
          },
          {
            title: '状态',
            key: 'status',
            render: (h, params) => {
              const row = params.row
              let color = ''
              let text = ''
              if (row.status === 2) {
                color = 'primary'
                text = '待审核'
              } else if (row.status === 0) {
                color = 'error'
                text = '驳回'
              } else if (row.status === 1) {
                color = 'success'
                text = '已执行'
              } else if (row.status === 4) {
                color = 'error'
                text = '执行失败'
              } else if (row.status === 5) {
                color = 'primary'
                text = '待执行'
              } else {
                color = 'warning'
                text = '执行中'
              }

              return h('Tag', {
                props: {
                  type: 'dot',
                  color: color
                }
              }, text)
            },
            sortable: true
          },
          {
            title: '操作',
            key: 'action',
            align: 'center',
            render: (h, params) => {
              if (params.row.status === 0) {
                return h('div', [
                  h('Button', {
                    props: {
                      size: 'small',
                      type: 'text'
                    },
                    on: {
                      click: () => {
                        this.$router.push({
                          name: 'orderlist',
                          query: {
                            workid: params.row.work_id,
                            id: params.row.id,
                            status: params.row.status,
                            type: params.row.type
                          }
                        })
                      }
                    }
                  }, '详细信息'),
                  h('Button', {
                    props: {
                      size: 'small',
                      type: 'text'
                    },
                    on: {
                      click: () => {
                        this.$Modal.error({
                          title: '驳回理由',
                          content: params.row.rejected
                        })
                      }
                    }
                  }, '驳回理由')
                ])
              } else {
                return h('div', [
                  h('Button', {
                    props: {
                      size: 'small',
                      type: 'text'
                    },
                    on: {
                      click: () => {
                        this.$router.push({
                          name: 'orderlist',
                          query: {
                            workid: params.row.work_id,
                            id: params.row.id,
                            status: params.row.status,
                            type: params.row.type
                          }
                        })
                      }
                    }
                  }, '详细信息')
                ])
              }
            }
          }
        ],
        page_number: 1,
        computer_room: this.$config.computer_room,
        table_data: [],
        find: {
          picker: [],
          valve: false,
          text: ''
        }
      }
    },
    methods: {
      currentpage (vl = 1) {
        axios.get(`${this.$config.url}/myorder/?page=${vl}&query=${JSON.stringify(this.find)}`)
          .then(res => {
            this.table_data = res.data.data
            this.table_data.forEach((item) => { (item.backup === 1) ? item.backup = '是' : item.backup = '否' })
            this.page_number = parseInt(res.data.page)
          })
          .catch(error => {
            this.$config.err_notice(this, error)
          })
      },
      queryData () {
        this.find.valve = true
        this.currentpage()
      },
      queryCancel () {
        this.find = this.$config.clearObj(this.find)
        this.currentpage()
      }
    },
    mounted () {
      this.currentpage()
    }
  }
</script>
<!-- remove delete request -->
