<style lang="less">
  @import '../../styles/common.less';
  @import '../order/components/table.less';
</style>
<template>
  <div>
    <Col span="5">
      <Card>
        <p slot="title">
          <Icon type="md-settings"></Icon>
          添加用户
        </p>
        <div class="edittable-testauto-con">
          <Form :model="userinfo" :label-width="80" ref="userinfova" :rules="userinfoValidate">
            <FormItem label="用户名" prop="username">
              <Input v-model="userinfo.username" placeholder="请输入"></Input>
            </FormItem>
            <FormItem label="密码" prop="password">
              <Input v-model="userinfo.password" placeholder="请输入" type="password"></Input>
            </FormItem>
            <FormItem label="确认密码" prop="confirmpassword">
              <Input v-model="userinfo.confirmpassword" placeholder="请输入" type="password"></Input>
            </FormItem>
            <FormItem label="部门" prop="department">
              <Input v-model="userinfo.department" placeholder="请输入"></Input>
            </FormItem>
            <FormItem label="姓名" prop="realname">
              <Input v-model="userinfo.realname" placeholder="请输入"></Input>
            </FormItem>
            <FormItem label="角色" prop="group">
              <Select v-model="userinfo.group" placeholder="请选择">
                <Option value="admin">管理员</Option>
                <Option value="perform" v-if="connectionList.multi">执行人</Option>
                <Option value="guest">使用人</Option>
              </Select>
            </FormItem>
            <FormItem label="电子邮箱" prop="email">
              <Input v-model="userinfo.email" placeholder="请输入"></Input>
            </FormItem>
            <FormItem label="权限组">
              <Select v-model="userinfo.authgroup" placeholder="请选择" multiple>
                <Option v-for="list in groupset" :value="list" :key="list">{{ list }}</Option>
              </Select>
            </FormItem>
            <Button type="primary" @click.native="registered" style="margin-left: 35%" :loading="loading">注册</Button>
          </Form>
        </div>
      </Card>
    </Col>
    <Col span="19" class="padding-left-10">
      <Card>
        <p slot="title">
          <Icon type="md-people"></Icon>
          系统用户表
        </p>
        <Input v-model="query.user" placeholder="请填写用户名" style="width: 20%" clearable></Input>
        <Input v-model="query.department" placeholder="请填写部门" style="width: 20%" clearable></Input>
        <Button @click="queryData" type="primary">查询</Button>
        <Button @click="queryCancel" type="warning">重置</Button>
        <div class="edittable-con-1">
          <Table border :columns="columns" :data="tableData" stripe height="520">
            <template slot-scope="{ row, index }" slot="action">
              <Button type="primary" size="small" style="margin-right: 5px" @click="editPassModal(row)"
                      v-if="row.id !== 1">更改密码
              </Button>
              <Button type="info" size="small" @click="editAuthModal(row)" style="margin-right: 5px">权限组</Button>
              <Button type="success" size="small" @click="editEmailModal(row)" style="margin-right: 5px">邮箱</Button>
              <Poptip
                confirm
                title="确定删除改用户吗？"
                transfer
                @on-ok="delUser(row)">
                <Button type="warning" size="small" v-if="row.id !== 1">删除</Button>
              </Poptip>
            </template>
          </Table>
        </div>
        <br>
        <Page :total="pagenumber" show-elevator @on-change="refreshUser" :page-size="10" ref="page"></Page>
      </Card>
    </Col>

    <Modal v-model="editPasswordForm.modal" :closable='false' :mask-closable=false :width="500">
      <h3 slot="header" style="color:#2D8CF0">修改用户密码</h3>
      <Form ref="editPasswordForm" :model="editPasswordForm" :label-width="100" label-position="right"
            :rules="passwordValidate">
        <FormItem label="用户名">
          <Input v-model="username" readonly="readonly"></Input>
        </FormItem>
        <FormItem label="新密码" prop="newPass">
          <Input v-model="editPasswordForm.newPass" placeholder="请输入新密码，至少6位字符" type="password"></Input>
        </FormItem>
        <FormItem label="确认新密码" prop="rePass">
          <Input v-model="editPasswordForm.rePass" placeholder="请再次输入新密码" type="password"></Input>
        </FormItem>
      </Form>
      <div slot="footer">
        <Button type="text" @click="cancelModal(editPasswordForm)">取消</Button>
        <Button type="primary" @click="saveEditPass" :loading="savePassLoading">保存</Button>
      </div>
    </Modal>

    <Modal v-model="editAuthForm.modal" :width="900">
      <h3 slot="header" style="color:#2D8CF0">权限组设置</h3>
      <Form :model="editAuthForm" :label-width="100" label-position="right">
        <FormItem label="用户名">
          <Input v-model="editAuthForm.username" readonly="readonly"></Input>
        </FormItem>
        <FormItem label="真实姓名">
          <Input v-model="editAuthForm.real_name" readonly="readonly"></Input>
        </FormItem>
        <FormItem label="角色">
          <Select v-model="editAuthForm.group" placeholder="请选择">
            <Option value="admin">管理员</Option>
            <Option value="perform" v-if="connectionList.multi && editAuthForm.id !== 1">执行人</Option>
            <Option value="guest" v-if="editAuthForm.id !== 1">使用者</Option>
          </Select>
        </FormItem>
        <FormItem label="部门">
          <Input v-model="editAuthForm.department" placeholder="请输入新部门"></Input>
        </FormItem>
        <FormItem label="权限组" prop="authgroup">
          <Select v-model="editAuthForm.authgroup" multiple @on-change="getGroupList" placeholder="请选择">
            <Option v-for="list in groupset" :value="list" :key="list">{{ list }}</Option>
          </Select>
          <template>
            <FormItem>
              <Divider orientation="left">DDL权限</Divider>
              <FormItem label="DDL是否可见:">
                <p>{{formItem.ddl}}</p>
              </FormItem>
              <FormItem label="可访问的连接名:" v-if="formItem.ddl === '是'">
                <Tag color="blue" v-for="i in formItem.ddlcon" :key="i">{{i}}</Tag>
              </FormItem>
              <Divider orientation="left">DML权限</Divider>
              <FormItem label="DML是否可见:">
                <p>{{formItem.dml}}</p>
              </FormItem>
              <FormItem label="可访问的连接名:" v-if="formItem.dml === '是'">
                <Tag color="blue" v-for="i in formItem.dmlcon" :key="i">{{i}}</Tag>
              </FormItem>
              <Divider orientation="left">查询权限</Divider>
              <FormItem label="查询是否可见:">
                <p>{{formItem.query}}</p>
              </FormItem>
              <FormItem label="可访问的连接名:" v-if="formItem.query === '是'">
                <Tag color="blue" v-for="i in formItem.querycon" :key="i">{{i}}</Tag>
              </FormItem>
              <Divider orientation="left">上级审核人</Divider>
              <FormItem>
                <Tag color="blue" v-for="i in formItem.person" :key="i">{{i}}</Tag>
              </FormItem>
              <Divider orientation="left">管理权限</Divider>
              <FormItem label="用户管理权限:">
                <p>{{formItem.user}}</p>
              </FormItem>
              <FormItem label="数据库管理权限:">
                <p>{{formItem.base}}</p>
              </FormItem>
            </FormItem>
          </template>
        </FormItem>
      </Form>
      <div slot="footer">
        <Button type="text" @click="editAuthForm.modal = false">取消</Button>
        <Button type="primary" :loading="savePassLoading" @click="saveAuthInfo">保存</Button>
      </div>
    </Modal>

    <Modal v-model="email.modal" :closable='false' :mask-closable=false :width="500">
      <h3 slot="header" style="color:#2D8CF0">email邮箱</h3>
      <Form :label-width="100" label-position="right">
        <FormItem label="E-mail">
          <Input v-model="email.addr"></Input>
        </FormItem>
        <FormItem label="真实姓名">
          <Input v-model="real_name"></Input>
        </FormItem>
      </Form>
      <div slot="footer">
        <Button type="text" @click="email.modal=false">取消</Button>
        <Button type="warning" @click="modifyEmail">更改</Button>
      </div>
    </Modal>
  </div>
