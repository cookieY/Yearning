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
            <Form inline>
              <FormItem>
                <Poptip
                  confirm
                  title="您确认删除这些工单信息吗?"
                  @on-ok="delrecordData"
                >
                  <Button type="warning">删除记录</Button>
                </Poptip>
              </FormItem>
              <FormItem>
                <Poptip trigger="hover" title="提示" content="此开关用于打开实时表格数据更新功能">
                  <i-switch v-model="valve" @on-change="refreshForm" size="large">
                    <span slot="open">打开</span>
                    <span slot="close">关闭</span>
                  </i-switch>
                </Poptip>
              </FormItem>
              <FormItem>
                <Input placeholder="账号名" v-model="find.user"></Input>
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
            <Table border :columns="columns" :data="tableData" stripe ref="selection"
                   @on-selection-change="delrecordList">
              <template slot-scope="{ row, index }" slot="action">
                <div>
                  <Button type="text" @click="openOrder(index)" size="small">查看</Button>
                  <Button type="text" @click="orderDetail(row)" v-if="row.status !== 2 && row.status !==3" size="small">
                    执行结果
                  </Button>
                  <Button type="text" @click="openOSC" v-if="row.status === 3 && row.type === 0" size="small">osc进度
                  </Button>
                </div>
              </template>
            </Table>
            <br>
            <Page :total="pagenumber" show-elevator @on-change="refreshData" :page-size="20" ref="page"></Page>
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
        <FormItem label="机房:">
          <span>{{ formitem.computer_room }}</span>
        </FormItem>
        <FormItem label="连接名称:">
          <span>{{ formitem.connection_name }}</span>
        </FormItem>
        <FormItem label="数据库库名:">
          <span>{{ formitem.basename }}</span>
        </FormItem>
        <FormItem label="定时执行:">
          <span>{{ formitem.delay }}</span>
        </FormItem>
        <FormItem label="工单说明:">
          <span>{{ formitem.text }}</span>
        </FormItem>
        <FormItem>
          <Table :columns="sql_columns" :data="sql" height="200"></Table>
        </FormItem>
        <FormItem label="选择执行人:" v-if="multi && auth === 'admin'" required>
          <Select v-model="multi_name" style="width: 20%">
            <Option v-for="i in multi_list" :value="i.username" :key="i.username">{{i.username}}</Option>
          </Select>
        </FormItem>
      </Form>
      <template>
        <p class="pa">SQL检查结果:</p>
        <Table :columns="columnsName" :data="dataId" stripe border height="200"></Table>
      </template>

      <div slot="footer">
        <Button type="warning" @click.native="testTo()" :loading="loading">
          <span v-if="!loading">检测sql</span>
          <span v-else>检测中</span></Button>
        <Button @click="modal2 = false">取消</Button>
        <template v-if="switch_show">
          <template v-if="multi">
            <Button type="error" @click="rejectTo()" :disabled="summit" v-if="auth === 'admin'">驳回</Button>
            <Button type="error" @click="rejectTo()" v-else>驳回</Button>
            <Button type="success" @click="agreedTo()" :disabled="summit" v-if="auth === 'admin'">同意</Button>
            <Button type="success" @click="performTo()" v-else-if="auth === 'perform'">执行</Button>
          </template>
          <template v-else>
            <Button type="error" @click="rejectTo()" :disabled="summit">驳回</Button>
            <Button type="success" @click="performTo()" :disabled="summit">执行</Button>
          </template>
        </template>
      </div>
    </Modal>

    <Modal v-model="reject.reje" @on-ok="rejectText">
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
      @on-ok="stopOSC"
      ok-text="终止osc"
      cancel-text="关闭窗口">
      <Form>
        <FormItem label="SQL语句SHA1值">
          <Select v-model="oscsha1" style="width:70%" @on-change="setpOSC" transfer>
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

  import ICircle from 'iview/src/components/circle/circle'

  export default {
    components: {ICircle},
    name: 'Sqltable',
    data () {
      return {
        loading: false,
        sql_columns: [
          {
            title: 'sql语句',
            key: 'sql'
          }
        ],
        columns: [
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
            key: 'text',
            tooltip: true
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
            title: '真实姓名',
            key: 'real_name',
            sortable: true
          },
          {
            title: '执行人',
            key: 'executor',
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
            width: 150,
            align: 'center',
            slot: 'action'
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
        tableData: [],
        pagenumber: 1,
        delrecord: [],
        togoing: null,
        osc: false,
        oscsha1: '',
        osclist: [],
        percent: 0,
        consuming: '00:00',
        callback_time: null,
        switch_show: true,
        multi: Boolean,
        auth: sessionStorage.getItem('auth'),
        multi_list: {},
        multi_name: '',
        reboot: null,
        valve: true,
        find: {
          picker: [],
          user: '',
          valve: false
        }
      }
    },
    methods: {
      openOrder (index) {
        this.summit = true
        this.sql = []
        this.togoing = index
        this.dataId = []
        this.modal2 = true
        this.formitem = this.tableData[index]
        this.tableData[index].status === 2 ? this.switch_show = true : this.switch_show = false
        axios.get(`${this.$config.url}/getsql?id=${this.formitem.id}&bundle_id=${this.formitem.bundle_id}`)
          .then(res => {
            let tmpSql = res.data.sql.split(';')
            for (let i of tmpSql) {
              this.sql.push({'sql': i})
            }
            this.formitem.computer_room = res.data.comRoom
            this.formitem.connection_name = res.data.conn
          })
          .catch(err => {
            this.$config.err_notice(this, err)
          })
      },
      agreedTo () {
        if (this.multi_name === '') {
          this.$Message.error('请选择执行人!')
        } else {
          axios.put(`${this.$config.url}/audit_sql`, {
            'type': 2,
            'perform': this.multi_name,
            'work_id': this.formitem.work_id,
            'username': this.formitem.username
          })
            .then(res => {
              this.$config.notice(res.data)
              this.modal2 = false
              this.refreshData(this.$refs.page.currentPage)
            })
            .catch(error => {
              this.$config.err_notice(this, error)
            })
        }
      },
      performTo () {
        this.modal2 = false
        this.tableData[this.togoing].status = 3
        axios.put(`${this.$config.url}/audit_sql`, {
          'type': 1,
          'to_user': this.formitem.username,
          'id': this.formitem.id
        })
          .then(res => {
            this.$config.notice(res.data)
            this.refreshData(this.$refs.page.currentPage)
          })
          .catch(error => {
            this.$config.err_notice(this, error)
          })
      },
      rejectTo () {
        this.modal2 = false
        this.reject.reje = true
      },
      testTo () {
        this.loading = true
        this.osclist = []
        axios.put(`${this.$config.url}/audit_sql`, {
          'type': 'test',
          'base': this.formitem.basename,
          'id': this.formitem.id
        })
          .then(res => {
            if (res.data.status === 200) {
              this.dataId = res.data.result
              this.osclist = this.dataId.filter(vl => {
                if (vl.SQLSHA1 !== '') {
                  return vl
                }
              })
              this.summit = false
              this.loading = false
            } else {
              this.$config.err_notice(this, res.data.status)
            }
          })
          .catch(error => {
            this.$config.err_notice(this, error)
          })
      },
      rejectText () {
        axios.put(`${this.$config.url}/audit_sql`, {
          'type': 0,
          'text': this.reject.textarea,
          'to_user': this.formitem.username,
          'id': this.formitem.id
        })
          .then(res => {
            this.$config.notice(res.data)
            this.refreshData(this.$refs.page.currentPage)
          })
          .catch(error => {
            this.$config.err_notice(this, error)
          })
      },
      orderDetail (row) {
        this.$router.push({
          name: 'orderlist',
          query: {workid: row.work_id, id: row.id, status: 1, type: row.type}
        })
      },
      refreshData (vl = 1) {
        axios.get(`${this.$config.url}/audit_sql?page=${vl}&query=${JSON.stringify(this.find)}`)
          .then(res => {
            this.multi = res.data.multi
            if (!this.multi) {
              for (let i = 0; i < this.columns.length; i++) {
                if (this.columns[i].key === 'executor') {
                  this.columns.splice(i, 1)
                }
              }
            }
            this.tableData = res.data.data
            this.tableData.forEach((item) => { (item.backup === 1) ? item.backup = '是' : item.backup = '否' })
            this.pagenumber = res.data.page
            this.multi_list = res.data.multi_list
          })
          .catch(error => {
            this.$config.err_notice(this, error)
          })
      },
      delrecordList (vl) {
        this.delrecord = vl
      },
      delrecordData () {
        let step = this.$refs.page.currentPage
        if (this.tableData.length === this.delrecord.length) {
          step = step - 1
        }
        axios.post(`${this.$config.url}/undoOrder`, {
          'id': JSON.stringify(this.delrecord)
        })
          .then(res => {
            this.$config.notice(res.data)
            this.refreshData(step)
          })
          .catch(error => {
            this.$config.err_notice(this, error)
          })
      },
      setpOSC (vl) {
        let vm = this
        this.callback_time = setInterval(function () {
          axios.get(`${vm.$config.url}/osc/${vl}`)
            .then(res => {
              if (res.data[0] !== undefined) {
                vm.percent = res.data[0].PERCENT
                vm.consuming = res.data[0].REMAINTIME
              } else {
                vm.percent = 100
                clearInterval(vm.callback_time)
              }
            })
            .catch(error => console.log(error))
        }, 1000)
      },
      openOSC () {
        this.oscsha1 = ''
        this.osc = true
      },
      stopOSC () {
        axios.delete(`${this.$config.url}/osc/${this.oscsha1}`)
          .then(res => {
            this.$config.notice(res.data)
          })
          .catch(error => this.$config.err_notice(this, error))
      },
      callback_method () {
        clearInterval(this.callback_time)
      },
      refreshForm (vl) {
        if (vl) {
          let vm = this
          this.reboot = setInterval(function () {
            vm.refreshData(vm.$refs.page.currentPage)
          }, 5000)
        } else {
          clearInterval(this.reboot)
        }
      },
      queryData () {
        this.find.valve = true
        this.refreshData()
      },
      queryCancel () {
        this.find = this.$config.clearObj(this.find)
        this.refreshData()
      }
    },
    mounted () {
      this.refreshData()
      this.refreshForm(this.valve)
    },
    destroyed () {
      clearInterval(this.reboot)
    }
  }
</script>
<!-- remove delete request -->
