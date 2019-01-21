// The Vue build version to load with the `import` command
// (runtime-only or standalone) has been set in webpack.base.conf with an alias.
import Vue from 'vue'
import Subnet from './subnet.vue'
import iView from 'iview'
import Vuex from 'vuex'
import VueRouter from 'vue-router'
import axios from 'axios'
import { MainRoute } from './router'
import store from './aspsm'
import 'iview/dist/styles/iview.css'
import config from './libs/util'
import particles from 'particles.js/particles'

Vue.config.productionTip = false
Vue.prototype.$config = config
Vue.use(particles)
Vue.use(Vuex)
Vue.use(iView)
Vue.use(VueRouter)
Vue.prototype.$http = axios
/* eslint-disable no-new */
const RouterConfig = {
  routes: MainRoute
}

const router = new VueRouter(RouterConfig)

router.beforeEach((to, from, next) => {
  iView.LoadingBar.start()
  config.title(to.meta.title)
  if (sessionStorage.getItem('locking') === '1' && to.name !== 'locking') { // 判断当前是否是锁定状态
    next(false)
    router.replace({name: 'login'})
  } else {
    if (!sessionStorage.getItem('user') && to.name !== 'login') { // 判断是否已经登录且前往的页面不是登录页
      next(false)
      router.replace({name: 'login'})
    } else {
      next()
    }
  }
})

router.afterEach(() => {
  iView.LoadingBar.finish()
  window.scrollTo(0, 0)
})

new Vue({
  el: '#Subnet',
  template: '<Subnet/>',
  components: {Subnet},
  store: store,
  router: router
})
