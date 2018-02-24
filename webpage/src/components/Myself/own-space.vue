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
            <span>{{ userForm.name }}</span>
          </div>
        </FormItem>
        <FormItem label="部门：">
          <span>{{ userForm.department }}</span>
        </FormItem>
        <FormItem label="权限分类：">
          <span>{{ userForm.group }}</span>
        </FormItem>
        <FormItem label="具体权限：">
          <br>
        <FormItem label="DDL提交权限:">
          <p>{{formItem.ddl}}</p>
        </FormItem>
          <FormItem label="可访问的连接名:" v-if="formItem.ddl === '是'">
            <p>{{formItem.ddlcon}}</p>
          </FormItem>
          <FormItem label="DML提交权限:">
            <p>{{formItem.dml}}</p>
          </FormItem>
          <FormItem label="可访问的连接名:" v-if="formItem.dml === '是'">
            <p>{{formItem.dmlcon}}</p>
          </FormItem>
          <FormItem label="字典查看权限:">
            <p>{{formItem.dic}}</p>
          </FormItem>
          <FormItem label="可访问的连接名:" v-if="formItem.dic === '是'">
            <p>{{formItem.diccon}}</p>
          </FormItem>
          <FormItem label="数据查询权限:">
            <p>{{formItem.query}}</p>
          </FormItem>
          <FormItem label="可访问的连接名:" v-if="formItem.query === '是'">
            <p>{{formItem.querycon}}</p>
          </FormItem>
          <FormItem label="用户管理权限:">
            <p>{{formItem.user}}</p>
          </FormItem>
          <FormItem label="数据库管理权限:">
            <p>{{formItem.base}}</p>
          </FormItem>
        </FormItem>
        <FormItem label="编辑：">
          <Button type="text" size="small" @click="editPasswordModal=true">修改密码</Button>
          <br>
          <Button type="text" size="small" @click="editEmailModal=true">修改邮箱</Button>
        </FormItem>
      </Form>
    </div>
  </Card>
  <Modal v-model="editPasswordModal" :closable='false' :mask-closable=false :width="500">
    <h3 slot="header" style="color:#2D8CF0">修改密码</h3>
    <Form ref="editPasswordForm" :model="editPasswordForm" :label-width="100" label-position="right" :rules="passwordValidate">
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
</div>
</template>

<script>
import Cookies from 'js-cookie'
import util from '../../libs/util'
import axios from 'axios'
const exchangetype = function typeok (vl) {
  if (typeof vl === 'string') {
    if (vl === '1') {
      return '是'
    } else {
      return '否'
    }
  } else {
    return vl.toString()
  }
}
export default {
  name: 'own-space',
  data () {
    const valideRePassword = (rule, value, callback) => {
      if (value !== this.editPasswordForm.newPass) {
        callback(new Error('两次输入密码不一致'));
      } else {
        callback();
      }
    };
    return {
      editEmailModal: false,
      editEmailForm: {
        mail: ''
      },
      userForm: {
        name: '',
        group: '',
        department: '',
        permisson: []
      },
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
      }
    };
  },
  methods: {
    saveEditPass () {
      this.$refs['editPasswordForm'].validate((valid) => {
        if (valid) {
          this.savePassLoading = true;
          axios.post(`${util.url}/otheruser/changepwd`, {
              'username': Cookies.get('user'),
              'new': this.editPasswordForm.newPass,
              'old': this.editPasswordForm.oldPass
            })
            .then(res => {
              this.$Notice.success({
                title: '通知',
                desc: res.data
              })
              this.editPasswordModal = false;
            })
            .catch(error => {
              util.ajanxerrorcode(this, error)
            })
          this.savePassLoading = false
        }
      })
      for (let i in this.editPasswordForm) {
        this.editPasswordForm[i] = ''
      }
    },
    saveEmail () {
      this.savePassLoading = true;
      axios.put(`${util.url}/otheruser/mail`, {'mail': this.editEmailForm.mail})
        .then(res => {
          this.$Notice.success({
            title: '通知',
            desc: res.data
          })
          this.editEmailModal = false;
        })
        .catch(error => {
          util.ajanxerrorcode(this, error)
        })
      this.savePassLoading = false
    },
    init () {
      axios.put(`${util.url}/homedata/ownspace`, {
          'user': Cookies.get('user')
        })
        .then(res => {
          this.userForm.name = Cookies.get('user');
          this.userForm.group = res.data.userinfo.group;
          this.userForm.department = res.data.userinfo.department;
          this.formItem.ddl = exchangetype(res.data.permissons.ddl)
          this.formItem.ddlcon = exchangetype(res.data.permissons.ddlcon)
          this.formItem.dml = exchangetype(res.data.permissons.dml)
          this.formItem.dmlcon = exchangetype(res.data.permissons.dmlcon)
          this.formItem.dic = exchangetype(res.data.permissons.dic)
          this.formItem.diccon = exchangetype(res.data.permissons.diccon)
          this.formItem.query = exchangetype(res.data.permissons.query)
          this.formItem.querycon = exchangetype(res.data.permissons.querycon)
          this.formItem.user = exchangetype(res.data.permissons.user)
          this.formItem.base = exchangetype(res.data.permissons.base)
        })
    }
  },
  mounted () {
    this.init();
  }
};
</script>
