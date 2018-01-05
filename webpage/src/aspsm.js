import Vue from 'vue'
import Vuex from 'vuex'
import Cookies from 'js-cookie'
import {MainRoute, appRouter} from './router'
import util from './libs/util'
Vue.use(Vuex)
const store = new Vuex.Store({
  state: {
    // 数据库登陆信息
    formItem: {
      input: '',
      sqladdress: '',
      sqlserver: '',
      basename: '',
      table_name: '',
      select: '',
      date: ''
    },
    // 新的属性
    menuList: [],
    routers: [
      MainRoute, ...appRouter
    ],
    menuTheme: 'dark',
    currentPageName: 'home_index',
    currentPath: [
      {
        title: '首页',
        path: '/',
        name: 'home_index'
      }
    ],
    pageOpenedList: [
      {
        title: '首页',
        path: '',
        name: 'home_index'
      }
    ],
    tagsList: [...appRouter],
    cachePage: [],
    messageCount: 0
  },
  mutations: {
    Menulist (state) {
      let accessCode = parseInt(Cookies.get('access')) // 0
      let menuList = []
      appRouter.forEach((item, index) => {
        if (item.access !== undefined) { // item.access=0
          if (util.showThisRoute(item.access, accessCode)) {
            if (item.children.length <= 1) {
              menuList.push(item)
            } else {
              let i = menuList.push(item)
              let childrenArr = []
              childrenArr = item.children.filter(child => {
                if (child.access !== undefined) {
                  if (child.access === accessCode) {
                    return child
                  }
                } else {
                  return child
                }
              })
              menuList[i - 1].children = childrenArr
            }
          }
        } else { // 如果是权限页面
          if (item.children.length <= 1) {
            menuList.push(item)
          } else {
            let i = menuList.push(item)
            let childrenArr = []
            childrenArr = item.children.filter(child => {
              if (child.access !== undefined) {
                if (util.showThisRoute(child.access, accessCode)) {
                  return child
                }
              } else {
                return child
              }
            })
            menuList[i - 1].children = childrenArr
          }
        }
      })
      state.menuList = menuList
    },
    changeMenuTheme (state, theme) {
      state.menuTheme = theme;
    },
    lock (state) {
      Cookies.set('locking', '1');
    },
    unlock (state) {
      Cookies.set('locking', '0');
    },
    Breadcrumbset (state, name) {
      if (name === 'ownspace_index') {
        state.currentPath.splice(1, state.currentPath.length - 1)
        state.currentPath.push({'title': '个人中心', 'path': 'ownspace', 'name': name})
      } else if (name === 'message_index') {
        state.currentPath.splice(1, state.currentPath.length - 1)
        state.currentPath.push({'title': '消息中心', 'path': 'message', 'name': name})
      } else if (name === 'home_index') {
        state.currentPath.splice(1, state.currentPath.length - 1)
      } else {
        state.currentPath.splice(1, state.currentPath.length - 1)
        appRouter.forEach((val) => {
          for (let i of val.children) {
            if (i.name === name) {
              state.currentPath.push({
                'title': val.title,
                'path': val.path,
                'name': val.name
              }, {
                'title': i.title,
                'path': `${val.path}/${i.path}`,
                'name': i.name
              })
            }
          }
        })
      }
    },
    removeTag (state, name) {
      state.pageOpenedList.map((item, index) => {
        if (item.name === name) {
          state.pageOpenedList.splice(index, 1);
        }
      });
    }
  }
})

export default store
