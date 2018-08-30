<style lang="less">
  @import './own-space.less';
</style>

<template>
  <div>
    <Card>
      <p slot="title">
        <Icon type="person"></Icon>
        个人信息
      </p>
      <div>
        <Form ref="userForm" :model="userForm" :label-width="100" label-position="right">
          <FormItem label="用户名：" prop="name">
            <div style="display:inline-block;width:300px;">
              <span>{{ userForm.username }}</span>
            </div>
          </FormItem>
          <FormItem label="部门：">
            <span>{{ userForm.department }}</span>
          </FormItem>
          <FormItem label="角色：">
            <span>{{ userForm.group }}</span>
          </FormItem>
          <FormItem label="权限组：">
            <span>{{ userForm.auth_group }}</span>
          </FormItem>
          <FormItem label="邮箱：">
            <span>{{ userForm.email }}</span>
          </FormItem>
          <FormItem label="具体权限：">
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
          <FormItem label="编辑：">
            <Button type="warning" size="small" @click="editPasswordModal=true">修改密码</Button>
            <Button type="primary" size="small" @click="editEmailModal=true">修改邮箱</Button>
            <Button type="success" size="small" @click="ApplyForPermission">权限申请</Button>
          </FormItem>
        </Form>
      </div>
    </Card>
    <Modal v-model="editPasswordModal" :closable='false' :mask-closable=false :width="500">
      <h3 slot="header" style="color:#2D8CF0">修改密码</h3>
      <Form ref="editPasswordForm" :model="editPasswordForm" :label-width="100" label-position="right"
            :rules="passwordValidate">
        <FormItem label="原密码" prop="oldPass" :error="oldPassError">
          <Input v-model="editPasswordForm.oldPass" placeholder="请输入现在使用的密码" type="password"></Input>
        </FormItem>
        <FormItem label="新密码" prop="newPass">
          <Input v-model="editPasswordForm.newPass" placeholder="请输入新密码，至少6位字符" type="password"></Input>
        </FormItem>
        <FormItem label="确认新密码" prop="rePass">
          <Input v-model="editPasswordForm.rePass" placeholder="请再次输入新密码" type="password"></Input>
        </FormItem>
      </Form>
      <div slot="footer">
        <Button type="text" @click="editPasswordModal=false">取消</Button>
        <Button type="primary" :loading="savePassLoading" @click="saveEditPass">保存</Button>
      </div>
    </Modal>
    <Modal v-model="editEmailModal" :closable='false' :mask-closable=false :width="500">
      <h3 slot="header" style="color:#2D8CF0">邮箱修改</h3>
      <Form :label-width="100" label-position="right">
        <FormItem label="邮箱地址">
          <Input v-model="editEmailForm.mail"></Input>
        </FormItem>
      </Form>
      <div slot="footer">
        <Button type="text" @click="editEmailModal=false">取消</Button>
        <Button type="primary" :loading="savePassLoading" @click="saveEmail">保存</Button>
      </div>
    </Modal>

    <Modal v-model="editInfoModal" :width="1000">
      <h3 slot="header" style="color:#2D8CF0">权限申请单</h3>
      <Form :model="editAuthForm" :label-width="120" label-position="right">
        <FormItem label="权限组" prop="authgroup">
          <Select v-model="editAuthForm.authgroup" multiple @on-change="getgrouplist"  placeholder="请选择">
            <Option v-for="list in groupset" :value="list" :key="list">{{ list }}</Option>
          </Select>
          <template>
            <FormItem label="所拥有的权限:">
              <br>
              <FormItem label="DDL是否可见:">
                <p>{{permission.ddl}}</p>
              </FormItem>
              <FormItem label="可访问的连接名:" v-if="permission.ddl === '是'">
                <p>{{permission.ddlcon}}</p>
              </FormItem>
              <FormItem label="DML是否可见:">
                <p>{{permission.dml}}</p>
              </FormItem>
              <FormItem label="可访问的连接名:" v-if="permission.dml === '是'">
                <p>{{permission.dmlcon}}</p>
              </FormItem>
              <FormItem label="查询是否可见:">
                <p>{{permission.query}}</p>
              </FormItem>
              <FormItem label="可访问的连接名:" v-if="permission.query === '是'">
                <p>{{permission.querycon}}</p>
              </FormItem>
              <FormItem label="字典是否可见:">
                <p>{{permission.dic}}</p>
              </FormItem>
              <FormItem label="上级审核人:">
                <p>{{permission.person}}</p>
              </FormItem>
              <FormItem label="可访问的连接名:" v-if="permission.dic === '是'">
                <p>{{permission.diccon}}</p>
              </FormItem>
              <FormItem label="用户管理权限:">
                <p>{{permission.user}}</p>
              </FormItem>
              <FormItem label="数据库管理权限:">
                <p>{{permission.base}}</p>
              </FormItem>
            </FormItem>
          </template>
        </FormItem>
      </Form>
      <div slot="footer">
        <Button type="text" @click="editInfoModal=false">取消</Button>
        <Button type="primary"  :loading="savePassLoading"  @click="PutPermissionData">提交</Button>
      </div>
    </Modal>
  </div>
