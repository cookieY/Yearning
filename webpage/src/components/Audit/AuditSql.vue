<style lang="less">
@import '../../styles/common.less';
@import '../Order/components/table.less';
.demo-Circle-custom{
  & h1{
    color: #3f414d;
    font-size: 28px;
    font-weight: normal;
  }
  & p{
    color: #657180;
    font-size: 14px;
    margin: 10px 0 15px;
  }
  & span{
    display: block;
    padding-top: 15px;
    color: #657180;
    font-size: 14px;
    &:before{
      content: '';
      display: block;
      width: 50px;
      height: 1px;
      margin: 0 auto;
      background: #e0e3e6;
      position: relative;
      top: -15px;
    };
  }
  & span i{
    font-style: normal;
    color: #3f414d;
  }
}
p{
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
        <Icon type="person"></Icon>
        工单审核
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
            <Button type="text" style="margin-left: -1%" @click.native="mou_data()">刷新</Button>
            <Table border :columns="columns6" :data="tmp" stripe ref="selection" @on-selection-change="delrecordList"></Table>
            <br>
            <Page :total="pagenumber" show-elevator @on-change="mou_data" :page-size="20" ref="page"></Page>
        </Col>
      </Row>
    </Card>
  </Row>
  <Modal v-model="modal2" width="800">
    <p slot="header" style="color:#f60;font-size: 16px">
      <Icon type="information-circled"></Icon>
      <span>SQL工单详细信息</span>
    </p>
    <Form label-position="right">
      <FormItem label="id:">
        <span>{{ formitem.id }}</span>
      </FormItem>
      <FormItem label="工单编号:">
        <span>{{ formitem.work_id }}</span>
      </FormItem>
      <FormItem label="提交时间:">
        <span>{{ formitem.date }}</span>
      </FormItem>
      <FormItem label="提交人:">
        <span>{{ formitem.username }}</span>
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
      <FormItem label="工单说明:">
        <span>{{ formitem.text }}</span>
      </FormItem>
      <FormItem label="SQL语句:">
        <p v-for="i in sql">{{ i }}</p>
      </FormItem>
    </Form>
    <p class="pa">SQL检查结果:</p>
    <Table :columns="columnsName" :data="dataId" stripe border></Table>
    <div slot="footer">
      <Button type="warning" @click.native="test_button()">检测sql</Button>
      <Button @click="modal2 = false">取消</Button>
      <template v-if="switch_show">
      <Button type="error" @click="out_button()" :disabled="summit">驳回</Button>
      <Button type="success" @click="put_button()" :disabled="summit">同意</Button>
      </template>
    </div>
  </Modal>

  <Modal v-model="reject.reje" @on-ok="rejecttext">
    <p slot="header" style="color:#f60;font-size: 16px">
      <Icon type="information-circled"></Icon>
      <span>SQL工单驳回理由说明</span>
    </p>
    <Input v-model="reject.textarea" type="textarea" :autosize="{minRows: 15,maxRows: 15}" placeholder="请填写驳回说明"></Input>
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
import Cookies from 'js-cookie'
import util from '../../libs/util'
import ICircle from 'iview/src/components/circle/circle'
export default {
  components: {ICircle},
  name: 'Sqltable',
  data () {
    return {
      columns6: [
        {
          type: 'selection',
          width: 60,
          align: 'center'
        },
        {
          title: '工单编号:',
          key: 'work_id',
          sortable: true,
          sortType: 'desc',
          width: 250
        },
        {
          title: '工单说明:',
          key: 'text'
        },
        {
          title: '是否备份',
          key: 'backup'
        },
        {
          title: '提交时间:',
          key: 'date',
          sortable: true,
          width: 150
        },
        {
          title: '提交人',
          key: 'username',
          sortable: true,
          width: 150
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
              color = 'blue'
              text = '待审核'
            } else if (row.status === 0) {
              color = 'red'
              text = '驳回'
            } else if (row.status === 1) {
              color = 'green'
              text = '已执行'
            } else {
              color = 'yellow'
              text = '执行中'
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
              label: '已执行',
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
              label: '执行中',
              value: 3
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
            }
          }
        },
        {
          title: '操作',
          key: 'action',
          width: 200,
          align: 'center',
          render: (h, params) => {
            if (params.row.status !== 1) {
              if (params.row.status === 3 && params.row.type === 0) {
                return h('div', [
                  h('Button', {
                    props: {
                      size: 'small',
                      type: 'text'
                    },
                    on: {
                      click: () => {
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
                        this.edit_tab(params.index)
                      }
                    }
                  }, '查看')
                ])
              }
            } else {
              return h('div', [
                h('Button', {
                  props: {
                    size: 'small',
                    type: 'text'
                  },
                  on: {
                    click: () => {
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
            }
          }
        }
      ],
      modal2: false,
      sql: null,
      formitem: {
        workid: '',
        date: '',
        username: '',
        dataadd: '',
        database: '',
        att: '',
        id: null
      },
      summit: true,
      columnsName: [
        {
          title: 'ID',
          key: 'ID',
          width: '50'
        },
        {
          title: '阶段',
          key: 'stage',
          width: '100'
        },
        {
          title: '错误等级',
          key: 'errlevel',
          width: '85'
        },
        {
          title: '阶段状态',
          key: 'stagestatus'
        },
        {
          title: '错误信息',
          key: 'errormessage'
        },
        {
          title: '当前检查的sql',
          key: 'sql'
        },
        {
          title: '预计影响的SQL',
          key: 'affected_rows'
        },
        {
          title: 'SQLSHA1',
          key: 'SQLSHA1'
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
      switch_show: true
    }
  },
  methods: {
    edit_tab: function (index) {
      this.togoing = index
      this.dataId = []
      this.modal2 = true
      this.formitem = this.tmp[index]
      this.tmp[index].status === 2 ? this.switch_show = true : this.switch_show = false
      this.sql = this.tmp[index].sql.split(';')
    },
    put_button () {
      this.modal2 = false
      this.tmp[this.togoing].status = 3
      axios.put(`${util.url}/audit_sql`, {
          'type': 1,
          'from_user': Cookies.get('user'),
          'to_user': this.formitem.username,
          'id': this.formitem.id
        })
        .then(res => {
          this.$Notice.success({
            title: '执行成功',
            desc: res.data
          })
          this.$refs.page.currentPage = 1
        })
        .catch(error => {
          util.ajanxerrorcode(this, error)
        })
    },
    out_button () {
      this.modal2 = false
      this.reject.reje = true
    },
    rejecttext () {
      axios.put(`${util.url}/audit_sql`, {
          'type': 0,
          'from_user': Cookies.get('user'),
          'text': this.reject.textarea,
          'to_user': this.formitem.username,
          'id': this.formitem.id
        })
        .then(res => {
          this.$Notice.warning({
            title: res.data
          })
          this.mou_data()
          this.$refs.page.currentPage = 1
        })
        .catch(error => {
          util.ajanxerrorcode(this, error)
        })
    },
    test_button () {
      this.osclist = []
      axios.put(`${util.url}/audit_sql`, {
          'type': 'test',
          'base': this.formitem.basename,
          'id': this.formitem.id
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
            this.$Notice.error({
              title: '警告',
              desc: '无法连接到Inception!'
            })
          }
        })
        .catch(error => {
          util.ajanxerrorcode(this, error)
        })
    },
    mou_data (vl = 1) {
      axios.get(`${util.url}/audit_sql?page=${vl}&username=${Cookies.get('user')}`)
        .then(res => {
          this.tmp = res.data.data
          this.tmp.forEach((item) => { (item.backup === 1) ? item.backup = '是' : item.backup = '否' })
          this.pagenumber = res.data.page.alter_number
        })
        .catch(error => {
          util.ajanxerrorcode(this, error)
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
          this.$Notice.info({
            title: '信息',
            desc: res.data
          })
          this.mou_data()
        })
        .catch(error => {
          util.ajanxerrorcode(this, error)
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
            this.$Notice.info({
              title: '通知',
              desc: res.data
            })
        })
        .catch(error => console.log(error))
    }
  },
  mounted () {
    this.mou_data()
  }
}
</script>
<!-- remove delete request -->
