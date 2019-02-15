<style lang="less">
  @import "../../styles/common.less";
  @import "components/table.less";
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
            <div id="showImage" class="margin-bottom-10">
              <Form ref="formItem" :model="formItem" :rules="ruleValidate" :label-width="80">
                <FormItem label="机房:" prop="computer_room">
                  <Select v-model="formItem.computer_room" @on-change="ScreenConnection">
                    <Option v-for="i in datalist.computer_roomlist" :key="i" :value="i">{{i}}</Option>
                  </Select>
                </FormItem>

                <FormItem label="连接名:" prop="connection_name">
                  <Select v-model="formItem.connection_name" @on-change="DataBaseName">
                    <Option
                      v-for="i in datalist.connection_name_list"
                      :value="i.connection_name"
                      :key="i.connection_name"
                    >{{ i.connection_name }}
                    </Option>
                  </Select>
                </FormItem>

                <FormItem label="库名:" prop="basename">
                  <Select v-model="formItem.basename">
                    <Option
                      v-for="item in datalist.basenamelist"
                      :value="item"
                      :key="item"
                    >{{ item }}
                    </Option>
                  </Select>
                </FormItem>

                <FormItem label="工单说明:" prop="text">
                  <Input v-model="formItem.text" placeholder="请输入"></Input>
                </FormItem>

                <FormItem label="指定审核人:" prop="assigned">
                  <Select v-model="formItem.assigned" filterable>
                    <Option v-for="i in this.assigned" :value="i" :key="i">{{i}}</Option>
                  </Select>
                </FormItem>

                <FormItem label="是否备份" required>
                  <RadioGroup v-model="formItem.backup">
                    <Radio label="1">是</Radio>
                    <Radio label="0">否</Radio>
                  </RadioGroup>
                </FormItem>

                <FormItem label="定时执行">
                  <DatePicker format="yyyy-MM-dd HH:mm" type="datetime" placeholder="选择时间点" :options="invalidDate"
                              v-model="formItem.picker" @on-change="formItem.picker=$event"></DatePicker>
                </FormItem>
              </Form>
              <Form :label-width="30">
                <FormItem>
                  <Button type="info" icon="md-brush" @click.native="beautify()">美化</Button>
                  <Button
                    type="error"
                    icon="md-trash"
                    @click.native="ClearForm()"
                    style="margin-left: 10%"
                  >清除
                  </Button>
                </FormItem>

                <FormItem>
                  <Button type="warning" icon="md-search" @click.native="test_sql()" :loading="loading">检测</Button>
                  <Button
                    type="success"
                    icon="ios-redo"
                    @click.native="SubmitSQL()"
                    style="margin-left: 10%"
                    :disabled="this.validate_gen"
                  >提交
                  </Button>
                </FormItem>
              </Form>

              <Alert style="height: 145px">检测表字段提示信息
                <template slot="desc">
                  <p>1.错误等级 0正常,1警告,2错误。</p>
                  <p>2.阶段状态 审核成功,Audit completed</p>
                  <p>3.错误信息 用来表示出错错误信息</p>
                  <p>4.当前检查的sql</p>
                  <p>注:只有错误等级等于0时提交按钮才会激活</p>
                </template>
              </Alert>
            </div>
          </div>
        </Card>
      </Col>
      <Col span="18" class="padding-left-10">
        <Card>
          <p slot="title">
            <Icon type="ios-crop"></Icon>
            填写sql语句
          </p>
          <editor v-model="formItem.textarea" @init="editorInit" @setCompletions="setCompletions"></editor>
          <br>
          <br>
          <Table :columns="columnsName" :data="Testresults" highlight-row></Table>
        </Card>
      </Col>
    </Row>
  </div>
