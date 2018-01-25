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
        审核工单
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
        <Table border :columns="columns6" :data="tmp" stripe ref="selection" @on-selection-change="delrecordList"></Table>
        <br>
        <Page :total="pagenumber" show-elevator @on-change="splicpage" :page-size="20" ref="page"></Page>
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
      <Button @click="cancel_button">取消</Button>
      <Button type="error" @click="out_button()" :disabled="summit">驳回</Button>
      <Button type="success" @click="put_button()" :disabled="summit">同意</Button>
    </div>
  </Modal>

  <Modal v-model="reject.reje" @on-ok="rejecttext">
    <p slot="header" style="color:#f60;font-size: 16px">
      <Icon type="information-circled"></Icon>
      <span>SQL工单驳回理由说明</span>
    </p>
    <Input v-model="reject.textarea" type="textarea" :autosize="{minRows: 15,maxRows: 15}" placeholder="请填写驳回说明"></Input>
  </Modal>
</div>
</template>
<script>
import axios from 'axios'
import Cookies from 'js-cookie'
import util from '../../libs/util'
export default {
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
              text = '审核中'
            } else if (row.status === 0) {
              color = 'red'
              text = '拒绝'
            } else if (row.status === 1) {
              color = 'green'
              text = '同意'
            } else {
              color = 'yellow'
              text = '进行中'
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
              label: '拒绝',
              value: 0
            },
            {
              label: '审核中',
              value: 2
            },
            {
              label: '进行中',
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
          width: 100,
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
                    this.edit_tab(params.index)
                  }
                }
              }, '查看')
            ])
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
      summit: false,
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
      togoing: null
    }
  },
  methods: {
    edit_tab: function (index) {
      this.togoing = index
      this.dataId = []
      this.modal2 = true
      if (this.tmp[index].status === 2) {
        this.summit = false
        this.formitem = this.tmp[index]
        this.sql = this.tmp[index].sql.split(';')
      } else {
        this.formitem = this.tmp[index]
        this.sql = this.tmp[index].sql.split(';')
        this.summit = true
      }
    },
    cancel_button () {
      this.modal2 = false
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
          this.mou_data()
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
      axios.put(`${util.url}/audit_sql`, {
          'type': 'test',
          'base': this.formitem.basename,
          'id': this.formitem.id
        })
        .then(res => {
          if (res.data.status === 200) {
            this.dataId = res.data.result
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
    splicpage (page) {
      this.mou_data(page)
    },
    mou_data (vl = 1) {
      axios.get(`${util.url}/audit_sql?page=${vl}&username=${Cookies.get('user')}`)
        .then(res => {
          this.tmp = res.data.data
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
      axios.post(`${util.url}/audit_sql`, {
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
    }
  },
  mounted () {
    this.mou_data()
  }
}
</script>
<!-- remove delete request -->
