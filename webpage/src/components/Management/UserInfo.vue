<style lang="less">
  @import '../../styles/common.less';
  @import '../Order/components/table.less';
</style>
<template>
  <div>
    <Col span="6">
      <Card>
        <p slot="title">
          <Icon type="load-b"></Icon>
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
            <FormItem label="角色" prop="group">
              <Select v-model="userinfo.group" placeholder="请选择">
                <Option value="admin">管理员</Option>
                <Option value="perform" v-if="connectionList.multi">执行人</Option>
                <Option value="guest">使用人</Option>
              </Select>
            </FormItem>
            <FormItem label="权限组" prop="authgroup">
              <Select v-model="userinfo.authgroup" placeholder="请选择">
                <Option v-for="list in groupset" :value="list" :key="list">{{ list }}</Option>
              </Select>
            </FormItem>
            <FormItem label="电子邮箱">
              <Input v-model="userinfo.email" placeholder="请输入"></Input>
            </FormItem>
            <Button type="primary" @click.native="Registered" style="margin-left: 35%">注册</Button>
          </Form>
        </div>
      </Card>
    </Col>
    <Col span="18" class="padding-left-10">
      <Card>
        <p slot="title">
          <Icon type="ios-crop-strong"></Icon>
          系统用户表
        </p>
        <div class="edittable-con-1">
          <Table border :columns="columns6" :data="data5" stripe height="550"></Table>
        </div>
        <br>
        <Page :total="pagenumber" show-elevator @on-change="splicpage" :page-size="10" ref="total"></Page>
      </Card>
    </Col>

    <Modal v-model="editPasswordModal" :closable='false' :mask-closable=false :width="500">
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
        <Button type="text" @click="cancelEditPass">取消</Button>
        <Button type="primary" @click="saveEditPass" :loading="savePassLoading">保存</Button>
      </div>
    </Modal>

    <Modal v-model="editAuthModal" :width="900">
      <h3 slot="header" style="color:#2D8CF0">权限组设置</h3>
      <Form :model="editAuthForm" :label-width="100" label-position="right">
        <FormItem label="用户名">
          <Input v-model="editAuthForm.username" readonly="readonly"></Input>
        </FormItem>
        <FormItem label="角色">
          <Select v-model="editAuthForm.group" placeholder="请选择">
            <Option value="admin">管理员</Option>
            <Option value="perform" v-if="connectionList.multi && this.userid !== 1">执行人</Option>
            <Option value="guest" v-if="this.userid !== 1">使用者</Option>
          </Select>
        </FormItem>
        <FormItem label="部门">
          <Input v-model="editAuthForm.department" placeholder="请输入新部门"></Input>
        </FormItem>
        <FormItem label="权限组" prop="authgroup">
          <Select v-model="editAuthForm.authgroup" multiple @on-change="getgrouplist" placeholder="请选择">
            <Option v-for="list in groupset" :value="list" :key="list">{{ list }}</Option>
          </Select>
          <template>
            <FormItem label="所拥有的权限:">
              <br>
              <FormItem label="DDL是否可见:">
                <p>{{formItem.ddl}}</p>
              </FormItem>
              <FormItem label="可访问的连接名:" v-if="formItem.ddl === '是'">
                <p>{{formItem.ddlcon}}</p>
              </FormItem>
              <FormItem label="DML是否可见:">
                <p>{{formItem.dml}}</p>
              </FormItem>
              <FormItem label="可访问的连接名:" v-if="formItem.dml === '是'">
                <p>{{formItem.dmlcon}}</p>
              </FormItem>
              <FormItem label="查询是否可见:">
                <p>{{formItem.query}}</p>
              </FormItem>
              <FormItem label="可访问的连接名:" v-if="formItem.query === '是'">
                <p>{{formItem.querycon}}</p>
              </FormItem>
              <FormItem label="字典是否可见:">
                <p>{{formItem.dic}}</p>
              </FormItem>
              <FormItem label="上级审核人:">
                <p>{{formItem.person}}</p>
              </FormItem>
              <FormItem label="可访问的连接名:" v-if="formItem.dic === '是'">
                <p>{{formItem.diccon}}</p>
              </FormItem>
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
        <Button type="text" @click="cancelAuthModal">取消</Button>
        <Button type="primary" :loading="savePassLoading" @click="saveAuthInfo">保存</Button>
      </div>
    </Modal>

    <Modal v-model="deluserModal" :closable='false' :mask-closable=false :width="500">
      <h3 slot="header" style="color:#2D8CF0">删除用户</h3>
      <Form :label-width="100" label-position="right">
        <FormItem label="用户名">
          <Input v-model="username" readonly="readonly"></Input>
        </FormItem>
        <FormItem label="请输入用户名">
          <Input v-model="confirmuser" placeholder="请确认用户名"></Input>
        </FormItem>
      </Form>
      <div slot="footer">
        <Button type="text" @click="cancelDelInfo">取消</Button>
        <Button type="warning" @click="delUser">删除</Button>
      </div>
    </Modal>

    <Modal v-model="editemail" :closable='false' :mask-closable=false :width="500">
      <h3 slot="header" style="color:#2D8CF0">更改email邮箱</h3>
      <Form :label-width="100" label-position="right">
        <FormItem label="E-mail">
          <Input v-model="email"></Input>
        </FormItem>
      </Form>
      <div slot="footer">
        <Button type="text" @click="editemail=false">取消</Button>
        <Button type="warning" @click="putemail">更改</Button>
      </div>
    </Modal>
  </div>
