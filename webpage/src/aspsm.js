import Vue from 'vue'
import Vuex from 'vuex'
import {
  MainRoute,
  appRouter
} from './router'
import util from './libs/util'

Vue.use(Vuex)
const store = new Vuex.Store({
  state: {
    // 数据库登录信息
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
    currentPageName: 'home_index',
    currentPath: [{
      title: '首页',
      path: '/',
      name: 'home_index'
    }],
    pageOpenedList: [{
      title: '首页',
      path: '',
      name: 'home_index'
    }],
    tagsList: [...appRouter],
    cachePage: []
  },
  mutations: {
    clearAllTags (state) {
      state.pageOpenedList.splice(1)
      state.cachePage.length = 0
      localStorage.pageOpenedList = JSON.stringify(state.pageOpenedList)
    },
    clearOtherTags (state, vm) {
      let currentName = vm.$route.name
      let currentIndex = 0
      state.pageOpenedList.forEach((item, index) => {
        if (item.name === currentName) {
          currentIndex = index
        }
      })
      if (currentIndex === 0) {
        state.pageOpenedList.splice(1)
      } else {
        state.pageOpenedList.splice(currentIndex + 1)
        state.pageOpenedList.splice(1, currentIndex - 1)
      }
      let newCachepage = state.cachePage.filter(item => {
        return item === currentName
      })
      state.cachePage = newCachepage
      localStorage.pageOpenedList = JSON.stringify(state.pageOpenedList)
    },
    Menulist (state) {
      let accessCode = parseInt(sessionStorage.getItem('access')) // 0
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
        } else {
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
    lock () {
      sessionStorage.setItem('locking', '1')
    },
    unlock () {
      sessionStorage.setItem('locking', '0')
    },
    Breadcrumbset (state, name) {
      if (name === 'ownspace_index') {
        state.currentPath.splice(1, state.currentPath.length - 1)
        state.currentPath.push({
          'title': '个人中心',
          'path': 'ownspace',
          'name': name
        })
      } else if (name === 'home_index') {
        state.currentPath.splice(1, state.currentPath.length - 1)
      } else if (name === 'myorder') {
        state.currentPath.splice(1, state.currentPath.length - 1)
        state.currentPath.push({
          'title': '我的工单',
          'path': 'message',
          'name': name
        })
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
          state.pageOpenedList.splice(index, 1)
        }
      })
    }
  }
})

export default store
