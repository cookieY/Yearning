// import env from '../../config/env';
import Notice from 'iview/src/components/notice'
import { appRouter } from '../router'

let util = {}
util.title = function (title) {
  title = title || 'Yearning SQL审核平台'
  window.document.title = title
}

util.err_notice = function (err) {
  Notice.error({
    title: '错误',
    desc: err
  })
}

util.notice = function (vl) {
  Notice.info({
    title: '通知',
    desc: vl
  })
}

util.url = location.protocol + '//' + document.domain + ':8000/api/v1'

util.auth = location.protocol + '//' + document.domain + ':8000/api-token-auth/'

util.ajanxerrorcode = function (vm, error) {
  if (error.response) {
    if (error.response.status === 401) {
      vm.$router.push({name: 'error_401'})
    } else if (error.response.status === 400) {
      Notice.error({title: '警告', desc: '账号密码错误,请重新输入!'})
    } else if (error.response.status === 500) {
      vm.$router.push({name: 'error_500'})
    } else if (error.response.status === 404) {
      vm.$router.push({name: 'error_404'})
    } else {
      Notice.error({title: '警告', desc: error.response})
    }
  }
}

util.oneOf = function (ele, targetArr) {
  if (targetArr.indexOf(ele) >= 0) {
    return true
  } else {
    return false
  }
}

util.showThisRoute = function (itAccess, currentAccess) {
  if (typeof itAccess === 'object' && itAccess.isArray()) {
    return util.oneOf(currentAccess, itAccess)
  } else {
    return itAccess === currentAccess
  }
}

util.openPage = function (vm, name) {
  vm.$router.push({name: name})
  vm.$store.commit('Breadcrumbset', name)
  vm.$store.state.currentPageName = name
  util.taglist(vm, name)
}

util.taglist = function (vm, name) {
  vm.$store.state.pageOpenedList.forEach((vl, index) => {
    if (vl.name === name && name !== 'home_index') {
      vm.$store.state.pageOpenedList.splice(index, 1)
    }
  })
  if (name === 'myorder') {
    vm.$store.state.pageOpenedList.push({'title': '我的工单', 'name': 'myorder'})
  }
  appRouter.forEach((val) => {
    for (let i of val.children) {
      if (i.name === name && name !== 'home_index') {
        vm.$store.state.pageOpenedList.push({'title': i.title, 'name': i.name})
      }
    }
  })
  localStorage.setItem('pageOpenedList', JSON.stringify(vm.$store.state.pageOpenedList))
}

export default util
