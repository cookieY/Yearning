// The Vue build version to load with the `import` command
// (runtime-only or standalone) has been set in webpack.base.conf with an alias.
import Vue from 'vue'
import Cookies from 'js-cookie'
import Subnet from './Subnet.vue'
import iView from 'iview'
import Vuex from 'vuex'
import VueRouter from 'vue-router'
import axios from 'axios'
import {MainRoute} from './router'
import store from './aspsm'
import 'iview/dist/styles/iview.css'
import util from './libs/util'
import particles from 'particles.js/particles'
Vue.config.productionTip = false
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
  util.title(to.meta.title)
  if (Cookies.get('locking') === '1' && to.name !== 'locking') { // 判断当前是否是锁定状态
    iView.LoadingBar.finish()
    next(false)
    router.replace({name: 'login'})
  } else if (Cookies.get('locking') === '0' && to.name === 'locking') {
    iView.LoadingBar.finish()
    next(false)
  } else {
    if (!Cookies.get('user') && to.name !== 'login') { // 判断是否已经登录且前往的页面不是登录页
      next({name: 'login'});
      iView.LoadingBar.finish()
    } else if (Cookies.get('user') && to.name === 'login') { // 判断是否已经登录且前往的是登录页
      next({name: 'login'});
      iView.LoadingBar.finish()
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
  components: { Subnet },
  store: store,
  router: router
})
