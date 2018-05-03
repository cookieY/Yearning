<template>
  <div>
    <Row>
      <Card>
        <p slot="title">
          <Icon type="person"></Icon>
          查询审核
        </p>
        <Row>
          <Col span="24">
            <Poptip
              confirm
              title="您确认删除这些工单信息吗?"
              @on-ok="delrecordData"
            >
              <Button type="text" style="margin-left: -1%">删除记录</Button>
            </Poptip>
            <Table border :columns="permissoncolums" :data="query_info" stripe ref="selection" @on-selection-change="delrecordList"></Table>
            <br>
            <Page :total="per_pn" show-elevator @on-change="permisson_list" :page-size="20" ref="perpage"></Page>
          </Col>
        </Row>
      </Card>
    </Row>

    <Modal v-model="editInfodModal"  :width="800">
      <h3 slot="header" style="color:#2D8CF0">查询申请单详细内容</h3>
      <Form :label-width="120" label-position="right">
          <FormItem label="机房">
            <p>{{query.computer_room}}</p>
          </FormItem>
          <hr style="height:1px;border:none;border-top:1px dashed #dddee1;" />
          <br>
          <FormItem label="连接名:">
            <p>{{query.connection_name}}</p>
          </FormItem>
          <hr style="height:1px;border:none;border-top:1px dashed #dddee1;" />
          <br>
          <FormItem label="查询时限:">
            <p>{{query.timer}}分钟</p>
          </FormItem>
          <hr style="height:1px;border:none;border-top:1px dashed #dddee1;" />
          <br>
          <FormItem label="导出数据:">
            <p v-if="query.export === 1">是</p>
            <p v-else>否</p>
          </FormItem>
        <hr style="height:1px;border:none;border-top:1px dashed #dddee1;" />
        <br>
        <FormItem label="查询说明:">
          <Input v-model="query.instructions" type="textarea" :autosize="{minRows: 5,maxRows: 8}" readonly></Input>
        </FormItem>
      </Form>
      <div slot="footer">
        <Button type="primary" @click="editInfodModal=false">取消</Button>
        <Button type="error" @click="reject" v-if="query.query_per === 2">驳回</Button>
        <Button type="success" @click="savedata" v-if="query.query_per === 2">同意</Button>
      </div>
    </Modal>

  </div>
</template>

<script>
  import axios from 'axios'
  import util from '../../libs/util'
  export default {
    name: 'Query_audit',
    data () {
      return {
        query_info: [],
        permissoncolums: [
          {
            type: 'selection',
            width: 60,
            align: 'center'
          },
          {
            title: '申请编号',
            key: 'work_id'
          },
          {
            title: '时间',
            key: 'time'
          },
          {
            title: '申请人',
            key: 'username'
          },
          {
            title: '状态',
            key: 'query_per',
            width: 150,
            render: (h, params) => {
              const row = params.row
              let color = ''
              let text = ''
              if (row.query_per === 2) {
                color = 'blue'
                text = '待审核'
              } else if (row.query_per === 0) {
                color = 'red'
                text = '驳回'
              } else if (row.query_per === 1) {
                color = 'green'
                text = '同意'
              } else {
                color = 'yellow'
                text = '查询结束'
              }
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
                value: 0
              },
              {
                label: '待审核',
                value: 2
              },
              {
                label: '查询结束',
                value: 3
              }
            ],
            //            filterMultiple: false 禁止多选,
            filterMethod (value, row) {
              if (value === 1) {
                return row.query_per === 1
              } else if (value === 2) {
                return row.query_per === 2
              } else if (value === 0) {
                return row.query_per === 0
              } else if (value === 3) {
                return row.query_per === 3
              }
            }
          },
          {
            title: '操作',
            key: 'action',
            width: 200,
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
                      this.modalinfo(params.row)
                    }
                  }
                }, '查看')
              ])
            }
          }
        ],
        per_pn: 1,
        delrecord: [],
        editInfodModal: false,
        query: {}
      }
    },
    methods: {
      permisson_list (vl = 1) {
        axios.get(`${util.url}/query_order?page=${vl}`)
          .then(res => {
            this.query_info = res.data['data']
            this.per_pn = res.data['pn']
          })
          .catch(error => {
            util.ajanxerrorcode(this, error)
          })
      },
      delrecordData () {
        axios.post(`${util.url}/query_order/`, {'work_id': JSON.stringify(this.delrecord)})
          .then(res => {
            this.$Notice.info({
              title: '通知',
              desc: res.data
            })
            this.$refs.perpage.currentPage = 1
            this.permisson_list()
          })
          .catch(error => {
            util.ajanxerrorcode(this, error)
          })
      },
      delrecordList (vl) {
        this.delrecord = vl.map(vl => vl.work_id)
      },
      modalinfo (vl) {
        this.editInfodModal = true
        this.query = vl
      },
      savedata () {
        axios.put(`${util.url}/query_worklf/`,
          {
            'mode': 'agree',
            'work_id': this.query.work_id
          })
          .then(res => {
            this.$Notice.info({
              title: '通知',
              desc: res.data
            })
            this.editInfodModal = false
            this.$refs.perpage.currentPage = 1
            this.permisson_list()
          })
          .catch(error => {
            util.ajanxerrorcode(this, error)
          })
      },
      reject () {
        axios.put(`${util.url}/query_worklf/`,
          {
            'mode': 'disagree',
            'work_id': this.query.work_id
          })
          .then(res => {
            this.$Notice.info({
              title: '通知',
              desc: res.data
            })
            this.editInfodModal = false
            this.$refs.perpage.currentPage = 1
            this.permisson_list()
          })
          .catch(error => {
            util.ajanxerrorcode(this, error)
          })
      }
    },
    mounted () {
      this.permisson_list()
    }
  }
</script>

<style scoped>

</style>
