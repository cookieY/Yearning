<style lang="less">
  @import '../../styles/common.less';
  @import '../Order/components/table.less';

  .demo-spin-icon-load {
    animation: ani-demo-spin 1s linear infinite;
  }
</style>
<template>
  <div>
    <Col span="6">
      <Card>
        <p slot="title">
          <Icon type="load-b"></Icon>
          添加数据库
        </p>
        <div class="edittable-testauto-con">
          <Form ref="formValidate" :model="formItem" :label-width="100" :rules="ruleInline">
            <Form-item label="机房:">
              <Select v-model="formItem.add" placeholder="请选择">
                <Option v-for="list in dataset" :value="list" :key="list">{{ list }}</Option>
              </Select>
            </Form-item>
            <Form-item label="连接名称:" prop="name">
              <Input v-model="formItem.name" placeholder="请输入"></Input>
            </Form-item>
            <Form-item label="数据库地址:" prop="ip">
              <Input v-model="formItem.ip" placeholder="请输入"></Input>
            </Form-item>
            <Form-item label="端口:" prop="port">
              <Input v-model="formItem.port" placeholder="请输入"></Input>
            </Form-item>
            <Form-item label="用户名:" prop="username">
              <Input v-model="formItem.username" placeholder="请输入"></Input>
            </Form-item>
            <Form-item label="密码:" prop="password">
              <Input v-model="formItem.password" placeholder="请输入" type="password"></Input>
            </Form-item>
            <Button type="info" @click="testlink()">测试连接</Button>
            <Button type="success" @click="add()" style="margin-left: 5%">确定</Button>
            <Button type="warning" @click="del()" style="margin-left: 5%">取消</Button>
          </Form>
        </div>
      </Card>
      <Card>
        <Tabs value="name1">
          <TabPane label="字典生成" icon="load-b" name="name1">
            <div class="edittable-testauto-con">
              <Form :model="dictionary" :label-width="80" ref="generation">
                <FormItem label="连接名:" prop="dic">
                  <Select v-model="dictionary.name" placeholder="请选择数据库连接名" style="width: 60%" @on-change="BaseList"
                          transfer>
                    <Option v-for="i in rowdata" :value="i.id" :key="i.connection_name">{{i.connection_name}}</Option>
                  </Select>
                </FormItem>
                <FormItem label="数据库名称:">
                  <Checkbox :indeterminate="dictionary.indeterminate" :value="dictionary.checkAll"
                            @click.prevent.native="dicCheckAll">全选
                  </Checkbox>
                  <CheckboxGroup v-model="dictionary.databases">
                    <Checkbox :label="c" :key="c" v-for="c in dictionary.databasesList"></Checkbox>
                  </CheckboxGroup>
                </FormItem>
                <Button @click.native="Commit" type="info">生成数据字典</Button>
              </Form>
            </div>
          </TabPane>
          <!-- 数据库字典model-->
          <TabPane label="字典删除" name="name2">
            <Form :model="dictionary" :label-width="80">
              <FormItem label="连接名:">
                <Select v-model="dictionary.delname" placeholder="请选择数据库连接名" style="width: 60%" @on-change="getdiclist"
                        transfer>
                  <Option v-for="i in diclist" :value="i.Name" :key="i.Name">{{i.Name}}</Option>
                </Select>
              </FormItem>
              <FormItem label="数据库名称:">
                <CheckboxGroup v-model="dictionary.getdel">
                  <Checkbox :label="c.BaseName" :key="c.BaseName" v-for="c in dictionary.getdellist"></Checkbox>
                </CheckboxGroup>
              </FormItem>
              <Button @click.native="Deletedic" type="warning">删除数据字典</Button>
            </Form>
          </TabPane>
        </Tabs>
      </Card>
    </Col>
    <Col span="18" class="padding-left-10">
      <Card>
        <p slot="title">
          <Icon type="ios-crop-strong"></Icon>
          数据库详情表
        </p>
        <div class="edittable-con-1">
          <Table :columns="columns" :data="rowdata" height="550"></Table>
        </div>
        <br>
        <Page :total="pagenumber" show-elevator @on-change="mountdata" :page-size="10" ref="totol"></Page>
      </Card>
    </Col>
    <Modal v-model="delbaseModal" :width="500">
      <h3 slot="header" style="color:#2D8CF0">删除数据库</h3>
      <Form :label-width="100" label-position="right">
        <FormItem label="数据库连接名">
          <Input v-model="delbasename" readonly="readonly"></Input>
        </FormItem>
        <FormItem label="请输入数据库连接名">
          <Input v-model="delconfirmbasename" placeholder="请确认数据库连接名"></Input>
        </FormItem>
      </Form>
      <div slot="footer">
        <Button type="text" @click="delbaseModal = false">取消</Button>
        <Button type="primary" @click="delbaselink">删除</Button>
      </div>
    </Modal>
    <Modal v-model="addDingding" :width="500">
      <h3 slot="header" style="color:#2D8CF0">添加钉钉推送接口</h3>
      <Form :label-width="100" label-position="right">
        <FormItem label="数据库连接名">
          <Input v-model="dingname" readonly="readonly"></Input>
        </FormItem>
        <FormItem label="提交工单推送的消息内容:">
          <Input v-model="dingdingbeforetext" type="textarea" :autosize="{minRows: 2,maxRows: 5}"></Input>
        </FormItem>
        <FormItem label="审核成功后推送的消息内容:">
          <Input v-model="dingdingaftertext" type="textarea" :autosize="{minRows: 2,maxRows: 5}"></Input>
        </FormItem>
      </Form>
      <div slot="footer">
        <Button type="text" @click="addDingding = false">取消</Button>
        <Button type="primary" @click="savedingding()">添加</Button>
      </div>
    </Modal>

    <Modal v-model="baseinfo" :width="500" okText="保存" @on-ok="update_base">
      <h3 slot="header" style="color:#2D8CF0">数据库连接信息</h3>
      <Form :label-width="100" label-position="right">
        <FormItem label="机房">
          <Input v-model="editbaseinfo.computer_room" readonly></Input>
        </FormItem>
        <FormItem label="连接名称:">
          <Input v-model="editbaseinfo.connection_name" readonly></Input>
        </FormItem>
        <FormItem label="数据库地址:">
          <Input v-model="editbaseinfo.ip"></Input>
        </FormItem>
        <FormItem label="端口:">
          <Input v-model="editbaseinfo.port"></Input>
        </FormItem>
        <FormItem label="用户名:">
          <Input v-model="editbaseinfo.username"></Input>
        </FormItem>
        <FormItem label="密码:">
          <Input v-model="editbaseinfo.password" type="password"></Input>
        </FormItem>
      </Form>
    </Modal>

  </div>