</template>

<script>
  //
  import util from '../../libs/util'
  import axios from 'axios'
  export default {
    name: 'own-space',
    data () {
      const valideRePassword = (rule, value, callback) => {
        if (value !== this.editPasswordForm.newPass) {
          callback(new Error('两次输入密码不一致'))
        } else {
          callback()
        }
      }
      return {
        editAuthForm: {
          authgroup: []
        },
        groupset: [],
        editEmailModal: false,
        editEmailForm: {
          mail: ''
        },
        userForm: {},
        formItem: {
          ddl: '',
          ddlcon: ''
        },
        uid: '', // 登录用户的userId
        save_loading: false,
        editPasswordModal: false, // 修改密码模态框显示
        savePassLoading: false,
        oldPassError: '',
        checkIdentifyCodeLoading: false,
        editPasswordForm: {
          oldPass: '',
          newPass: '',
          rePass: ''
        },
        passwordValidate: {
          oldPass: [{
            required: true,
            message: '请输入原密码',
            trigger: 'blur'
          }],
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
        editInfoModal: false,
        permission: {
          ddl: '0',
          ddlcon: [],
          dml: '0',
          dmlcon: [],
          query: '0',
          querycon: [],
          dic: '0',
          diccon: [],
          dicedit: '0',
          dicexport: '0',
          index: '0',
          indexcon: [],
          user: '0',
          base: '0'
        },
        indeterminate: {
          ddl: true,
          dml: true,
          query: true,
          dic: true,
          person: true
        },
        checkAll: {
          ddl: false,
          dml: false,
          query: false,
          dic: false,
          person: false
        },
        connectionList: {
          connection: [],
          dic: [],
          person: []
        },
        permission_list: {}
      }
    },
    methods: {
      getgrouplist () {
        axios.put(`${util.url}/authgroup/group_list`, {'group_list': JSON.stringify(this.editAuthForm.authgroup)})
          .then(res => {
            this.permission_list = res.data.permissions
            this.permission = util.mode(res.data.permissions)
          })
          .catch(error => {
            util.err_notice(error)
          })
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
      saveEditPass () {
        this.$refs['editPasswordForm'].validate((valid) => {
          if (valid) {
            this.savePassLoading = true
            axios.post(`${util.url}/otheruser/changepwd`, {
              'username': sessionStorage.getItem('user'),
              'new': this.editPasswordForm.newPass,
              'old': this.editPasswordForm.oldPass
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
        for (let i in this.editPasswordForm) {
          this.editPasswordForm[i] = ''
        }
      },
      saveEmail () {
        this.savePassLoading = true
        axios.put(`${util.url}/otheruser/mail`, {'mail': this.editEmailForm.mail})
          .then(res => {
            util.notice(res.data)
            this.editEmailModal = false
          })
          .catch(error => {
            util.err_notice(error)
          })
        this.savePassLoading = false
      },
      init () {
        axios.put(`${util.url}/homedata/ownspace`)
          .then(res => {
            this.userForm = res.data.userinfo
            this.formItem = util.mode(res.data.permissons)
          })
      },
      ApplyForPermission () {
        this.editInfoModal = true
        this.editAuthForm.authgroup = this.userForm.auth_group.split(',')
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
      },
      PutPermissionData () {
        this.savePassLoading = true
        axios.post(`${util.url}/apply_grained/`, {
          'auth_group': this.editAuthForm.authgroup,
          'grained_list': JSON.stringify(this.permission_list)
        })
          .then(res => {
            util.notice(res.data)
            this.editInfoModal = false
          })
          .catch(error => {
            util.err_notice(error)
          })
        this.savePassLoading = false
      }
    },
    mounted () {
      this.init();
      this.getauthgroup();
      axios.put(`${util.url}/workorder/connection`, {'permissions_type': 'own_space'})
        .then(res => {
          this.connectionList.connection = res.data['connection']
          this.connectionList.dic = res.data['dic']
          this.connectionList.person = res.data['person']
        })
        .catch(error => {
          util.err_notice(error)
        })
    }
  }
</script>
