<style lang="less">
  @import '../../styles/common.less';
  @import 'components/table.less';
  .demo-spin-icon-load {
    animation: ani-demo-spin 1s linear infinite;
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
      <Col span="6">
      <Card>
        <p slot="title">
          <Icon type="ios-redo"></Icon>
          选择数据库
        </p>
        <div class="edittable-test-con">
          <Form :model="formItem" :label-width="100" ref="formItem" :rules="ruleValidate">
            <Form-item label="机房:" prop="computer_room">
              <Select v-model="formItem.computer_room" placeholder="请选择" @on-change="Connection_Name">
                <Option v-for="i in dataset" :value="i" :key="i">{{ i }}</Option>
              </Select>
            </Form-item>
            <Form-item label="连接名称:" prop="connection_name">
              <Select v-model="formItem.connection_name" placeholder="请选择" @on-change="DataBaseName">
                <Option v-for="i in tableform.sqlname" :value="i.connection_name" :key="i.connection_name" filterable>{{ i.connection_name }}</Option>
              </Select>
            </Form-item>
            <Form-item label="数据库库名:" prop="basename">
              <Select v-model="formItem.basename" placeholder="请选择" @on-change="GetTableName" filterable>
                <Option v-for="item in tableform.basename" :value="item" :key="item">{{ item }}</Option>
              </Select>
            </Form-item>
            <Form-item label="数据库表名:" prop="tablename">
              <Select v-model="formItem.tablename" placeholder="请选择" filterable>
                <Option v-for="item in tableform.info" :value="item" :key="item">{{ item }}</Option>
              </Select>
            </Form-item>
            <Button type="warning" @click="canel()" style="margin-left: 20%">重置</Button>
            <Button type="primary" @click="getinfo()" style="margin-left: 5%">连接</Button>
          </Form>
          <br>
          <Tabs value="order1" style="height: 300px;overflow-y: scroll;">
            <TabPane label="生成语句" name="order1">
              <p v-for="list in sql" style="font-size: 12px;color:#2b85e4"> {{ list }}<br><br></p>
            </TabPane>
            <TabPane label="提交工单" name="order2">
              <Button type="primary" style="margin-left: 25%;margin-top: 20%;" @click.native="orderswitch" size="large">获取工单详情</Button>
            </TabPane>
          </Tabs>
        </div>
      </Card>
      </Col>
      <Col span="18" class="padding-left-10">
      <Card>
        <p slot="title">
          <Icon type="android-remove"></Icon>
          表结构详情
        </p>
        <div class="edittable-table-height-con">
          <Tabs :value="tabs">
            <TabPane label="表字段详情" name="order1" icon="folder">
              <Table :columns="tabcolumns" :data="TableDataNew"></Table>
            </TabPane>
            <TabPane label="添加&删除索引" name="order2" icon="ios-unlocked">
              <editindex :tabledata="indexinfo" :table_name="formItem.tablename" @on-indexdata="getindexconfirm"></editindex>
              <br>
              <br>
              <br>
              <br>
            </TabPane>
          </Tabs>
        </div>
      </Card>
      </Col>
    </Row>

    <Modal v-model="openswitch" @on-ok="commitorder" :ok-text="'提交工单'" width="800">
      <Row>
        <Card>
          <div class="step-header-con">
            <h3 style="margin-left: 35%">Yearning SQL平台审核工单</h3>
          </div>
          <p class="step-content"></p>
          <Form class="step-form" :label-width="100">
            <FormItem label="用户名:">
              <p>{{username}}</p>
            </FormItem>
            <FormItem label="数据库库名:">
              <p>{{formItem.basename}}</p>
            </FormItem>
            <FormItem label="数据库表名:">
              <p>{{formItem.tablename}}</p>
            </FormItem>
            <FormItem label="执行SQL:">
              <p v-for="i in sql">{{i}}</p>
            </FormItem>
            <FormItem label="工单提交说明:" required>
              <Input v-model="formItem.text" placeholder="最多不超过20个字"></Input>
            </FormItem>
            <FormItem label="指定审核人:" required>
              <Select v-model="formItem.assigned" filterable transfer>
                <Option v-for="i in this.assigned" :value="i" :key="i">{{i}}</Option>
              </Select>
            </FormItem>
            <FormItem label="是否备份">
              <RadioGroup v-model="formItem.backup">
                <Radio label="1">是</Radio>
                <Radio label="0">否</Radio>
              </RadioGroup>
            </FormItem>
            <FormItem label="确认提交：" required>
              <Checkbox v-model="pass">确认</Checkbox>
            </FormItem>
          </Form>
        </Card>
      </Row>
    </Modal>
  </div>
</template>

<script>
  import Cookies from 'js-cookie'
  import axios from 'axios'
  import util from '../../libs/util'
  import editindex from './components/ModifyIndex.vue'
  export default {
    components: {
      editindex
    },
    data () {
      return {
        ruleValidate: {
          computer_room: [{
            required: true,
            message: '机房地址不得为空',
            trigger: 'change'
          }],
          connection_name: [{
            required: true,
            message: '连接名不得为空',
            trigger: 'change'
          }],
          basename: [{
            required: true,
            message: '数据库名不得为空',
            trigger: 'change'
          }],
          tablename: [{
            required: true,
            message: '表名不得为空',
            trigger: 'change'
          }],
          text: [{
            required: true,
            message: '说明不得为空',
            trigger: 'change'
          },
            {
              type: 'string',
              max: 20,
              message: '最多20个字',
              trigger: 'blur'
            }
          ]
        },
        dataset: util.computer_room,
        item: {},
        basename: [],
        sqlname: [],
        TableDataNew: [],
        tableform: {
          sqlname: [],
          basename: [],
          info: []
        },
        tabcolumns: [
          {
            title: '字段名',
            key: 'Field'
          },
          {
            title: '字段类型',
            key: 'Type',
            editable: true
          },
          {
            title: '字段是否为空',
            key: 'Null',
            editable: true,
            option: true
          },
          {
            title: '默认值',
            key: 'Default',
            editable: true
          },
          {
            title: '索引类型',
            key: 'Key'
          },
          {
            title: '备注',
            key: 'Extra'
          }
        ],
        username: Cookies.get('user'),
        indexinfo: [],
        sql: [],
        openswitch: false,
        pass: false,
        formItem: {
          text: '',
          computer_room: '',
          connection_name: '',
          basename: '',
          tablename: '',
          backup: 0,
          assigned: ''
        },
        id: null,
        tabs: 'order1',
        assigned: []
      }
    },
    methods: {
      Connection_Name (index) {
        if (index) {
          this.ScreenConnection(index)
        }
      },
      DataBaseName (index) {
        if (index) {
          this.id = this.item.filter(item => {
            if (item.connection_name === index) {
              return item
            }
          })
          axios.put(`${util.url}/workorder/basename`, {
            'id': this.id[0].id
          })
            .then(res => {
              this.tableform.basename = res.data
            })
            .catch(() => {
              this.$Notice.error({
                title: '警告',
                desc: '无法连接数据库!请检查网络'
              })
            })
        }
      },
      ScreenConnection (b) {
        this.tableform.sqlname = this.item.filter(item => {
          if (item.computer_room === b) {
            return item
          }
        })
      },
      GetTableName () {
        if (this.formItem.basename) {
          let data = JSON.stringify(this.formItem)
          axios.put(`${util.url}/workorder/tablename`, {
            'data': data,
            'id': this.id[0].id
          })
            .then(res => {
              this.tableform.info = res.data
            }).catch(error => {
            util.ajanxerrorcode(this, error)
          })
        }
      },
      getdatabases () {
        axios.put(`${util.url}/workorder/connection`, {'permissions_type': 'ddl'})
          .then(res => {
            this.item = res.data['connection']
            this.assigned = res.data['assigend']
          })
          .catch(error => {
            util.ajanxerrorcode(this, error)
          })
      },
      getinfo () {
        this.$refs['formItem'].validate((valid) => {
          if (valid) {
            this.$Spin.show({
              render: (h) => {
                return h('div', [
                  h('Icon', {
                    'class': 'demo-spin-icon-load',
                    props: {
                      type: 'load-c',
                      size: 30
                    }
                  }),
                  h('div', '数据库连接中,请稍后........')
                ])
              }
            })
            this.formItem.table_name = this.formItem.tablename
            axios.put(`${util.url}/workorder/field`, {
              'connection_info': JSON.stringify(this.formItem),
              'id': this.id[0].id
            })
              .then(res => {
                this.TableDataNew = res.data
                this.$Spin.hide()
              })
              .catch(() => {
                this.$Notice.error({
                  title: '警告',
                  desc: '连接失败！详细信息请查看日志'
                })
              })
            this.getindex()
          } else {
            this.$Message.error('表单验证失败!');
          }
        })
      },
      canel () {
        this.sql = []
        this.pass = false
      },
      getindex () {
        if (this.formItem.table_name) {
          axios.put(`${util.url}/workorder/indexdata`, {
            'login': JSON.stringify(this.formItem),
            'table': this.formItem.tablename,
            'id': this.id[0].id
          })
            .then(res => {
              this.indexinfo = res.data
            }).catch(error => {
            util.ajanxerrorcode(this, error)
          })
        }
      },
      getindexconfirm (val) {
        for (let i of val) {
          this.sql.push(i)
        }
      },
      orderswitch () {
        this.openswitch = !this.openswitch
      },
      commitorder () {
        if (this.sql === [] || this.formItem.basename === '' || this.assigned === '' || this.formItem.text === '' || this.formItem.assigned === '') {
          this.$Notice.error({
            title: '警告',
            desc: '工单数据缺失,请检查工单信息是否缺失!'
          })
        } else {
          if (this.pass === true) {
            axios.post(`${util.url}/sqlsyntax/`, {
              'data': JSON.stringify(this.formItem),
              'sql': JSON.stringify(this.sql),
              'user': Cookies.get('user'),
              'type': 1,
              'id': this.id[0].id
            })
              .then(res => {
                this.$Notice.success({
                  title: '通知',
                  desc: res.data
                })
                this.$router.push({
                  name: 'myorder'
                })
              }).catch(error => {
              util.ajanxerrorcode(this, error)
            })
          } else {
            this.$Notice.warning({
              title: '注意',
              desc: '提交工单需点击确认按钮'
            })
          }
        }
      }
    },
    mounted () {
      this.getdatabases()
    }
  }
</script>
