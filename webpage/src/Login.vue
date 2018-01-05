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
      <Icon type="cube" size="60" style="margin-top:5%"></Icon>
      <p style="margin-left: 20%;margin-top: -10%;font-size: 20px">Yearning SQL 审计平台</p>
    </div>
    <br>
    <Card>
      <Form ref="formInline" :model="formInline" :rules="ruleInline" inline>
        <Form-item prop="user" style="width: 100%">
          <Input v-model="formInline.user" placeholder="Username">
          </Input>
        </Form-item>
        <Form-item prop="password" style="width: 100%">
          <Input type="password" v-model="formInline.password" placeholder="Password" @on-keyup.enter="authdata()">
          </Input>
        </Form-item>
        <Form-item style="width: 100%">
          <Button type="primary" @click="authdata()" style="width: 100%" size="large">登录</Button>
          <p style="margin-left: 20%;margin-top: 2%">如需注册账号请联系平台管理员</p>
        </Form-item>
      </Form>
    </Card>
  </div>
</div>
</template>
<script>
import axios from 'axios'
import util from './libs/util'
import ICol from '../node_modules/iview/src/components/grid/col.vue'
import Cookies from 'js-cookie'
export default {
  components: {
    ICol
  },
  name: 'Login',
  data () {
    return {
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
    authdata () {
      axios.post(util.auth, {
          'username': this.formInline.user,
          'password': this.formInline.password
        })
        .then(res => {
          axios.defaults.headers.common['Authorization'] = 'JWT ' + res.data['token']
          Cookies.set('user', this.formInline.user)
          Cookies.set('password', this.formInline.password)
          Cookies.set('jwt', 'JWT ' + res.data['token'])
          axios.post(`${util.url}/auth_twice`, {
              'user': Cookies.get('user')
            })
            .then(res => {
              let auth = res.data
              if (auth === 'admin') {
                Cookies.set('access', 0)
              } else {
                Cookies.set('access', 1)
              }
              this.$router.push({
                name: 'home_index'
              })
            })
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