</template>
<script>
  import axios from 'axios'
  import '../../assets/tablesmargintop.css'

  export default {
    data () {
      const valideRePassword = (rule, value, callback) => { // eslint-disable-line no-unused-vars
        if (value !== this.editPasswordForm.newPass) {
          callback(new Error('两次输入密码不一致'))
        } else {
          callback()
        }
      }
      const valideuserinfoPassword = (rule, value, callback) => {
        if (value !== this.userinfo.password) {
          callback(new Error('两次输入密码不一致'))
        } else {
          callback()
        }
      }
      return {
        query: {
          user: '',
          department: '',
          valve: false
        },
        loading: false,
        columns: [
          {
            title: '用户名',
            key: 'username',
            sortable: true
          },
          {
            title: '角色',
            key: 'group',
            sortable: true
          },
          {
            title: '姓名',
            key: 'real_name',
            sortable: true
          },
          {
            title: '部门',
            key: 'department',
            sortable: true
          },
          {
            title: 'email',
            key: 'email',
            sortable: true
          },
          {
            title: '操作',
            key: 'action',
            width: 400,
            align: 'center',
            slot: 'action'
          }
        ],
        tableData: [],
        pagenumber: 1,
        // 新建用户
        userinfo: {
          username: '',
          password: '',
          confirmpassword: '',
          group: '',
          checkbox: '',
          department: '',
          email: '',
          authgroup: [],
          realname: ''
        },
        groupset: [],
        userinfoValidate: {
          username: [{
            required: true,
            message: '请输入用户名',
            trigger: 'blur'
          }],
          password: [
            {
              required: true,
              message: '请输入密码',
              trigger: 'blur'
            },
            {
              min: 6,
              message: '请至少输入6个字符',
              trigger: 'blur'
            },
            {
              max: 32,
              message: '最多输入32个字符',
              trigger: 'blur'
            }
          ],
          confirmpassword: [
            {
              required: true,
              message: '请再次输入新密码',
              trigger: 'blur'
            },
            {
              validator: valideuserinfoPassword,
              trigger: 'blur'
            }
          ],
          group: [
            {
              required: true,
              message: '请输入权限',
              trigger: 'blur'
            }
          ],
          department: [
            {
              required: true,
              message: '请输入部门名称',
              trigger: 'blur'
            },
            {
              min: 2,
              message: '请至少输入6个字符',
              trigger: 'blur'
            },
            {
              max: 32,
              message: '最多输入32个字符',
              trigger: 'blur'
            }
          ],
          realname: [
            {
              required: true,
              message: '请输入姓名',
              trigger: 'blur'
            },
            {
              min: 2,
              message: '请至少输入2个字符',
              trigger: 'blur'
            },
            {
              max: 32,
              message: '最多输入32个字符',
              trigger: 'blur'
            }],
          email: [
            {
              required: true,
              message: '请输入工作邮箱',
              trigger: 'blur'
            },
            {
              min: 2,
              message: '请至少输入2个字符',
              trigger: 'blur'
            },
            {
              max: 32,
              message: '最多输入32个字符',
              trigger: 'blur'
            }]
        },
        // 更改密码遮罩层状态
        editPasswordModal: false,
        // 更改密码
        editPasswordForm: {
          newPass: '',
          rePass: '',
          modal: false
        },
        // 保存更改密码loding按钮状态
        savePassLoading: false,
        // 更改密码表单验证规则
        passwordValidate: {
          newPass: [{
            required: true,
            message: '请输入新密码',
            trigger: 'blur'
          },
            {
              min: 6,
              message: '请至少输入6个字符',
              trigger: 'blur'
            },
            {
              max: 32,
              message: '最多输入32个字符',
              trigger: 'blur'
            }
          ],
          rePass: [{
            required: true,
            message: '请再次输入新密码',
            trigger: 'blur'
          },
            {
              validator: valideRePassword,
              trigger: 'blur'
            }
          ]
        },
        // 更改部门及权限
        GroupModal: false,
        editAuthForm: {
          username: '',
          group: '',
          department: '',
          authgroup: [],
          real_name: '',
          modal: false
        },
        formItem: {
          ddl: '',
          ddlcon: [],
          dml: '',
          dmlcon: [],
          query: '',
          querycon: [],
          user: '',
          base: '',
          person: ''
        },
        // 更改部门及权限遮罩层状态
        email: {
          modal: false,
          addr: ''
        },
        real_name: '',
        // 用户名
        username: '',
        connectionList: {
          multi: Boolean
        },
        permission_list: {}
      }
    },
    methods: {
      getGroupList () {
        axios.put(`${this.$config.url}/authgroup/group_list`, {'group_list': JSON.stringify(this.editAuthForm.authgroup)})
          .then(res => {
            this.permission_list = res.data.permissions
            this.formItem = this.$config.mode(res.data.permissions)
          })
          .catch(error => {
            this.$config.err_notice(this, error)
          })
      },
      getAuthGroup () {
        axios.get(`${this.$config.url}/authgroup/group_name`)
          .then(res => {
            this.groupset = res.data.authgroup
          })
          .catch(error => {
            this.$config.err_notice(this, error)
          })
      },
      editPassModal (row) {
        this.editPasswordForm.modal = true
        this.username = row.username
      },
      editAuthModal (row) {
        this.editAuthForm = this.$config.sameMerge(this.editAuthForm, row, this.editAuthForm)
        this.editAuthForm.modal = true
        if (row.auth_group !== null) {
          this.editAuthForm.authgroup = row.auth_group.split(',')
        } else {
          this.editAuthForm.authgroup = []
        }
      },
      editEmailModal (row) {
        this.email.modal = true
        this.username = row.username
        this.email.addr = row.email
        this.real_name = row.real_name
      },
      cancelModal (vl) {
        this.$config.clearObj(vl)
      },
      saveEditPass () {
        this.$refs['editPasswordForm'].validate((valid) => {
          if (valid) {
            this.savePassLoading = true
            axios.put(this.$config.url + '/userinfo/changepwd', {
              'username': this.username,
              'new': this.editPasswordForm.newPass
            })
              .then(res => {
                this.$config.notice(res.data)
                this.editPasswordForm.modal = false
              })
              .catch(error => {
                this.$config.err_notice(this, error)
              })
            this.savePassLoading = false
          }
        })
      },
      saveAuthInfo () {
        this.savePassLoading = true
        axios.put(`${this.$config.url}/authgroup/save_info`, {
          'username': this.editAuthForm.username,
          'group': this.editAuthForm.group,
          'department': this.editAuthForm.department,
          'auth_group': this.editAuthForm.authgroup,
          'permission_list': JSON.stringify(this.permission_list)
        })
          .then(res => {
            this.$config.notice(res.data)
            this.$config.clearObj(this.editAuthForm)
            this.refreshUser(this.$refs.page.currentPage)
          })
          .catch(error => {
            this.$config.err_notice(this, error)
          })
        this.savePassLoading = false
      },
      modifyEmail () {
        axios.put(`${this.$config.url}/userinfo/changemail`, {
          'username': this.username,
          'mail': this.email.addr,
          'real': this.real_name
        })
          .then(res => {
            this.$config.notice(res.data)
            this.email.modal = false
            this.refreshUser(this.$refs.page.currentPage)
            sessionStorage.setItem('real_name', this.real_name)
          })
          .catch(error => {
            this.$config.err_notice(this, error)
          })
      },
      registered () {
        this.$refs['userinfova'].validate((valid) => {
          if (valid) {
            this.loading = true
            axios.post(this.$config.url + '/userinfo/', {
              'username': this.userinfo.username,
              'password': this.userinfo.password,
              'group': this.userinfo.group,
              'department': this.userinfo.department,
              'email': this.userinfo.email,
              'auth_group': JSON.stringify(this.userinfo.authgroup),
              'realname': this.userinfo.realname
            })
              .then(res => {
                this.loading = false
                this.$config.notice(res.data)
                this.refreshUser(this.$refs.page.currentPage)
                this.userinfo = this.$config.clearObj(this.userinfo)
              })
              .catch(error => {
                this.loading = false
                this.$config.err_notice(this, error)
              })
          }
        })
      },
      refreshUser (vl = 1) {
        axios.get(`${this.$config.url}/userinfo/all?page=${vl}&username=${this.query.user}&department=${this.query.department}&valve=${this.query.valve}`)
          .then(res => {
            this.tableData = res.data.data
            this.pagenumber = parseInt(res.data.page)
          })
          .catch(error => {
            this.$config.err_notice(this, error)
          })
      },
      delUser (row) {
        let step = this.$refs.page.currentPage
        if (this.tableData.length === 1) {
          step = step - 1
        }
        axios.delete(`${this.$config.url}/userinfo/${row.username}`)
          .then(res => {
            this.$config.notice(res.data)
            this.refreshUser(step)
          })
          .catch(error => {
            this.$config.err_notice(this, error)
          })
      },
      queryData () {
        this.query.valve = true
        this.refreshUser()
      },
      queryCancel () {
        this.$config.clearObj(this.query)
        this.refreshUser()
      }
    },
    mounted () {
      this.refreshUser()
      this.getAuthGroup()
      axios.put(`${this.$config.url}/workorder/connection`, {'permissions_type': 'user'})
        .then(res => {
          this.connectionList.multi = res.data['multi']
        })
        .catch(error => {
          this.$config.err_notice(this, error)
        })
    }
  }
</script>