</template>
<script>
  import '../../assets/tablesmargintop.css'
  import axios from 'axios'
  import util from '../../libs/util'
  import ICol from '../../../node_modules/iview/src/components/grid/col'

  export default {
    components: {
      ICol
    },
    name: 'sqlist',
    data () {
      return {
        columns: [
          {
            title: '连接名称',
            key: 'connection_name'
          },
          {
            title: '数据库地址',
            key: 'ip'
          },
          {
            title: '机房',
            key: 'computer_room'
          },
          {
            title: '操作',
            key: 'action',
            width: 300,
            render: (h, params) => {
              return h('div', [
                h('Button', {
                  props: {
                    size: 'small',
                    type: 'info'
                  },
                  on: {
                    click: () => {
                      this.edit_tab(params.index)
                    }
                  }
                }, '查看信息'),
                h('Button', {
                  style: {
                    marginLeft: '8px'
                  },
                  props: {
                    type: 'success',
                    size: 'small'
                  },
                  on: {
                    click: () => {
                      this.dingding(params.row)
                    }
                  }
                }, '钉钉推送信息'),
                h('Button', {
                  style: {
                    marginLeft: '8px'
                  },
                  props: {
                    type: 'warning',
                    size: 'small'
                  },
                  on: {
                    click: () => {
                      this.deldatabases(params.index)
                    }
                  }
                }, '删除')
              ])
            }
          }
        ],
        rowdata: [],
        modal: false,
        // 添加数据库信息
        formItem: {
          name: '',
          ip: '',
          add: '',
          username: '',
          password: '',
          port: ''
        },
        // 添加表单验证规则
        ruleInline: {
          name: [{
            required: true,
            message: '请填写连接名称',
            trigger: 'blur'
          }],
          ip: [{
            required: true,
            message: '请填写连接地址',
            trigger: 'blur'
          }],
          username: [{
            required: true,
            message: '请填写用户名',
            trigger: 'blur'
          }],
          port: [{
            required: true,
            message: '请填写端口',
            trigger: 'blur'
          }],
          password: [{
            required: true,
            message: '请填写密码',
            trigger: 'blur'
          }]
        },
        // 生成字典规则
        dataset: [],
        Generate: {
          textarea: '',
          add: '',
          name: ''
        },
        dictionary: {
          name: '',
          add: '',
          databases: [],
          databasesList: [],
          indeterminate: false,
          checkAll: false,
          getdellist: [],
          getdel: [],
          delname: ''
        },
        delbaseModal: false,
        delbasename: '',
        delconfirmbasename: '',
        pagenumber: 1,
        addDingding: false,
        dingdingbeforetext: '',
        dingdingaftertext: '',
        dingname: '',
        dingdingid: null,
        tmp_id: null,
        diclist: [],
        baseinfo: false,
        editbaseinfo: {}
      }
    },
    methods: {
      del () {
        this.modal = false
        this.formItem = {}
      },
      testlink () {
        axios.put(util.url + '/management_db/test', {
          'ip': this.formItem.ip,
          'user': this.formItem.username,
          'password': this.formItem.password,
          'port': this.formItem.port
        })
          .then(res => {
            util.notice(res.data)
          })
          .catch(error => {
            util.err_notice(error)
          })
      },
      add () {
        for (let i of this.rowdata) {
          if (i.connection_name === this.formItem.name) {
            util.err_notice('连接名称重复,请更改为其他!')
            return
          }
        }
        this.$refs['formValidate'].validate((valid) => {
          if (valid) {
            let data = {
              'connection_name': this.formItem.name,
              'ip': this.formItem.ip,
              'computer_room': this.formItem.add,
              'username': this.formItem.username,
              'password': this.formItem.password,
              'port': this.formItem.port
            }
            axios.post(util.url + '/management_db/', {
              'data': JSON.stringify(data)
            })
              .then(() => {
                util.notice('数据库信息添加成功,请对相应用户赋予该数据库访问权限!')
                this.$refs.totol.currentPage = 1
                this.mountdata()
              })
              .catch(error => {
                util.err_notice(error)
              })
            this.del()
          }
        })
      },
      edit_tab (index) {
        this.baseinfo = true
        this.editbaseinfo = this.rowdata[index]
      },
      // 删除数据库
      deldatabases (index) {
        this.delbaseModal = true
        this.delbasename = this.rowdata[index].connection_name
      },
      // 删除数据库字典
      Deletedic () {
        if (this.dictionary.delname.length === 0) {
          this.$Message.error({
            content: '请选择相应的数据库再删除!',
            duration: 5
          })
        } else {
          if (this.dictionary.getdel.length === 0) {
            this.$Message.error({
              content: '请选择相应的数据表再删除!',
              duration: 5
            })
          } else {
            this.$Loading.start()
            axios.put(`${util.url}/adminsql/deldic`, {
              'name': this.dictionary.delname,
              'basename': this.dictionary.getdel
            })
              .then(res => {
                util.notice(res.data)
                this.$Loading.finish()
                this.cleardata()
              })
              .catch(error => {
                util.err_notice(error)
                this.$Loading.error()
              })
          }
        }
      },
      // 生成数据库字典
      Commit () {
        if (this.dictionary.databases.length === 0) {
          this.$Message.error({
            content: '请选择相应的数据库再生成数据字典!',
            duration: 5
          })
        } else {
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
                h('div', '数据库字典正在生成中,请稍后........')
              ])
            }
          })
          axios.put(`${util.url}/adminsql/Generation`, {
            'id': this.tmp_id,
            'basename': JSON.stringify(this.dictionary.databases)
          })
            .then(res => {
              util.notice(res.data)
              this.$Spin.hide()
              this.cleardata()
            }).catch(error => {
            util.err_notice(error)
            this.$Spin.hide()
          })
        }
      },
      // 生成数据库全部库名
      BaseList (vl) {
        if (vl.length === 0) {
          return
        }
        this.tmp_id = vl
        axios.put(`${util.url}/workorder/basename`, {
          'id': vl
        })
          .then(res => {
            this.dictionary.databasesList = res.data
          })
          .catch(() => {
            util.err_notice('数据库信息获取失败,请检查网络状态.')
          })
      },
      // 全选
      dicCheckAll () {
        if (this.dictionary.indeterminate) {
          this.dictionary.checkAll = false
        } else {
          this.dictionary.checkAll = !this.dictionary.checkAll
        }
        this.dictionary.indeterminate = false

        if (this.dictionary.checkAll) {
          this.dictionary.databases = this.dictionary.databasesList
        } else {
          this.dictionary.databases = []
        }
      },
      // 生成已生成字典的库列表
      getdiclist (val) {
        if (val.length === 0) {
          return
        }
        axios.put(`${util.url}/sqldic/getdiclist`, {
          'name': val
        })
          .then(res => {
            this.dictionary.getdellist = res.data
          })
          .catch(error => {
            util.err_notice(error)
          })
      },
      // 重置
      cleardata () {
        this.dictionary.name = ''
        this.dictionary.databases = []
        this.dictionary.databasesList = []
        this.dictionary.getdellist = []
        this.dictionary.getdel = []
        this.dictionary.delname = ''
      },
      delbaselink () {
        if (this.delbasename === this.delconfirmbasename) {
          axios.delete(`${util.url}/management_db?del=${this.delbasename}`)
            .then(res => {
              util.notice(res.data)
              this.delbaseModal = false
              this.delconfirmbasename = ''
              this.$refs.totol.currentPage = 1
              this.mountdata()
            })
            .catch(error => {
              util.err_notice(error)
            })
        } else {
          this.$Message.error({
            content: '请确认输入的连接名称一致！'
          })
        }
      },
      mountdata (vl = 1) {
        axios.get(`${util.url}/management_db/all/?page=${vl}&permissions_type=base`)
          .then(res => {
            this.rowdata = res.data.data
            this.pagenumber = parseInt(res.data.page)
            this.diclist = res.data.diclist
            this.dataset = res.data['custom']
          })
          .catch(error => {
            util.err_notice(error)
          })
      },
      dingding (vl) {
        this.dingname = vl.connection_name
        axios.get(`${util.url}/dingding?connection_name=${this.dingname}`)
          .then(res => {
            this.dingdingid = res.data.id
            this.dingdingbeforetext = res.data.before
            this.dingdingaftertext = res.data.after
          })
          .catch(error => {
            util.err_notice(error)
          })
        this.addDingding = true
      },
      savedingding () {
        axios.post(`${util.url}/dingding/`, {
          'before': this.dingdingbeforetext,
          'after': this.dingdingaftertext,
          'id': this.dingdingid
        })
          .then(() => {
            util.notice('钉钉推送消息已设置成功!')
            this.addDingding = false
          })
          .catch(error => {
            util.err_notice(error)
          })
      },
      update_base () {
        axios.put(`${util.url}/management_db/update`, {
          'data': JSON.stringify(this.editbaseinfo)
        })
          .then(res => util.notice(res.data))
          .catch(err => util.err_notice(err))
      }
    },
    mounted () {
      this.mountdata()
    }
  }
</script>