</template>
<script>
  import axios from 'axios'
  import '../../assets/tablesmargintop.css'
  import util from '../../libs/util'

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
        percent: 0,
        permission: {
          ddl: '0',
          ddlcon: [],
          dml: '0',
          dmlcon: [],
          query: '0',
          dic: '0',
          diccon: [],
          dicedit: '0',
          dicexport: '0',
          index: '0',
          indexcon: [],
          user: '0',
          base: '0'
        },
        con: [],
        columns6: [
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
            title: '权限组',
            key: 'auth_group',
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
            width: 300,
            align: 'center',
            render: (h, params) => {
              if (params.row.id !== 1) {
                return h('div', [
                  h('Button', {
                    props: {
                      type: 'primary',
                      size: 'small'
                    },
                    style: {
                      marginRight: '5px'
                    },
                    on: {
                      click: () => {
                        this.edituser(params.index)
                      }
                    }
                  }, '更改密码'),
                  h('Button', {
                    props: {
                      type: 'info',
                      size: 'small'
                    },
                    style: {
                      marginRight: '5px'
                    },
                    on: {
                      click: () => {
                        this.editauth(params.index)
                      }
                    }
                  }, '权限组'),
                  h('Button', {
                    props: {
                      type: 'success',
                      size: 'small'
                    },
                    style: {
                      marginRight: '5px'
                    },
                    on: {
                      click: () => {
                        this.editEmail(params.index)
                      }
                    }
                  }, 'email更改'),
                  h('Button', {
                    props: {
                      type: 'warning',
                      size: 'small'
                    },
                    on: {
                      click: () => {
                        this.deleteUser(params.index)
                      }
                    }
                  }, '删除')
                ])
              } else {
                return h('div', [
                  h('Button', {
                    props: {
                      type: 'info',
                      size: 'small'
                    },
                    style: {
                      marginRight: '5px'
                    },
                    on: {
                      click: () => {
                        this.editauth(params.index)
                      }
                    }
                  }, '权限组'),
                  h('Button', {
                    props: {
                      type: 'success',
                      size: 'small'
                    },
                    style: {
                      marginRight: '5px'
                    },
                    on: {
                      click: () => {
                        this.editEmail(params.index)
                      }
                    }
                  }, 'email更改')
                ])
              }
            }
          }
        ],
        data5: [],
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
          authgroup: ''
        },
        groupset: [],
        userinfoValidate: {
          username: [{
            required: true,
            message: '请输入用户名',
            trigger: 'blur'
          }],
          password: [{
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
          confirmpassword: [{
            required: true,
            message: '请再次输入新密码',
            trigger: 'blur'
          },
            {
              validator: valideuserinfoPassword,
              trigger: 'blur'
            }
          ],
          group: [{
            required: true,
            message: '请输入权限',
            trigger: 'blur'
          }],
          department: [{
            required: true,
            message: '请输入部门名称',
            trigger: 'blur'
          }],
          authgroup: [{
            required: true,
            message: '请选择权限组',
            trigger: 'blur'
          }]
        },
        // 更改密码遮罩层状态
        editPasswordModal: false,
        // 更改密码
        editPasswordForm: {
          newPass: '',
          rePass: ''
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
        editAuthForm: {
          username: '',
          group: '',
          department: '',
          authgroup: []
        },
        formItem: {
          ddl: '',
          ddlcon: '',
          dml: '',
          dmlcon: '',
          dic: '',
          diccon: '',
          query: '',
          querycon: '',
          user: '',
          base: '',
          person: ''
        },
        // 更改部门及权限遮罩层状态
        editAuthModal: false,
        editemail: false,
        email: '',
        // 用户名
        username: '',
        confirmuser: '',
        deluserModal: false,
        userid: null,
        dicadd: [],
        checkAll: {
          ddl: false,
          dml: false,
          query: false,
          dic: false,
          person: false
        },
        indeterminate: {
          ddl: true,
          dml: true,
          query: true,
          dic: true,
          person: true
        },
        connectionList: {
          connection: [],
          dic: [],
          person: [],
          multi: Boolean
        },
        permission_list: {}
      }
    },
    methods: {
      getgrouplist () {
        axios.put(`${util.url}/authgroup/group_list`, {'group_list': JSON.stringify(this.editAuthForm.authgroup)})
          .then(res => {
            this.permission_list = res.data.permissions
            this.formItem = util.mode(res.data.permissions)
          })
          .catch(error => {
            util.err_notice(error)
          })
      },
      cancelAuthModal () {
        this.editAuthModal = false
        this.editAuthForm.authgroup = []
      },
      getauthgroup () {
        axios.get(`${util.url}/authgroup/group_name`)
          .then(res => {
            this.groupset = res.data.authgroup
          })
          .catch(error => {
            util.err_notice(error)
          })
      },
      edituser (index) {
        this.editPasswordModal = true
        this.username = this.data5[index].username
      },
      editauth (index) {
        this.editAuthModal = true
        this.userid = this.data5[index].id
        this.editAuthForm.username = this.data5[index].username
        this.editAuthForm.department = this.data5[index].department
        this.editAuthForm.group = this.data5[index].group
        if (this.data5[index].auth_group !== null) {
          this.editAuthForm.authgroup = this.data5[index].auth_group.split(',')
        } else {
          this.editAuthForm.authgroup = []
        }
      },
      deleteUser (index) {
        this.deluserModal = true
        this.username = this.data5[index].username
      },
      editEmail (index) {
        this.editemail = true
        this.username = this.data5[index].username
        this.email = this.data5[index].email
      },
      putemail () {
        axios.put(`${util.url}/userinfo/changemail`, {
          'username': this.username,
          'mail': this.email
        })
          .then(res => {
            util.notice(res.data)
            this.editemail = false
            this.$refs.total.currentPage = 1
            this.refreshuser()
          })
          .catch(error => {
            util.err_notice(error)
          })
      },
      Registered () {
        this.$refs['userinfova'].validate((valid) => {
          if (valid) {
            axios.post(util.url + '/userinfo/', {
              'username': this.userinfo.username,
              'password': this.userinfo.password,
              'group': this.userinfo.group,
              'department': this.userinfo.department,
              'email': this.userinfo.email,
              'auth_group': this.userinfo.authgroup
            })
              .then(res => {
                util.notice(res.data)
                this.refreshuser()
                this.userinfo = {
                  username: '',
                  password: '',
                  confirmpassword: '',
                  group: '',
                  checkbox: '',
                  department: '',
                  email: '',
                  auth_group: ''
                }
              })
              .catch(error => {
                util.err_notice(error)
              })
          }
        })
      },
      refreshuser (vl = 1) {
        axios.get(`${util.url}/userinfo/all?page=${vl}`)
          .then(res => {
            this.data5 = res.data.data
            this.pagenumber = parseInt(res.data.page)
          })
          .catch(error => {
            util.err_notice(error)
          })
      },
      splicpage (page) {
        this.refreshuser(page)
      },
      cancelEditPass () {
        this.editPasswordForm = {}
        this.editPasswordModal = false
      },
      cancelDelInfo () {
        this.deluserModal = false
        this.confirmuser = ''
      },
      saveEditPass () {
        this.$refs['editPasswordForm'].validate((valid) => {
          if (valid) {
            this.savePassLoading = true
            axios.put(util.url + '/userinfo/changepwd', {
              'username': this.username,
              'new': this.editPasswordForm.newPass
            })
              .then(res => {
                util.notice(res.data)
                this.editPasswordModal = false
              })
              .catch(error => {
                util.err_notice(error)
              })
            this.savePassLoading = false
          }
        })
      },
      saveAuthInfo () {
        this.savePassLoading = true
        axios.put(`${util.url}/authgroup/save_info`, {
          'username': this.editAuthForm.username,
          'group': this.editAuthForm.group,
          'department': this.editAuthForm.department,
          'auth_group': this.editAuthForm.authgroup,
          'permission_list': JSON.stringify(this.permission_list)
        })
          .then(res => {
            util.notice(res.data)
            this.editAuthModal = false
            this.editAuthForm.authgroup = []
            this.refreshuser()
          })
          .catch(error => {
            util.err_notice(error)
          })
        this.savePassLoading = false
      },
      delUser () {
        if (this.username === this.confirmuser) {
          axios.delete(util.url + '/userinfo/' + this.username)
            .then(res => {
              util.notice(res.data)
              this.deluserModal = false
              this.$refs.total.currentPage = 1
              this.refreshuser()
            })
            .catch(error => {
              util.err_notice(error)
            })
        } else {
          this.$Message.error('用户名不一致!请重新操作!')
        }
      },
      ddlCheckAll (name, indeterminate, ty) {
        if (this.indeterminate[indeterminate]) {
          this.checkAll[indeterminate] = false
        } else {
          this.checkAll[indeterminate] = !this.checkAll[indeterminate]
        }
        this.indeterminate[indeterminate] = false
        if (this.checkAll[indeterminate]) {
          if (ty === 'dic') {
            this.permission[name] = this.connectionList[ty].map(vl => vl.Name)
          } else if (ty === 'person') {
            this.permission[name] = this.connectionList[ty].map(vl => vl.username)
          } else {
            this.permission[name] = this.connectionList[ty].map(vl => vl.connection_name)
          }
        } else {
          this.permission[name] = []
        }
      }
    },
    mounted () {
      this.getauthgroup()
      axios.put(`${util.url}/workorder/connection`, {'permissions_type': 'user'})
        .then(res => {
          this.connectionList.connection = res.data['connection']
          this.connectionList.dic = res.data['dic']
          this.connectionList.person = res.data['person']
          this.connectionList.multi = res.data['multi']
        })
        .catch(error => {
          util.err_notice(error)
        })
      this.refreshuser()
    }
  }
</script>
