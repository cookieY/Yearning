<style lang="less">
  @import './own-space.less';
</style>

<template>
  <div>
    <div>
      <Form ref="userForm" :label-width="100" label-position="right">
        <FormItem label="用户名：" prop="name">
          <div style="display:inline-block;width:300px;">
            <span>{{ userForm.username }}</span>
          </div>
        </FormItem>
        <FormItem label="姓名：" prop="name">
          <div style="display:inline-block;width:300px;">
            <span>{{ userForm.real_name }}</span>
          </div>
        </FormItem>
        <FormItem label="部门：">
          <span>{{ userForm.department }}</span>
        </FormItem>
        <FormItem label="角色：">
          <span>{{ userForm.group }}</span>
        </FormItem>
        <FormItem label="权限组：">
          <Tag color="blue" v-for="i in authgroup" :key="i">{{i}}</Tag>
        </FormItem>
        <FormItem label="邮箱：">
          <span>{{ userForm.email }}</span>
        </FormItem>
          <Button type="warning" size="small" @click="editPasswordModal=true">修改密码</Button>
          <Button type="primary" size="small" @click="openMailChange">修改邮箱/真实姓名</Button>
          <Button type="success" size="small" @click="openPerChange">查看/申请权限</Button>
      </Form>
    </div>

    <Modal v-model="editPasswordModal" :closable='false' :mask-closable=false :width="500">
      <h3 slot="header" style="color:#2D8CF0">修改密码</h3>
      <Form ref="editPasswordForm" :model="editPasswordForm" :label-width="100" label-position="right"
            :rules="passwordValidate">
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
      <h3 slot="header" style="color:#2D8CF0">邮箱/真实姓名修改</h3>
      <Form :label-width="100" label-position="right">
        <FormItem label="邮箱地址">
          <Input v-model="editEmailForm.email"></Input>
        </FormItem>
        <FormItem label="真实姓名">
          <Input v-model="editEmailForm.real_name"></Input>
        </FormItem>
      </Form>
      <div slot="footer">
        <Button type="text" @click="editEmailModal=false">取消</Button>
        <Button type="primary" :loading="savePassLoading" @click="saveEmail">保存</Button>
      </div>
    </Modal>

    <Modal v-model="editInfoModal" :width="1000">
      <h3 slot="header" style="color:#2D8CF0">权限申请单</h3>
      <Form :label-width="120" label-position="right">
        <FormItem label="权限组" prop="authgroup">
          <Select v-model="applygroup" multiple @on-change="getgrouplist" placeholder="请选择">
            <Option v-for="list in groupset" :value="list" :key="list">{{ list }}</Option>
          </Select>
          <template>
            <FormItem>
              <Divider orientation="left">DDL权限</Divider>
              <FormItem label="DDL是否可见:">
                <p>{{permission.ddl}}</p>
              </FormItem>
              <FormItem label="可访问的连接名:" v-if="permission.ddl === '是'">
                <Tag color="blue" v-for="i in permission.ddlcon" :key="i">{{i}}</Tag>
              </FormItem>
              <Divider orientation="left">DML权限</Divider>
              <FormItem label="DML是否可见:">
                <p>{{permission.dml}}</p>
              </FormItem>
              <FormItem label="可访问的连接名:" v-if="permission.dml === '是'">
                <Tag color="blue" v-for="i in permission.dmlcon" :key="i">{{i}}</Tag>
              </FormItem>
              <Divider orientation="left">查询权限</Divider>
              <FormItem label="查询是否可见:">
                <p>{{permission.query}}</p>
              </FormItem>
              <FormItem label="可访问的连接名:" v-if="permission.query === '是'">
                <Tag color="blue" v-for="i in permission.querycon" :key="i">{{i}}</Tag>
              </FormItem>
              <Divider orientation="left">上级审核人</Divider>
              <FormItem>
                <Tag color="blue" v-for="i in permission.person" :key="i">{{i}}</Tag>
              </FormItem>
              <Divider orientation="left">管理权限</Divider>
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
        <Button type="primary" :loading="savePassLoading" @click="putPermissionData">提交</Button>
      </div>
    </Modal>
  </div>
</template>

<script>
  //

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
        groupset: Array,
        editEmailModal: false,
        editEmailForm: {
          mail: '',
          real_name: ''
        },
        userForm: Object,
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
          newPass: '',
          rePass: ''
        },
        passwordValidate: {
          oldPass: [
            {
              required: true,
              message: '请输入原密码',
              trigger: 'blur'
            }
          ],
          newPass: [
            {
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
          ddlcon: Array,
          dml: '0',
          dmlcon: Array,
          query: '0',
          querycon: Array,
          index: '0',
          indexcon: Array,
          user: '0',
          base: '0'
        },
        permission_list: Object,
        authgroup: [],
        applygroup: []
      }
    },
    methods: {
      openMailChange () {
        this.editEmailModal = true
        this.editEmailForm = this.userForm
      },
      openPerChange () {
        this.editInfoModal = true
        this.applygroup = this.authgroup
      },
      getgrouplist () {
        axios.put(`${this.$config.url}/authgroup/group_list`, {'group_list': JSON.stringify(this.applygroup)})
          .then(res => {
            this.permission_list = res.data.permissions
            this.permission = this.$config.mode(res.data.permissions)
          })
          .catch(error => {
            this.$config.err_notice(this, error)
          })
      },
      getauthgroup () {
        axios.get(`${this.$config.url}/authgroup/group_name`)
          .then(res => {
            this.groupset = res.data.authgroup
          })
          .catch(error => {
            this.$config.err_notice(this, error)
          })
      },
      saveEditPass () {
        this.$refs['editPasswordForm'].validate((valid) => {
          if (valid) {
            this.savePassLoading = true
            axios.put(`${this.$config.url}/userinfo/changepwd`, {
              'username': this.userForm.username,
              'new': this.editPasswordForm.newPass
            })
              .then(res => {
                this.$config.notice(res.data)
                this.editPasswordModal = false
              })
              .catch(error => {
                this.$config.err_notice(this, error)
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
        axios.put(`${this.$config.url}/userinfo/changemail`, {
          'mail': this.editEmailForm.email,
          'username': this.userForm.username,
          'real': this.editEmailForm.real_name
        })
          .then(res => {
            this.$config.notice(res.data)
            this.editEmailModal = false
            sessionStorage.setItem('real_name', this.editEmailForm.real_name)
          })
          .catch(error => {
            this.$config.err_notice(this, error)
          })
        this.savePassLoading = false
      },
      init () {
        axios.put(`${this.$config.url}/homedata/ownspace`)
          .then(res => {
            this.userForm = res.data.userinfo
            this.authgroup = res.data.userinfo.auth_group.split(',')
            this.applygroup = res.data.userinfo.auth_group.split(',')
            this.formItem = this.$config.mode(res.data.permissons)
          })
      },
      putPermissionData () {
        this.savePassLoading = true
        axios.post(`${this.$config.url}/apply_grained/`, {
          'auth_group': this.applygroup,
          'grained_list': JSON.stringify(this.permission_list),
          'real_name': sessionStorage.getItem('real_name')
        })
          .then(res => {
            this.$config.notice(res.data)
            this.editInfoModal = false
          })
          .catch(error => {
            this.$config.err_notice(this, error)
          })
        this.savePassLoading = false
      }
    },
    mounted () {
      this.init()
      this.getauthgroup()
    }
  }
</script>
