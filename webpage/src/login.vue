<style>
  .div-relative {
    position: relative;
    width: 100%;
  }

  .div-a {
    position: absolute;
    left: 38%;
    top: 20%;
    width: 350px;
    height: 100px
  }
</style>

<template>
  <div id="band" class="div-relative">
    <div class="div-a">
      <div style='margin-left: 10%'>
        <Icon type="md-cube" size="60" style="margin-top:5%"></Icon>
        <p style="margin-left: 20%;margin-top: -10%;font-size: 20px">Yearning SQL 审核平台</p>
      </div>
      <br>
      <Card>
        <Tabs value="custom" style="max-height: 280px;overflow:scroll;">
          <TabPane label="普通登陆" name="custom">
            <Form ref="formInline" :model="formInline" :rules="ruleInline" inline>
              <Form-item prop="user" style="width: 100%">
                <Input v-model="formInline.user" placeholder="Username"></Input>
              </Form-item>
              <Form-item prop="password" style="width: 100%">
                <Input type="password" v-model="formInline.password" placeholder="Password"></Input>
              </Form-item>
              <Form-item style="width: 100%">
                <Button type="primary" @click="authdata()" style="width: 100%" size="large">登录</Button>
                <p style="margin-left: 5%;margin-top: 5%">2018 © Power By Cookie.Ye 使用chrome获得最佳体验</p>
              </Form-item>
            </Form>
          </TabPane>
          <!--自己添加-->
          <TabPane label="LDAP登陆" name="ldap">
            <Form ref="formInline" :model="formInline" :rules="ruleInline" inline>
              <Form-item prop="user" style="width: 100%">
                <Input v-model="formInline.user" placeholder="ldap_Username"></Input>
              </Form-item>
              <Form-item prop="password" style="width: 100%">
                <Input type="password" v-model="formInline.password" placeholder="ldap_Password"
                       @on-keyup.enter="authdata()"></Input>
              </Form-item>
              <Form-item style="width: 100%">
                <Button type="primary" @click="ldap_login()" style="width: 100%" size="large">登录</Button>
                <p style="margin-left: 5%;margin-top: 5%">2018 © Power By Cookie.Ye 使用chrome获得最佳体验</p>
              </Form-item>
            </Form>
          </TabPane>
          <!--自己添加-->
          <TabPane label="用户注册" name="register">
            <Form ref="userinfova" :model="userinfo" :rules="userinfoValidate" inline>

              <Form-item prop="username" style="width: 100%">
                <Input v-model="userinfo.username" placeholder="用户名"></Input>
              </Form-item>

              <Form-item prop="password" style="width: 100%">
                <Input type="password" v-model="userinfo.password" placeholder="密码" @on-keyup.enter="authdata()"></Input>
              </Form-item>

              <Form-item prop="confirmpassword" style="width: 100%">
                <Input v-model="userinfo.confirmpassword" placeholder="确认密码" type="password"></Input>
              </Form-item>

              <Form-item prop="email" style="width: 100%">
                <Input v-model="userinfo.email" placeholder="E-mail"></Input>
              </Form-item>

              <Form-item style="width: 100%">
                <Button type="primary" @click="LoginRegister()" style="width: 100%" size="large">注册</Button>
                <p style="margin-left: 5%;margin-top: 5%">2018 © Power By Cookie.Ye 使用chrome获得最佳体验</p>
              </Form-item>
            </Form>
          </TabPane>
        </Tabs>
      </Card>
    </div>
  </div>
</template>
<script>
  import axios from 'axios'
  import util from './libs/util'
  import ICol from '../node_modules/iview/src/components/grid/col.vue'
  //
  export default {
    components: {
      ICol
    },
    name: 'Login',
    data () {
      const valideuserinfoPassword = (rule, value, callback) => {
        if (value !== this.userinfo.password) {
          callback(new Error('两次输入密码不一致'))
        } else {
          callback()
        }
      }
      return {
        userinfo: {
          username: '',
          password: '',
          confirmpassword: '',
          email: '',
          authgroup: []
        },
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
          email: [{
            required: true,
            message: '请输入邮箱名称',
            trigger: 'blur'
          }]
        },
        formInline: {
          user: '',
          password: ''
        },
        ruleInline: {
          user: [{
            required: true,
            message: '请填写用户名',
            trigger: 'blur'
          }],
          password: [{
            required: true,
            message: '请填写密码',
            trigger: 'blur'
          },
            {
              type: 'string',
              min: 6,
              message: '密码长度不能小于6位',
              trigger: 'blur'
            }
          ]
        }
      }
    },
    methods: {
      LoginRegister () {
        this.$refs['userinfova'].validate((valid) => {
          if (valid) {
            axios.post(util.url + '/loginregister/', {
              'username': this.userinfo.username,
              'password': this.userinfo.password,
              'confirmpassword': this.userinfo.confirmpassword,
              'email': this.userinfo.email,
              'auth_group': JSON.stringify(this.userinfo.authgroup)
            })
              .then(res => {
                util.notice(res.data)
                this.userinfo = {
                  username: '',
                  password: '',
                  confirmpassword: '',
                  email: ''
                }
              })
              .catch(error => {
                util.err_notice(error)
              })
          }
        })
      },
      authdata () {
        axios.post(util.auth, {
          'username': this.formInline.user,
          'password': this.formInline.password
        })
          .then(res => {
            axios.defaults.headers.common['Authorization'] = 'JWT ' + res.data['token']
            sessionStorage.setItem('user', this.formInline.user)
            sessionStorage.setItem('password', this.formInline.password)
            sessionStorage.setItem('jwt', `JWT ${res.data['token']}`)
            sessionStorage.setItem('auth', res.data['permissions'])
            let auth = res.data['permissions']
            if (auth === 'admin' || auth === 'perform') {
              sessionStorage.setItem('access', 0)
            } else {
              sessionStorage.setItem('access', 1)
            }
            this.$router.push({
              name: 'home_index'
            })
          })
          .catch(error => {
            util.ajanxerrorcode(this, error)
          })
      },
      ldap_login () {
        axios.post(`${util.url}/ldapauth`, {
          'username': this.formInline.user,
          'password': this.formInline.password
        })
          .then(res => {
            if (res.data['token'] === 'null') {
              this.$Notice.error({
                title: '警告',
                desc: res.data['res']
              })
            } else {
              axios.defaults.headers.common['Authorization'] = 'JWT ' + res.data['token']
              sessionStorage.setItem('user', this.formInline.user)
              sessionStorage.setItem('password', this.formInline.password)
              sessionStorage.setItem('jwt', `JWT ${res.data['token']}`)
              sessionStorage.setItem('auth', res.data['permissions'])
              let auth = res.data['permissions']
              if (auth === 'admin') {
                sessionStorage.setItem('access', 0)
              } else {
                sessionStorage.setItem('access', 1)
              }
              this.$router.push({
                name: 'home_index'
              })
            }
          })
          .catch(error => {
            util.ajanxerrorcode(this, error)
          })
      }
    },
    mounted () {
      window.particlesJS.load('band', '/static/particlesjs-config.json')
    }
  }
</script>
