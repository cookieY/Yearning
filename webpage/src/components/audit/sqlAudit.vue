<style lang="less">
  @import '../../styles/common.less';
  @import '../order/components/table.less';

  .demo-Circle-custom {
    & h1 {
      color: #3f414d;
      font-size: 28px;
      font-weight: normal;
    }
    & p {
      color: #657180;
      font-size: 14px;
      margin: 10px 0 15px;
    }
    & span {
      display: block;
      padding-top: 15px;
      color: #657180;
      font-size: 14px;
      &:before {
        content: '';
        display: block;
        width: 50px;
        height: 1px;
        margin: 0 auto;
        background: #e0e3e6;
        position: relative;
        top: -15px;
      }
    ;
    }
    & span i {
      font-style: normal;
      color: #3f414d;
    }
  }

  p {
    word-wrap: break-word;
    word-break: break-all;
    overflow: hidden;
  }
</style>
<template>
  <div>
    <Row>
      <Card>
        <p slot="title">
          <Icon type="md-person"></Icon>
          工单审核
        </p>
        <Row>
          <Col span="24">
            <template v-if="auth === 'manager' || auth === 'admin'">
              <Poptip
                confirm
                title="您确认删除这些工单信息吗?"
                @on-ok="delrecordData">
                <Button type="text" >逻辑删除</Button>
              </Poptip>
              <Button type="success" @click.native="mou_data()">刷新</Button>
            </template>
            <Table border :columns="columns6" :data="tmp" stripe ref="selection"
                   @on-selection-change="delrecordList"></Table>
            <br>
            <Page :total="pagenumber" show-elevator @on-change="mou_data" :page-size="20" ref="page"></Page>
          </Col>
        </Row>
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
        <Button @click="modal2 = false">取消</Button>
        <template v-if="formitem.status === 2">
          <template v-if="auth === 'admin' || auth === 'manager'">
            <Button type="warning" @click.native="test_button()" >检测sql</Button>
            <Button type="error" @click="out_button()" :disabled="summit">驳回</Button>
            <template v-if="multi && multi_name">
              <Button type="success" @click="agreed_button()" :disabled="summit">同意</Button>
            </template>
            <template v-else>
              <Button type="success" @click="put_button()" :disabled="summit">执行</Button>
            </template>
          </template>
        </template>
        <template v-else-if="formitem.status === 1">
          <Button type="warning" @click.native="test_button()" >检测sql</Button>
          <Button type="error" @click="out_button()" :disabled="summit">拒绝</Button>
          <Button type="success" @click="put_button()" :disabled="summit">执行</Button>
        </template>

      </div>
    </Modal>

    <Modal v-model="reject.reje" @on-ok="rejecttext">
      <p slot="header" style="color:#f60;font-size: 16px">
        <Icon type="information-circled"></Icon>
        <span>SQL工单驳回理由说明</span>
      </p>
      <Input v-model="reject.textarea" type="textarea" :autosize="{minRows: 15,maxRows: 15}"
             placeholder="请填写驳回说明"></Input>
    </Modal>

    <Modal
      v-model="osc"
      title="osc进度查看窗口"
      :closable="false"
      :mask-closable="false"
      @on-cancel="callback_method"
      @on-ok="stop_osc"
      ok-text="终止osc"
      cancel-text="关闭窗口">
      <Form>
        <FormItem label="SQL语句SHA1值">
          <Select v-model="oscsha1" style="width:70%" @on-change="oscsetp" transfer>
            <Option v-for="item in osclist" :value="item.SQLSHA1" :key="item.SQLSHA1">{{ item.SQLSHA1 }}</Option>
          </Select>
        </FormItem>
        <FormItem label="osc进度详情图表">
          <i-circle
            :size="250"
            :trail-width="4"
            :stroke-width="5"
            :percent="percent"
            stroke-linecap="square"
            stroke-color="#43a3fb">
            <div class="demo-Circle-custom">
              <p>已完成</p>
              <h1>{{percent}}%</h1>
              <br>
              <span>
                距离完成还差
                <i>{{consuming}}</i>
            </span>
            </div>
          </i-circle>
        </FormItem>
      </Form>
    </Modal>


  </div>
