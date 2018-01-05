<style lang="less">
@import '../../styles/common.less';
@import 'components/table.less';
.demo-spin-icon-load {
    animation: ani-demo-spin 1s linear infinite;
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
          <TabPane label="添加字段" name="order1" icon="plus">
            <Table stripe :columns="addcolums" :data="add_row" height="385" border></Table>
            <div style="margin-top: 5%">
              <Input v-model="Add_tmp.Field" placeholder="字段名" style="width: 10%"></Input>
              <Input v-model="Add_tmp.Type" placeholder="类型及长度" style="width: 10%"></Input>
              <Select v-model="Add_tmp.Null" style="width: 15%" placeholder="字段可以为空" clearable>
                      <Option value="YES">YES</Option>
                      <Option value="NO">NO</Option>
              </Select>
              <Input v-model="Add_tmp.Default" placeholder="默认值" style="width: 15%"></Input>
              <Input v-model="Add_tmp.Extra" placeholder="字段备注" style="width: 15%"></Input>
              <Button type="warning" @click.native="ClearColumns">清空</Button>
              <Button type="info" @click.native="AddColumns()">添加</Button>
            </div>
            <br>
            <br>
            <br>
            <br>
          </TabPane>

          <TabPane label="修改&删除字段" name="order2" icon="edit">
            <Table stripe :columns="tabcolumns" :data="TableDataNew" border style="margin-left: 1%"></Table>
            <br>
            <Button type="info" @click="confirmsql()" style="margin-left: 80%">生成</Button>
          </TabPane>

          <TabPane label="添加&删除索引" name="order3" icon="ios-unlocked">
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
          <FormItem label="工单提交说明:">
            <Input v-model="formItem.text" placeholder="最多不超过20个字"></Input>
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
import expandRow from './components/FieldsTableData.vue'
import axios from 'axios'
import util from '../../libs/util'
import editindex from './components/ModifyIndex.vue'
export default {
  components: {
    expandRow,
    editindex
  },
  data () {
    return {
      dataset: util.computer_room,
      item: {},
      basename: [],
      sqlname: [],
      TableDataOld: [],
      TableDataNew: [],
      tableform: {
        sqlname: [],
        basename: [],
        info: []
      },
      tabcolumns: [
        {
          type: 'expand',
          width: 50,
          render: (h, params) => {
            return h(expandRow, {
              props: {
                row: params.row
              }
            })
          }
        },
        {
          title: '字段名',
          key: 'Field'
        },
        {
          title: '操作',
          key: 'action',
          width: 150,
          align: 'center',
          render: (h, params) => {
            return h('div', [
              h('Button', {
                props: {
                  size: 'small',
                  type: 'success'
                },
                style: {
                  marginRight: '5px'
                },
                on: {
                  click: () => {
                    this.edit_tab(params)
                  }
                }
              }, '修改确认'),
              h('Button', {
                props: {
                  size: 'small',
                  type: 'warning'
                },
                on: {
                  click: () => {
                    this.remove(params.index)
                  }
                }
              }, '删除')
            ])
          }
        }
      ],
      putdata: [],
      Add_tmp: {
        Field: '',
        Type: '',
        Null: null,
        Default: null,
        Extra: null
      },
      add_row: [],
      username: Cookies.get('user'),
      addcolums: [
        {
          title: '字段名',
          key: 'Field'
        },
        {
          title: '字段类型',
          key: 'Type'
        },
        {
          title: '是否为空',
          key: 'Null'
        },
        {
          title: '默认值',
          key: 'Default'
        },
        {
          title: '备注',
          key: 'Extra'
        },
        {
          title: 'action',
          width: 80,
          render: (h, params) => {
            return h('Button', {
              props: {
                type: 'text'
              },
              on: {
                click: () => {
                  this.$Notice.error({
                    title: `${this.add_row[params.index].Field}-临时字段删除成功!`
                  })
                  this.add_row.splice(params.index, 1)
                }
              }
            }, '删除')
          }
        }
      ],
      indexinfo: [],
      sql: [],
      openswitch: false,
      pass: false,
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
      formItem: {
        text: '',
        computer_room: '',
        connection_name: '',
        basename: '',
        tablename: '',
        backup: 0
      },
      id: null,
      tabs: 'order1'
    };
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
      this.delinfo()
      axios.put(`${util.url}/workorder/connection`)
        .then(res => {
          this.item = res.data
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
              this.TableDataOld = res.data
              this.TableDataNew = Array.from(this.TableDataOld)
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
    AddColumns () {
      if (this.Add_tmp.Field === '' || this.Add_tmp.Null === null || this.Add_tmp.Type === '') {
        this.$Notice.warning({
          title: '字段名,是否为空，类型为必填项'
        })
      } else {
        if (this.Add_tmp.Extra) {
          this.Add_tmp.Extra = this.Add_tmp.Extra.replace(/\s+/g, '')
        }
        this.add_row.push(JSON.parse(JSON.stringify(this.Add_tmp)))
        for (let c of Object.keys(this.Add_tmp)) {
          this.Add_tmp[c] = ''
          this.Add_tmp.Default = null
          this.Add_tmp.Extra = null
        }
      }
    },
    ClearColumns () {
      this.Add_tmp = {}
    },
    remove (index) {
      this.$Notice.error({
        title: `${this.TableDataNew[index].Field}-字段删除成功!`
      })
      this.putdata.push({
        'del': this.TableDataNew[index],
        'table_name': this.formItem.tablename
      })
      this.TableDataNew.splice(index, 1)
      this.TableDataOld.splice(index, 1)
    },
    canel () {
      this.$refs['formItem'].resetFields();
      this.delinfo()
    },
    edit_tab (col) {
      this.TableDataNew[col.index] = col.row
      this.$Notice.success({
        title: `${col.row.Field}-字段修改成功!`
      })
    },
    confirmsql () {
      if (this.Add_tmp.Field !== '') {
        this.$Notice.warning({
          title: '警告',
          desc: '请将需要添加的字段添加进入临时表或者删除!'
        })
      } else {
        this.TableDataNew.forEach((item, i) => {
          if (this.TableDataNew[i].Type === this.TableDataOld[i].Type &&
            this.TableDataNew[i].Field === this.TableDataOld[i].Field &&
            this.TableDataNew[i].Default === this.TableDataOld[i].Default &&
            this.TableDataNew[i].Extra === this.TableDataOld[i].Extra &&
            this.TableDataNew[i].Null === this.TableDataOld[i].Null) {} else {
            this.putdata.push({
              'edit': this.TableDataNew[i],
              'table_name': this.formItem.tablename
            })
          }
        })
        this.putdata.push({
          'add': this.add_row,
          'table_name': this.formItem.tablename
        })
        axios.put(`${util.url}/sqlorder/sql`, {
            'data': JSON.stringify(this.putdata),
            'basename': this.formItem.basename
          })
          .then(res => {
            for (let i of res.data) {
              this.sql.push(i)
            }
            this.putdata = []
            this.add_row = []
          }).catch(error => {
            util.ajanxerrorcode(this, error)
          })
      }
    },
    delinfo () {
      this.tableform.sqlname = []
      this.tableform.basename = []
      this.tableform.info = []
      this.formItem.connection_name = ''
      this.formItem.computer_room = ''
      this.formItem.basename = ''
      this.formItem.table_name = ''
      this.formItem.tablename = ''
      this.TableDataOld = []
      this.TableDataNew = []
      this.sql = []
      this.pass = false
      this.indexinfo = []
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
      if (this.pass === true) {
        axios.post(`${util.url}/sqlsyntax/`, {
            'data': JSON.stringify(this.formItem),
            'sql': JSON.stringify(this.sql),
            'user': Cookies.get('user'),
            'type': 0,
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
  },
  mounted () {
    this.getdatabases()
  }
}
</script>
