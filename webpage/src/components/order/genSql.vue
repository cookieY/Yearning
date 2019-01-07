<style lang="less">
  @import '../../styles/common.less';
  @import 'components/table.less';

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
                  <Option v-for="i in tableform.sqlname" :value="i.connection_name" :key="i.connection_name">
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
              <Form-item>
                <Button type="primary" @click="getinfo()" style="margin-left: 3%">获取表结构信息</Button>
              </Form-item>
              <FormItem label="工单提交说明:" required>
                <Input v-model="formItem.text" placeholder="请输入工单说明"></Input>
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
              <FormItem label="是否备份" prop="backup">
                <RadioGroup v-model="formItem.backup">
                  <Radio label="1">是</Radio>
                  <Radio label="0">否</Radio>
                </RadioGroup>
              </FormItem>
              <FormItem>
                <Button type="warning" @click="canel()">重置</Button>
                <Button type="success" style="margin-left: 3%" @click="commitorder" :disabled="validate_gen">提交</Button>
              </FormItem>
            </Form>
          </div>
        </Card>
      </Col>
      <Col span="18" class="padding-left-10">
        <Card>
          <p slot="title">
            <Icon type="md-remove"></Icon>
            填写SQL语句
          </p>
          <div class="edittable-table-height-con">
            <Tabs :value="tabs">
              <TabPane label="填写SQL语句" name="order1" icon="md-code">
                <Form>
                  <FormItem>
                    <editor v-model="formDynamic" @init="editorInit" @setCompletions="setCompletions"></editor>
                  </FormItem>
                  <FormItem>
                    <Table :columns="columnsName" :data="Testresults" highlight-row></Table>
                  </FormItem>
                  <FormItem>
                    <Button type="warning" @click="test_sql">检测</Button>
                  </FormItem>
                </Form>
              </TabPane>
              <TabPane label="表结构详情" name="order3" icon="md-add">
                <Table :columns="tabcolumns" :data="TableDataNew"></Table>
              </TabPane>
            </Tabs>
          </div>
        </Card>
      </Col>
    </Row>
  </div>
</template>

<script>
  //
  import axios from 'axios'

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
        username: sessionStorage.getItem('user'),
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
            message: '提交说明不得为空',
            trigger: 'change'
          }
          ],
          backup: {required: true, message: '备份不得为空', trigger: 'change'}
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
        validate_gen: true,
        wordList: []
      }
    },
    methods: {
      setCompletions (editor, session, pos, prefix, callback) {
        callback(null, this.wordList.map(function (word) {
          return {
            caption: word.vl,
            value: word.vl,
            meta: word.meta
          }
        }))
      },
      editorInit: function () {
        require('brace/mode/mysql')
        require('brace/theme/xcode')
      },
      Connection_Name (index) {
        if (index) {
          this.tableform.sqlname = this.item.filter(item => {
            if (item.computer_room === index) {
              return item
            }
          })
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
            axios.put(`${this.$config.url}/sqlsyntax/test`, {
              'id': this.id[0].id,
              'base': this.formItem.basename,
              'sql': tmp
            })
              .then(res => {
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
              })
              .catch(() => {
                this.$config.err_notice('无法连接到Inception!')
              })
          } else {
            this.$Message.error('请填写具体地址或sql语句后再测试!')
          }
        })
      },
      DataBaseName (index) {
        if (index) {
          this.id = this.item.filter(item => {
            if (item.connection_name === index) {
              return item
            }
          })
          axios.put(`${this.$config.url}/workorder/basename`, {
            'id': this.id[0].id
          })
            .then(res => {
              this.tableform.basename = res.data
            })
            .catch(() => {
              this.$config.err_notice('无法连接数据库!请检查网络')
            })
        }
      },
      GetTableName () {
        if (this.formItem.basename) {
          let data = JSON.stringify(this.formItem)
          axios.put(`${this.$config.url}/workorder/tablename`, {
            'data': data,
            'id': this.id[0].id
          })
            .then(res => {
              this.tableform.info = res.data
            }).catch(error => {
            this.$config.err_notice(error)
          })
        }
      },
      getdatabases () {
        axios.put(`${this.$config.url}/workorder/connection`, {'permissions_type': 'ddl'})
          .then(res => {
            this.item = res.data['connection']
            this.assigned = res.data['assigend']
            this.dataset = res.data['custom']
          })
          .catch(error => {
            this.$config.err_notice(error)
          })
      },
      getinfo () {
        this.$refs['formItem'].validate((valid) => {
          if (valid) {
            this.$Spin.show({
              render: (h) => {
                return h('div', [
                  h('Icon', {
                    props: {
                      size: 30,
                      type: 'ios-loading'
                    },
                    style: {
                      animation: 'ani-demo-spin 1s linear infinite'
                    }
                  }),
                  h('div', '数据库连接中,请稍后........')
                ])
              }
            })
            this.formItem.table_name = this.formItem.tablename
            axios.put(`${this.$config.url}/workorder/field`, {
              'connection_info': JSON.stringify(this.formItem),
              'id': this.id[0].id
            })
              .then(res => {
                this.TableDataNew = res.data
                this.$Spin.hide()
              })
              .catch(() => {
                this.$config.err_notice('连接失败！详细信息请查看日志')
                this.$Spin.hide()
              })
            this.getindex()
          } else {
            this.$Message.error('表单验证失败!')
          }
        })
      },
      canel () {
        this.$refs['formItem'].resetFields()
      },
      commitorder () {
        this.$refs['formItem'].validate((valid) => {
          if (valid) {
            let sql = this.formDynamic.replace(/(;|；)$/gi, '').replace(/\s/g, ' ').replace(/；/g, ';').split(';')
            axios.post(`${this.$config.url}/sqlsyntax/`, {
              'data': JSON.stringify(this.formItem),
              'sql': JSON.stringify(sql),
              'real_name': sessionStorage.getItem('real_name'),
              'type': 0,
              'id': this.id[0].id
            })
              .then(res => {
                this.$config.notice(res.data)
                this.$router.push({
                  name: 'myorder'
                })
              })
              .catch(error => {
                this.$config.err_notice(error)
              })
          }
        })
      }
    },
    mounted () {
      for (let i of this.$config.highlight.split('|')) {
        this.wordList.push({'vl': i, 'meta': '关键字'})
      }
      this.getdatabases()
    }
  }
</script>