</template>
<script>
  import axios from 'axios'
  import util from '../../libs/util'
  import ICircle from 'iview/src/components/circle/circle'

  export default {
    components: {ICircle},
    name: 'Sqltable',
    data () {
      return {
        sql_columns: [
          {
            title: 'sql语句',
            key: 'sql'
          }
        ],
        columns6: [
          {
            type: 'selection',
            width: 60,
            align: 'center',
            fixed: 'left'
          },
          {
            title: '工单编号:',
            key: 'work_id',
            sortable: true,
            sortType: 'desc',
            width: 200
          },
          {
            title: '工单说明:',
            key: 'text'
          },
          {
            title: '是否备份',
            key: 'backup',
            width: 100
          },
          {
            title: '提交时间:',
            key: 'date',
            sortable: true
          },
          {
            title: '提交账号',
            key: 'username',
            sortable: true
          },
          {
            title: '审核人',
            key: 'assigned',
            sortable: true
          },
          {
            title: '执行人',
            key: 'exceuser',
            sortable: true
          },
          {
            title: '状态',
            key: 'status',
            width: 150,
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
            width: 150,
            align: 'center',
            render: (h, params) => {
              if (params.row.status === 5 || params.row.status === 4) {
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
                  }, '查看'),
                  h('Button', {
                    props: {
                      size: 'small',
                      type: 'text'
                    },
                    on: {
                      click: () => {
                        this.$router.push({
                          name: 'orderlist',
                          query: {workid: params.row.work_id, id: params.row.id, status: 1, type: params.row.type}
                        })
                      }
                    }
                  }, '执行结果')
                ])
              } else if (params.row.status === 3) {
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
                  }, '查看'),
                  h('Button', {
                    props: {
                      size: 'small',
                      type: 'text'
                    },
                    on: {
                      click: () => {
                        this.oscsha1 = ''
                        this.osc = true
                      }
                    }
                  }, 'osc进度')
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
        modal2: false,
        sql: [],
        formitem: {
          workid: '',
          date: '',
          username: '',
          dataadd: '',
          database: '',
          att: '',
          id: null,
          delay: null
        },
        summit: true,
        columnsName: [
          {
            title: 'ID',
            key: 'ID',
            width: 60,
            fixed: 'left'
          },
          {
            title: '阶段状态',
            key: 'stagestatus',
            width: 150
          },
          {
            title: '当前检查的sql',
            key: 'sql',
            width: 500
          },
          {
            title: '错误信息',
            key: 'errormessage',
            width: 300
          },
          {
            title: '影响行数',
            key: 'affected_rows',
            width: 90
          },
          {
            title: 'SQLSHA1',
            key: 'SQLSHA1',
            width: 200
          }
        ],
        dataId: [],
        reject: {
          reje: false,
          textarea: ''
        },
        tmp: [],
        pagenumber: 1,
        delrecord: [],
        togoing: null,
        osc: false,
        oscsha1: '',
        osclist: JSON.parse(sessionStorage.getItem('osc')),
        percent: 0,
        consuming: '00:00',
        callback_time: null,
        switch_show: true,
        multi: Boolean,
        auth: sessionStorage.getItem('auth'),
        multi_list: {},
        multi_name: '',
        reboot: null
      }
    },
    methods: {
      edit_tab: function (index) {
        this.sql = []
        this.togoing = index
        this.dataId = []
        this.modal2 = true
        this.formitem = this.tmp[index]
        this.tmp[index].status
        let tmpSql = this.tmp[index].sql.split(';')
        for (let i of tmpSql) {
          this.sql.push({'sql': i})
        }
      },
      agreed_button () {
        if (this.multi_name === '') {
          this.$Message.error('请选择执行人!')
        } else {
          axios.put(`${util.url}/audit_sql`, {
            'status': 1,
            'to_user': this.multi_name,
            'work_id': this.formitem.work_id,
            'username': this.formitem.username
          })
            .then(res => {
              util.notice(res.data)
              this.modal2 = false
            })
            .catch(error => {
              util.err_notice(error)
            })
        }
      },
      put_button () {
        this.modal2 = false
        this.tmp[this.togoing].status = 3
        axios.put(`${util.url}/audit_sql`, {
          'status': 3,
          'from_user': sessionStorage.getItem('user'),
          'to_user': this.formitem.username,
          'work_id': this.formitem.work_id
        })
          .then(res => {
            util.notice(res.data)
            this.$refs.page.currentPage = 1
          })
          .catch(error => {
            util.err_notice(error)
          })
      },
      out_button () {
        this.modal2 = false
        this.reject.reje = true
      },
      rejecttext () {
        axios.put(`${util.url}/audit_sql`, {
          'status': 0,
          'from_user': sessionStorage.getItem('user'),
          'text': this.reject.textarea,
          'work_id': this.formitem.work_id
          })
          .then(res => {
            util.err_notice(res.data)
            this.mou_data()
            this.$refs.page.currentPage = 1
          })
          .catch(error => {
            util.err_notice(error)
          })
      },
      test_button () {
        this.osclist = []
        axios.put(`${util.url}/audit_sql`, {
          'status': 'test',
          'base': this.formitem.basename,
          'work_id': this.formitem.work_id
        })
          .then(res => {
            if (res.data.status === 200) {
              let osclist
              this.dataId = res.data.result
              osclist = this.dataId.filter(vl => {
                if (vl.SQLSHA1 !== '') {
                  return vl
                }
              })
              this.osclist = osclist
              this.summit = false
              sessionStorage.setItem('osc', JSON.stringify(osclist))
            } else {
              util.err_notice(res.data.status)
            }
          })
          .catch(error => {
            util.err_notice(error)
          })
      },
      mou_data (vl = 1) {
        axios.get(`${util.url}/audit_sql?page=${vl}&username=${sessionStorage.getItem('user')}`)
          .then(res => {
            this.tmp = res.data.data
            this.tmp.forEach((item) => { (item.backup === 1) ? item.backup = '是' : item.backup = '否' })
            this.pagenumber = res.data.page
            this.multi = res.data.multi
            this.multi_list = res.data.multi_list
            this.multi_name = res.data.exceuser
          })
          .catch(error => {
            util.err_notice(error)
          })
      },
      delrecordList (vl) {
        this.delrecord = vl
      },
      delrecordData () {
        axios.post(`${util.url}/undoOrder`, {
          'id': JSON.stringify(this.delrecord)
        })
          .then(res => {
            util.notice(res.data)
            this.mou_data()
          })
          .catch(error => {
            util.err_notice(error)
          })
      },
      oscsetp (vl) {
        let vm = this
        this.callback_time = setInterval(function () {
          axios.get(`${util.url}/osc/${vl}`)
            .then(res => {
              if (res.data[0].PERCENT === 99) {
                vm.percent = 100
                clearInterval(vm.callback_time)
              } else {
                vm.percent = res.data[0].PERCENT
              }
              vm.consuming = res.data[0].REMAINTIME
            })
            .catch(error => console.log(error))
        }, 2000)
      },
      callback_method () {
        clearInterval(this.callback_time)
      },
      stop_osc () {
        axios.delete(`${util.url}/osc/${this.oscsha1}`)
          .then(res => {
            util.notice(res.data)
          })
          .catch(error => util.err_notice(error))
      }
    },
    mounted () {
      let vm = this
      this.mou_data()
      this.reboot = setInterval(function () {
        vm.mou_data(vm.$refs.page.currentPage)
      }, 10000)
    },
    destroyed () {
      clearInterval(this.reboot)
    }
  }
</script>
<!-- remove delete request -->
