<style lang="less">
  @import '../../styles/common.less';
  @import 'components/table.less';

  .demo-spin-icon-load {
    animation: ani-demo-spin 1s linear infinite;
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
                  <Option v-for="i in tableform.sqlname" :value="i.connection_name" :key="i.connection_name" filterable>
                    {{ i.connection_name }}
                  </Option>
                </Select>
              </Form-item>
              <Form-item label="数据库库名:" prop="basename">
                <Select v-model="formItem.basename" placeholder="请选择" @on-change="GetTableName" filterable>
                  <Option v-for="item in tableform.basename" :value="item" :key="item">{{ item }}</Option>
                </Select>
              </Form-item>
              <Form-item label="数据库表名:">
                <Select v-model="formItem.tablename" placeholder="请选择" filterable>
                  <Option v-for="item in tableform.info" :value="item" :key="item">{{ item }}</Option>
                </Select>
              </Form-item>
              <Button type="warning" @click="canel()" style="margin-left: 15%">重置</Button>
              <Button type="primary" @click="getinfo()" style="margin-left: 3%">连接</Button>
              <Button type="success" @click="confirmsql()" style="margin-left: 3%">生成</Button>
            </Form>
            <br>
            <Tabs value="order1" style="height: 300px;overflow-y: scroll;">
              <TabPane label="DDL语句" name="order1">
                <p v-for="list in sql" style="font-size: 12px;color:#2b85e4"> {{ list }}<br><br></p>
              </TabPane>
              <TabPane label="提交工单" name="order2">
                <Button type="primary" style="margin-left: 25%;margin-top: 20%;" @click.native="orderswitch"
                        size="large">获取工单详情
                </Button>
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
              <TabPane label="手动模式" name="order1" icon="edit">
                <Form>
                  <FormItem>
                    <editor v-model="formDynamic" @init="editorInit"></editor>
                  </FormItem>
                  <FormItem>
                    <Table :columns="columnsName" :data="Testresults" highlight-row></Table>
                  </FormItem>
                  <FormItem>
                    <Button type="warning" @click="test_sql">检测</Button>
                    <Button type="primary" @click="handleSubmit(formDynamic)" style="margin-left: 3%"
                            :disabled="this.validate_gen">提交到DDL语句
                    </Button>
                  </FormItem>
                </Form>
              </TabPane>
              <TabPane label="生成添加字段" name="order3" icon="plus">
                <Table stripe :columns="addcolums" :data="add_row" height="385" border></Table>
                <div style="margin-top: 5%">
                  <Input v-model="Add_tmp.Field" placeholder="字段名" style="width: 10%"></Input>
                  <Select v-model="Add_tmp.Species" style="width: 15%" transfer placeholder="字段类型">
                    <Option v-for="i in optionData" :key="i" :value="i">{{i}}</Option>
                  </Select>
                  <Input v-model="Add_tmp.Len" placeholder="字段长度" style="width: 10%"></Input>
                  <Select v-model="Add_tmp.Null" style="width: 15%" placeholder="字段可以为空" transfer>
                    <Option value="YES">YES</Option>
                    <Option value="NO">NO</Option>
                  </Select>
                  <Input v-model="Add_tmp.Default" placeholder="默认值" style="width: 15%"></Input>
                  <Input v-model="Add_tmp.Extra" placeholder="字段备注" style="width: 15%"></Input>
                  <Button type="warning" @click.native="ClearColumns">清空</Button>
                  <Button type="info" @click.native="AddColumns()">添加</Button>
                </div>
              </TabPane>
              <TabPane label="生成修改&删除字段" name="order4" icon="edit">
                <edittable refs="table2" v-model="TableDataNew" :columns-list="tabcolumns" @index="remove"
                           @on-change="cell_change"></edittable>
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
            <h3>Yearning SQL平台审核工单</h3>
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
                <Option v-for="i in assigned" :value="i" :key="i">{{i}}</Option>
              </Select>
            </FormItem>
            <FormItem label="延迟执行">
              <InputNumber
                v-model="formItem.delay"
                :formatter="value => `${value}分钟`"
                :parser="value => value.replace('分钟', '')"
                :min="0">
              </InputNumber>
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
  //
  import axios from 'axios'
  import util from '../../libs/util'
  import edittable from './components/editTable'
  import ICol from 'iview/src/components/grid/col'

  export default {
    components: {
      ICol,
      edittable,
      editor: require('../../libs/editor')
    },
    data () {
      return {
        dataset: [],
        item: {},
        basename: [],
        sqlname: [],
        TableDataNew: [],
        tableform: {
          sqlname: [],
          basename: [],
          info: []
        },
        columnsName: [
          {
            title: 'ID',
            key: 'ID',
            width: 50
          },
          {
            title: '阶段',
            key: 'stage',
            width: 100
          },
          {
            title: '错误等级',
            key: 'errlevel',
            width: 100
          },
          {
            title: '阶段状态',
            key: 'stagestatus',
            width: 150
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
            key: 'affected_rows',
            width: 130
          }
        ],
        Testresults: [],
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
          },
          {
            title: '操作',
            align: 'center',
            width: 190,
            key: 'handle',
            handle: ['edit', 'delete']
          }
        ],
        putdata: [],
        Add_tmp: {
          Field: '',
          Type: '',
          Null: null,
          Default: null,
          Extra: null,
          Len: '',
          Species: null
        },
        add_row: [],
        username: sessionStorage.getItem('user'),
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
          backup: '0',
          assigned: '',
          delay: 0
        },
        id: null,
        tabs: 'order1',
        optionData: [
          'varchar',
          'int',
          'char',
          'tinytext',
          'text',
          'mediumtext',
          'longtext',
          'blob',
          'mediumblob',
          'longblob',
          'tinyint',
          'smallint',
          'mediumint',
          'bigint',
          'time',
          'year',
          'date',
          'datetime',
          'timestamp',
          'decimal',
          'float',
          'double',
          'jason'
        ],
        assigned: [],
        formDynamic: '',
        validate_gen: true
      }
    },
    methods: {
      editorInit: function () {
        require('brace/mode/mysql')
        require('brace/theme/xcode')
      },
      Connection_Name (index) {
        if (index) {
          this.ScreenConnection(index)
        }
      },
      test_sql () {
        let ddl = ['select', 'insert', 'update', 'delete']
        let createtable = this.formDynamic.split(';')
        for (let i of createtable) {
          for (let c of ddl) {
            i = i.replace(/(^\s*)|(\s*$)/g, '')
            if (i.toLowerCase().indexOf(c) === 0) {
              this.$Message.error('不可提交非DDL语句!')
              return false
            }
          }
        }
        this.$refs['formItem'].validate((valid) => {
          if (valid) {
            let tmp = this.formDynamic.replace(/(;|；)$/gi, '').replace(/；/g, ';')
            axios.put(`${util.url}/sqlsyntax/test`, {
              'id': this.id[0].id,
              'base': this.formItem.basename,
              'sql': tmp
            })
              .then(res => {
                if (res.data.status === 200) {
                  this.Testresults = res.data.result
                  let gen = 0
                  this.Testresults.forEach(vl => {
                    if (vl.errlevel !== 0) {
                      gen += 1
                    }
                  })
                  if (gen === 0) {
                    this.validate_gen = false
                  } else {
                    this.validate_gen = true
                  }
                } else {
                  util.err_notice('无法连接到Inception!')
                }
              })
              .catch(error => {
                util.err_notice(error)
              })
          } else {
            this.$Message.error('请填写具体地址或sql语句后再测试!')
          }
        })
      },
      handleSubmit () {
        let createtable = this.formDynamic.replace(/(;|；)$/gi, '').replace(/\s/g, ' ').replace(/；/g, ';').split(';')
        this.validate_gen = true
        for (let i of createtable) {
          this.sql.push(i)
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
              util.err_notice('无法连接数据库!请检查网络')
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
            util.err_notice(error)
          })
        }
      },
      getdatabases () {
        axios.put(`${util.url}/workorder/connection`, {'permissions_type': 'ddl'})
          .then(res => {
            this.item = res.data['connection']
            this.assigned = res.data['assigend']
            this.dataset = res.data['custom']
          })
          .catch(error => {
            util.err_notice(error)
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
                util.err_notice('连接失败！详细信息请查看日志')
              })
            this.getindex()
          } else {
            this.$Message.error('表单验证失败!')
          }
        })
      },
      AddColumns () {
        if (this.Add_tmp.Field === '' || this.Add_tmp.Null === null || this.Add_tmp.Species === '') {
          this.$Notice.warning({
            title: '字段名,是否为空，类型为必填项'
          })
        } else {
          if (this.Add_tmp.Extra) {
            this.Add_tmp.Extra = this.Add_tmp.Extra.replace(/\s+/g, '')
          }
          if (this.Add_tmp.Len !== '') {
            this.Add_tmp.Type = `${this.Add_tmp.Species}(${this.Add_tmp.Len})`
          } else {
            this.Add_tmp.Type = `${this.Add_tmp.Species}`
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
        this.putdata.push({
          'del': index,
          'table_name': this.formItem.tablename
        })
      },
      canel () {
        this.sql = []
        this.pass = false
        this.getinfo()
      },
      confirmsql () {
        if (this.Add_tmp.Field !== '') {
          util.notice('请将需要添加的字段添加进入临时表或者删除!')
        } else {
          this.putdata.push({
            'add': this.add_row,
            'table_name': this.formItem.tablename
          })
          axios.put(`${util.url}/gensql/sql`, {
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
            util.err_notice(error)
          })
        }
      },
      orderswitch () {
        this.openswitch = !this.openswitch
      },
      commitorder () {
        if (this.sql === [] || this.formItem.basename === '' || this.assigned === '' || this.formItem.text === '' || this.formItem.assigned === '') {
          util.err_notice('工单数据缺失,请检查工单信息是否缺失!')
        } else {
          if (this.pass === true) {
            axios.post(`${util.url}/sqlsyntax/`, {
              'data': JSON.stringify(this.formItem),
              'sql': JSON.stringify(this.sql),
              'user': sessionStorage.getItem('user'),
              'type': 0,
              'id': this.id[0].id
            })
              .then(res => {
                util.notice(res.data)
                this.$router.push({
                  name: 'myorder'
                })
              }).catch(error => {
              util.err_notice(error)
            })
          } else {
            util.err_notice('提交工单需点击确认按钮')
          }
        }
      },
      cell_change (data) {
        this.putdata.push({
          'edit': data,
          'table_name': this.formItem.tablename
        })
      }
    },
    mounted () {
      this.getdatabases()
    }
  }
</script>