</template>
<script>
  import ICol from '../../../node_modules/iview/src/components/grid/col.vue'
  import axios from 'axios'

  export default {
    components: {
      ICol,
      editor: require('../../libs/editor')
    },
    name: 'SQLsyntax',
    data () {
      return {
        invalidDate: {
          disabledDate (date) {
            return date && date.valueOf() < Date.now() - 86400000
          }
        },
        validate_gen: true,
        formItem: {
          textarea: '',
          computer_room: '',
          connection_name: '',
          basename: '',
          text: '',
          backup: '0',
          assigned: '',
          picker: null
        },
        columnsName: [
          {
            title: 'ID',
            key: 'ID',
            width: 50
          },
          {
            title: '错误等级',
            key: 'errlevel',
            width: 85
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
        Testresults: [],
        item: {},
        datalist: {
          connection_name_list: [],
          basenamelist: [],
          sqllist: [],
          computer_roomlist: []
        },
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
          text: [{
            required: true,
            message: '说明不得为空',
            trigger: 'blur'
          }
          ],
          assigned: [{
            required: true,
            message: '审核人不得为空',
            trigger: 'change'
          }]
        },
        id: null,
        assigned: [],
        wordList: [],
        loading: false
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
      beautify () {
        axios.put(`${this.$config.url}/sqlsyntax/beautify`, {
          'data': this.formItem.textarea
        })
          .then(res => {
            this.formItem.textarea = res.data
          })
          .catch(error => {
            this.$config.err_notice(this, error)
          })
      },
      ScreenConnection (val) {
        this.formItem.connection_name = ''
        this.formItem.basename = ''
        this.datalist.connection_name_list = this.item.filter(item => {
          if (item.computer_room === val) {
            return item
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
              this.datalist.basenamelist = res.data
            })
            .catch(() => {
              this.$config.err_notice(this, '无法连接数据库!请检查网络')
            })
        }
      },
      test_sql () {
        let ddl = ['select', 'alter', 'drop', 'create']
        let createtable = this.formItem.textarea.replace(/(;|；)$/gi, '').replace(/\s/g, ' ').replace(/；/g, ';').split(';')
        for (let i of createtable) {
          for (let c of ddl) {
            i = i.replace(/(^\s*)|(\s*$)/g, '')
            if (i.toLowerCase().indexOf(c) === 0) {
              this.$Message.error('不可提交非DML语句!')
              return false
            }
          }
        }
        this.$refs['formItem'].validate((valid) => {
          if (valid) {
            this.loading = true
            if (this.formItem.textarea) {
              let tmp = this.formItem.textarea.replace(/(;|；)$/gi, '').replace(/；/g, ';')
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
                  this.loading = false
                })
                .catch(() => {
                  this.loading = false
                  this.$config.err_notice(this, '无法连接到Inception!')
                })
            } else {
              this.$Message.error('请填写sql语句后再测试!')
            }
          }
        })
      },
      SubmitSQL () {
        this.$refs['formItem'].validate((valid) => {
          if (valid) {
            if (this.formItem.textarea) {
              this.datalist.sqllist = this.formItem.textarea.replace(/(;|；)$/gi, '').replace(/\s/g, ' ').replace(/；/g, ';').split(';')
              axios.post(`${this.$config.url}/sqlsyntax/`, {
                'data': JSON.stringify(this.formItem),
                'sql': JSON.stringify(this.datalist.sqllist),
                'real_name': sessionStorage.getItem('real_name'),
                'type': 1,
                'id': this.id[0].id
              })
                .then(res => {
                  this.$Notice.success({
                    title: '成功',
                    desc: res.data
                  })
                })
                .catch(error => {
                  this.$config.err_notice(this, error)
                })
              this.validate_gen = true
            } else {
              this.$Message.error('请填写sql语句后再提交!')
            }
          } else {
            this.$Message.error('表单验证失败!')
          }
        })
      },
      ClearForm () {
        this.$refs['formItem'].resetFields()
      }
    },
    mounted () {
      axios.put(`${this.$config.url}/workorder/connection`, {'permissions_type': 'dml'})
        .then(res => {
          this.item = res.data['connection']
          this.assigned = res.data['assigend']
          this.datalist.computer_roomlist = res.data['custom']
        })
        .catch(error => {
          this.$config.err_notice(this, error)
        })
      for (let i of this.$config.highlight.split('|')) {
        this.wordList.push({'vl': i, 'meta': '关键字'})
      }
    }
  }
</script>
