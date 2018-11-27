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
        <Row>
          <Col span="24">
            <Table border :columns="columns" :data="table_data" stripe size="small"></Table>
          </Col>
        </Row>
        <br>
        <Page :total="page_number" show-elevator @on-change="currentpage" :page-size="20"></Page>
      </Card>
    </Row>

    <Modal v-model="modal2" width="1000">
      <p slot="header" style="color:#f60;font-size: 16px">
        <Icon type="information-circled"></Icon>
        <span>SQL工单详细信息</span>
      </p>
      <Form label-position="right">
        <FormItem label="工单编号:">
          <span>{{ formitem.work_id }}</span>
        </FormItem>
        <FormItem label="机房:">
          <span>{{ formitem.computer_room }}</span>
        </FormItem>
        <FormItem label="连接名称:">
          <span>{{ formitem.connection_name }}</span>
        </FormItem>
        <FormItem label="数据库库名:">
          <span>{{ formitem.basename }}</span>
        </FormItem>
        <FormItem label="延迟执行:">
          <span>{{ formitem.delay }}分钟</span>
        </FormItem>
        <FormItem label="工单说明:">
          <span>{{ formitem.text }}</span>
        </FormItem>
        <FormItem>
          <Table :columns="sql_columns" :data="sql" height="200"></Table>
          <template v-if="auth === 'admin' || auth === 'manager'">
            <p class="pa">SQL检查结果:</p>
            <Table :columns="columnsName" :data="dataId" stripe border height="200"></Table>
          </template>
        </FormItem>
        <FormItem label="选择执行人:" v-if="multi ">
          <Select v-model="multi_name" style="width: 20%" clearable v-if="formitem.status !== 1">
            <Option v-for="i in multi_list" :value="i.username" :key="i.username">{{i.auth_group+':'+i.username}}</Option>
          </Select>
          <span v-if="formitem.status === 1">{{ formitem.exceuser }}</span>
        </FormItem>
      </Form>


      <div slot="footer">
        <template v-if="auth === 'admin' || auth === 'manager'">
          <Button @click="modal2 = false">取消</Button>
          <template v-if="formitem.status === 2">
            <Button type="warning" @click.native="test_button()" >检测sql</Button>
            <Button type="error" @click="out_button()" :disabled="summit">驳回</Button>
            <template v-if="multi && multi_name">
              <Button type="success" @click="agreed_button()" :disabled="summit">同意</Button>
            </template>
            <template v-else>
              <Button type="success" @click="put_button()" :disabled="summit">执行</Button>
            </template>
          </template>
          <template v-else-if="formitem.status === 1">
            <Button type="warning" @click.native="test_button()" >检测sql</Button>
            <Button type="error" @click="out_button()" :disabled="summit">拒绝</Button>
            <Button type="success" @click="put_button()" :disabled="summit">执行</Button>
          </template>
        </template>
      </div>
    </Modal>
  </div>
</template>
<script>
  //
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
              if (row.status === 0) {
                color = 'error'
                text = '已驳回'
              } else if (row.status === 1) {
                color = 'primary'
                text = '待执行'
              } else if (row.status === 2) {
                color = 'primary'
                text = '待审核'
              } else if (row.status === 3) {
                color = 'warning'
                text = '执行中'
              } else if (row.status === 4) {
                color = 'error'
                text = '已失败'
              } else {
                color = 'success'
                text = '已完成'
              }
              return h('Tag', {
                props: {
                  type: 'dot',
                  color: color
                }
              }, text)
            },
            sortable: true,
            filters: [
              {
                label: '驳回',
                value: 0
              },
              {
                label: '待执行',
                value: 1
              },
              {
                label: '待审核',
                value: 2
              },
              {
                label: '执行中',
                value: 3
              },
              {
                label: '执行失败',
                value: 4
              },
              {
                label: '执行完成',
                value: 5
              }
            ],
            //            filterMultiple: false 禁止多选,
            filterMethod (value, row) {
              if (value === 1) {
                return row.status === 1
              } else if (value === 2) {
                return row.status === 2
              } else if (value === 0) {
                return row.status === 0
              } else if (value === 3) {
                return row.status === 3
              } else {
                return row.status === 4
              }
            }
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
              } else if (params.row.status === 5 || params.row.status === 4) {
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
              } else {
                return h('div', [
                  h('Button', {
                    props: {
                      size: 'small',
                      type: 'text'
                    },
                    on: {
                      click: () => {
                        this.summit = true
                        this.edit_tab(params.index)
                      }
                    }
                  }, '查看')
                ])
              }
            }
          }
        ],
        page_number: 1,
        computer_room: util.computer_room,
        table_data: [],
        modal2: false,
        summit: false,
        formitem: {},
        multi: Boolean,
        auth: sessionStorage.getItem('auth'),
        sql: [],
        sql_columns: [],
        multi_list: {},
        multi_name: ''
      }
    },
    methods: {
      currentpage (vl = 1) {
        axios.get(`${util.url}/myorder/?user=${sessionStorage.getItem('user')}&page=${vl}`)
          .then(res => {
            this.table_data = res.data.data
            this.table_data.forEach((item) => { (item.backup === 1) ? item.backup = '是' : item.backup = '否' })
            this.page_number = parseInt(res.data.page)
          })
          .catch(error => {
            util.err_notice(error)
          })
      },
      edit_tab: function (index) {
        this.sql = []
        this.togoing = index
        this.dataId = []
        this.modal2 = true
        this.formitem = this.table_data[index]
        this.table_data[index].status
        let tmpSql = this.table_data[index].sql.split(';')
        for (let i of tmpSql) {
          this.sql.push({'sql': i})
        }
      }
    },
    mounted () {
      this.currentpage()
    }
  }
</script>
<!-- remove delete request -->
