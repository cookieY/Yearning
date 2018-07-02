import Index from './Main.vue'

export const loginRouter = {
  path: '/login',
  name: 'login',
  meta: {
    title: 'Login - 登录'
  },
  component: resolve => {
    require(['./Login.vue'], resolve)
  }
}
export const locking = {
  path: '/locking',
  name: 'locking',
  component: resolve => {
    require(['./main_components/locking-page.vue'], resolve)
  }
}

export const page404 = {
  path: '/*',
  name: 'error_404',
  meta: {
    title: '404-页面不存在'
  },
  component: resolve => {
    require(['./components/Error/404.vue'], resolve)
  }
}

export const page401 = {
  path: '/401',
  meta: {
    title: '401-权限不足'
  },
  name: 'error_401',
  component: resolve => {
    require(['./components/Error/401.vue'], resolve)
  }
}

export const page500 = {
  path: '/500',
  meta: {
    title: '500-服务端错误'
  },
  name: 'error_500',
  component: resolve => {
    require(['./components/Error/500.vue'], resolve)
  }
}

export const appRouter = [
  {
    path: '/',
    icon: 'home',
    name: 'main',
    title: '首页',
    component: Index,
    redirect: '/home',
    children: [
      {
        path: 'home',
        title: '首页',
        name: 'home_index',
        component: resolve => {
          require(['./components/home/home.vue'], resolve)
        }
      },
      {
        path: 'ownspace',
        title: '个人中心',
        name: 'ownspace_index',
        component: resolve => {
          require(['./components/Myself/own-space.vue'], resolve)
        }
      },
      {
        path: 'message',
        title: '消息中心',
        name: 'message_index',
        component: resolve => {
          require(['./components/Myself/message.vue'], resolve)
        }
      }
    ]
  },
  {
    path: '/order',
    icon: 'folder',
    name: 'order',
    title: '工单提交',
    component: Index,
    children: [
      {
        path: 'ddledit',
        name: 'ddledit',
        title: 'DDL',
        'icon': 'compose',
        component: resolve => {
          require(['./components/Order/GenSQL.vue'], resolve)
        }
      },
      {
        path: 'dmledit',
        name: 'dmledit',
        title: 'DML',
        'icon': 'code',
        component: resolve => {
          require(['./components/Order/SQLsyntax.vue'], resolve)
        }
      },
      {
        path: 'indexedit',
        name: 'indexedit',
        title: '索引',
        'icon': 'share',
        component: resolve => {
          require(['./components/Order/GenIndex.vue'], resolve)
        }
      }
    ]
  },
  {
    path: '/view',
    icon: 'search',
    name: 'view',
    title: '查询',
    component: Index,
    children: [
      {
        path: 'view-dml',
        name: 'view-dml',
        title: '数据库字典',
        'icon': 'ios-book',
        component: resolve => {
          require(['./components/Search/DataBaseDic.vue'], resolve)
        }
      },
      {
        path: 'serach-sql',
        name: 'serach-sql',
        title: 'SQL查询',
        'icon': 'podium',
        component: resolve => {
          require(['./components/Search/work_flow.vue'], resolve)
        }
      }
    ]
  },
  {
    path: '/audit',
    icon: 'android-open',
    name: 'audit',
    title: '审核',
    component: Index,
    access: 0,
    children: [
      {
        path: 'audit-order',
        name: 'audit-audit',
        title: '工单',
        'icon': 'edit',
        component: resolve => {
          require(['./components/Audit/AuditSql.vue'], resolve)
        }
      },
      {
        path: 'audit-permissions',
        name: 'audit-permissions',
        title: '权限',
        'icon': 'android-share-alt',
        component: resolve => {
          require(['./components/Audit/Permissions.vue'], resolve)
        }
      },
      {
        path: 'query-audit',
        name: 'query-audit',
        title: '查询',
        'icon': 'social-rss',
        component: resolve => {
          require(['./components/Audit/Query_audit.vue'], resolve)
        }
      }
    ]
  },
  {
    path: '/record',
    icon: 'pie-graph',
    name: 'record',
    title: '记录',
    component: Index,
    access: 0,
    children: [
      {
        path: 'query-review',
        name: 'query-review',
        title: '查询审计',
        'icon': 'arrow-graph-up-right',
        component: resolve => {
          require(['./components/Audit/Query_record.vue'], resolve)
        }
      },
      {
        path: 'audit-record',
        name: 'audit-record',
        title: '工单记录',
        'icon': 'android-drafts',
        component: resolve => {
          require(['./components/Audit/Record.vue'], resolve)
        }
      }
    ]
  },
  {
    path: '/management',
    icon: 'social-buffer',
    name: 'management',
    title: '管理',
    access: 0,
    component: Index,
    children: [
      {
        path: 'management-user',
        name: 'management-user',
        title: '用户',
        'icon': 'person-stalker',
        component: resolve => {
          require(['./components/Management/UserInfo.vue'], resolve)
        }
      },
      {
        path: 'management-database',
        name: 'management-database',
        title: '数据库',
        'icon': 'social-buffer',
        component: resolve => {
          require(['./components/Management/MamagementBase.vue'], resolve)
        }
      },
      {
        path: 'setting',
        name: 'setting',
        title: '设置',
        'icon': 'android-settings',
        component: resolve => {
          require(['./components/Management/Setting.vue'], resolve)
        }
      }
    ]
  }
]

export const orderList = {
  path: '/',
  icon: 'home',
  name: 'main',
  title: '首页',
  component: Index,
  redirect: '/home',
  children: [
    {
      path: 'orderlist',
      title: '工单详情',
      name: 'orderlist',
      component: resolve => {
        require(['./components/Order/MyorderList.vue'], resolve)
      }
    }
  ]
}

export const queryList = {
  path: '/',
  icon: 'home',
  name: 'main',
  title: '首页',
  component: Index,
  redirect: '/home',
  children: [
    {
      path: 'querylist',
      title: '查询审计详情',
      name: 'querylist',
      component: resolve => {
        require(['./components/Audit/expend.vue'], resolve)
      }
    }
  ]
}

export const querypage = {
  path: '/',
  icon: 'home',
  name: 'main',
  title: '首页',
  component: Index,
  redirect: '/home',
  children: [
    {
      path: 'querypage',
      title: '查询',
      name: 'querypage',
      component: resolve => {
        require(['./components/Search/QuerySQL.vue'], resolve)
      }
    }
  ]
}

export const queryready = {
  path: '/',
  icon: 'home',
  name: 'main',
  title: '首页',
  component: Index,
  redirect: '/home',
  children: [
    {
      path: 'queryready',
      title: '查询申请进度',
      name: 'queryready',
      component: resolve => {
        require(['./components/Search/PutReady.vue'], resolve)
      }
    }
  ]
}

export const myorder = {
  path: '/',
  icon: 'home',
  name: 'main',
  title: '首页',
  component: Index,
  redirect: '/home',
  children: [
    {
      path: 'myorder',
      name: 'myorder',
      title: '我的工单',
      'icon': 'person',
      component: resolve => {
        require(['./components/Order/MyOrder.vue'], resolve)
      }
    }
  ]
}
export const MainRoute = [
  loginRouter,
  locking,
  ...appRouter,
  orderList,
  queryList,
  queryready,
  querypage,
  myorder,
  page404,
  page401,
  page500
]
