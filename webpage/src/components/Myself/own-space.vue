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
        <FormItem label="权限：">
          <span>{{ userForm.group }}</span>
        </FormItem>
        <FormItem label="登录密码：">
          <Button type="text" size="small" @click="editPasswordModal=true">修改密码</Button>
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
</div>
</template>

<script>
import Cookies from 'js-cookie'
import util from '../../libs/util'
import axios from 'axios'
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
      userForm: {
        name: '',
        group: '',
        department: ''
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
    init () {
      axios.put(`${util.url}/homedata/ownspace`, {
          'user': Cookies.get('user')
        })
        .then(res => {
          this.userForm.name = Cookies.get('user');
          this.userForm.group = res.data.group;
          this.userForm.department = res.data.department;
        })
    }
  },
  mounted () {
    this.init();
  }
};
</script>
