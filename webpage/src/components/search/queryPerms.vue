<template>
  <div>
    <Row>
      <Card>
        <p slot="title">
          <Icon type="md-person"></Icon>
          查询申请
        </p>
        <a type="primary" icon="md-add" @click="add_query_perm()" slot="extra">
          <Icon type="md-add-circle"></Icon>
          添加其他权限
        </a>
        <Row>
          <Col span="24">
            <Table border :columns="permissoncolums" :data="query_info" stripe ref="selection"
                   @on-selection-change="delrecordList"></Table>
            <br>
          </Col>
        </Row>
      </Card>
    </Row>

    <Modal v-model="editInfodModal" :width="800">
      <h3 slot="header" style="color:#2D8CF0">查询申请单详细内容</h3>
      <Form :label-width="120" label-position="right">
        <FormItem label="机房">
          <p>{{query.computer_room}}</p>
        </FormItem>
        <hr style="height:1px;border:none;border-top:1px dashed #dddee1;"/>
        <br>
        <FormItem label="连接名:">
          <p>{{query.connection_name}}</p>
        </FormItem>
        <hr style="height:1px;border:none;border-top:1px dashed #dddee1;"/>
        <br>
        <hr style="height:1px;border:none;border-top:1px dashed #dddee1;"/>
        <br>
        <FormItem label="导出数据:">
          <p v-if="query.export === 1">是</p>
          <p v-else>否</p>
        </FormItem>
        <hr style="height:1px;border:none;border-top:1px dashed #dddee1;"/>
        <br>
        <FormItem label="查询说明:">
          <Input v-model="query.instructions" type="textarea" :autosize="{minRows: 5,maxRows: 8}" readonly></Input>
        </FormItem>
      </Form>
      <div slot="footer">
        <Button type="primary" @click="editInfodModal=false">关闭</Button>
      </div>
    </Modal>

  </div>
</template>

<script>
  import axios from 'axios'
  import util from '../../libs/util'

  export default {
    name: 'submit_Page',
    data () {
      return {
        query_info: [],
        permissoncolums: [
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
            title: '数据库连接名称',
            key: 'connection_name'
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
                color = 'primary'
                text = '待审核'
              } else if (row.query_per === 0) {
                color = 'error'
                text = '驳回'
              } else if (row.query_per === 1) {
                color = 'success'
                text = '同意'
              } else {
                color = 'warning'
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
              if (params.row.query_per === 1) {
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
                  }, '查看'),
                  h('Button', {
                    props: {
                      size: 'small',
                      type: 'text'
                    },
                    on: {
                      click: () => {
                        this.stop_query(params.row)
                      }
                    }
                  }, '中止查询')
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
                        this.modalinfo(params.row)
                      }
                    }
                  }, '查看')
                ])
              }
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
            this.query_info = res.data['data'].filter(item => (item.query_per === 1 || item.query_per === 2))
            this.per_pn = res.data['pn']
          })
          .catch(error => {
            util.err_notice(error)
          })
      },
      modalinfo (vl) {
        this.editInfodModal = true
        this.query = vl
      },
      stop_query (vl) {
        axios.put(`${util.url}/query_worklf`, {'mode': 'end', 'work_id': vl.work_id})
          .then(res => {
            util.notice(res.data)
            this.permisson_list()
          })
          .catch(err => util.err_notice(err))
        this.$router.push({
          name: 'querypage'
        })
      },
      add_query_perm () {
        this.$router.push({
          name: 'serach-perm'
        })
      },
      delrecordData () {
        axios.post(`${util.url}/query_order/`, {'work_id': JSON.stringify(this.delrecord)})
          .then(res => {
            util.notice(res.data)
            this.$refs.perpage.currentPage = 1
            this.permisson_list()
          })
          .catch(error => {
            util.err_notice(error)
          })
      },
      delrecordList (vl) {
        this.delrecord = vl.map(vl => vl.work_id)
      }
    },
    mounted () {
      this.permisson_list()
    }
  }
</script>

<style scoped>

</style>
