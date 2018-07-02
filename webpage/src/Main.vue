<style lang="less">
  @import "./main.less";
</style>
<template>
  <div id="main" class="main" :class="{'main-hide-text': hideMenuText}">
    <div class="sidebar-menu-con"
         :style="{width: hideMenuText?'60px':'200px', overflow: hideMenuText ? 'visible' : 'auto', background: $store.state.menuTheme === 'dark'?'#495060':'white'}">
      <div class="logo-con">
      </div>
      <sidebar-menu v-if="!hideMenuText" :menuList="menuList" :iconSize="14"/>
      <sidebar-menu-shrink :icon-color="menuIconColor" v-else :menuList="menuList"/>
    </div>
    <div class="main-header-con" :style="{paddingLeft: hideMenuText?'60px':'200px'}">
      <div class="main-header">
        <div class="navicon-con">
          <Button :style="{transform: 'rotateZ(' + (this.hideMenuText ? '-90' : '0') + 'deg)'}" type="text"
                  @click="toggleClick">
            <Icon type="navicon" size="32"></Icon>
          </Button>
        </div>
        <div class="header-middle-con">
          <div class="main-breadcrumb">
            <breadcrumb-nav :currentPath="currentPath"></breadcrumb-nav>
          </div>
        </div>
        <div class="header-avator-con">
          <a @mouseover="getc = true">捐助</a>
          <Modal
            v-model="getc"
            title="捐助Yearning"
            width="640">
            <h3>让Yearning持续提供更好的功能与服务。</h3>
            <br>
            <img height="300" width="300" src="./assets/alipay.jpg"/>
            <img height="300" width="300" src="./assets/wechat.jpg"/>
          </Modal>
          <a href="https://cookiey.github.io/Yearning-document/used/" target="_Blank">使用说明</a>
          <div @click="handleFullScreen" v-if="showFullScreenBtn" class="full-screen-btn-con">
            <Tooltip :content="isFullScreen ? '退出全屏' : '全屏'" placement="bottom">
              <Icon :type="isFullScreen ? 'arrow-shrink' : 'arrow-expand'" :size="23"></Icon>
            </Tooltip>
          </div>
          <div @click="lockScreen" class="lock-screen-btn-con">
            <Tooltip content="锁屏" placement="bottom">
              <Icon type="locked" :size="20"></Icon>
            </Tooltip>
          </div>
          <div @click="showMessage" class="message-con">
            <Tooltip :content="messageCount > 0 ? '有' + messageCount + '条未读消息' : '无未读消息'" placement="bottom">
              <Badge :count="messageCount" dot>
                <Icon type="ios-bell" :size="22"></Icon>
              </Badge>
            </Tooltip>
          </div>
          <div class="switch-theme-con">
            <Row class="switch-theme" type="flex" justify="center" align="middle">
              <theme-dropdown-menu></theme-dropdown-menu>
            </Row>
          </div>
          <div class="user-dropdown-menu-con">
            <Row type="flex" justify="end" align="middle" class="user-dropdown-innercon">
              <Dropdown trigger="click" @on-click="handleClickUserDropdown">
                <a href="javascript:void(0)">
                  <span class="main-user-name">{{ userName }}</span>
                  <Icon type="arrow-down-b"></Icon>
                </a>
                <DropdownMenu slot="list">
                  <DropdownItem name="ownSpace">个人中心</DropdownItem>
                  <DropdownItem name="loginout" divided>退出登录</DropdownItem>
                </DropdownMenu>
              </Dropdown>
              <Avatar :src="avatorPath" style="background: #ffffff;margin-left: 10px;"></Avatar>
            </Row>
          </div>
        </div>
      </div>
      <div class="tags-con">
        <tags-page-opened :pageTagsList="pageTagsList"></tags-page-opened>
      </div>
    </div>
    <div class="single-page-con" :style="{paddingLeft: hideMenuText?'60px':'200px'}">
      <div class="single-page">
        <template
          v-if="$route.name === 'ddledit'
        || $route.name === 'dmledit'
        || $route.name === 'view-dml'
        || $route.name === 'serach-sql'
        ">
          <keep-alive>
            <router-view></router-view>
          </keep-alive>
        </template>
        <template v-else>
          <router-view></router-view>
        </template>
      </div>
    </div>
    <Modal
      v-model="statement"
      title="欢迎使用Yearning SQL审核平台"
      width="600"
      :mask-closable="false"
      :closable="false"
      :styles="{top: '20%'}"
      ok-text="同意"
      @on-ok="statementput"
    >
      <h3>关于Yearning:</h3>
      <br>
      <p>Yearning 是一款基于inception的开源SQL审核平台。设计的目的便是让DBA能够从手动审核的环境中释放出来.让sql审核更加流程化,标准化,自动化。非常欢迎大家体验并使用Yearning!</p>
      <br>
      <H3>关于二次开发的声明:</H3>
      <br>
      <p>作为一款开源平台。Yearning很希望有更多的开发者一起参与到开发中。同时也鼓励各公司根据自身业务对平台进行二次开发及定制。
        Yearning v1.0.0 采用Apache2.0许可证,以下为许可中相关的义务与责任。</p>
      <p>1.需要给代码的用户一份Apache Licence</p>
      <p>2.如果你修改了代码，需要在被修改的文件中说明。</p>
      <p>3.在延伸的代码中（修改和有源代码衍生的代码中）需要带有原来代码中的协议，商标，专利声明和其他原来作者规定需要包含的说明。</p>
      <p>4.如果再发布的产品中包含一个Notice文件，则在Notice文件中需要带有Apache Licence。你可以在Notice中增加自己的许可，但不可以表现为对Apache Licence构成更改。</p>
      <br>
      <h3>免责声明:</h3>
      <br>
      <p>由Yearning平台所产生的一切后果,Yearning作者本人不负一切责任! 请在进行安全评估及测试体验后使用。</p>
      <br>
      <h3>当然用的喜欢,就打赏下我吧 ^_^ 左上角点击捐助</h3>
      <br>
      <p>此声明不会对非超级管理员用户推送。当接受上述条款并点击同意后,此通知将不会再次出现在超级管理员页面中。</p>
    </Modal>
  </div>
</template>
<script>
  import sidebarMenu from './main_components/sidebarMenu.vue'
  import tagsPageOpened from './main_components/tagsPageOpened.vue'
  import breadcrumbNav from './main_components/breadcrumbNav.vue'
  import themeDropdownMenu from './main_components/themeDropdownMenu.vue'
  import sidebarMenuShrink from './main_components/sidebarMenuShrink.vue'
  import axios from 'axios'
  // ;
  import util from './libs/util.js'

  export default {
    components: {
      sidebarMenu,
      tagsPageOpened,
      breadcrumbNav,
      themeDropdownMenu,
      sidebarMenuShrink
    },
    data () {
      return {
        spanLeft: 4,
        spanRight: 20,
        currentPageName: '',
        hideMenuText: false,
        userName: sessionStorage.getItem('user'),
        showFullScreenBtn: window.navigator.userAgent.indexOf('MSIE') < 0,
        isFullScreen: false,
        lockScreenSize: 0,
        avatorPath: 'static/avatar.png',
        getc: false,
        statement: false
      }
    },
    computed: {
      menuList () {
        return this.$store.state.menuList
      },
      pageTagsList () {
        return this.$store.state.pageOpenedList // 打开的页面的页面对象
      },
      currentPath () {
        return this.$store.state.currentPath // 当前面包屑数组
      },
      menuIconColor () {
        return this.$store.state.menuTheme === 'dark' ? 'white' : '#495060'
      },
      messageCount () {
        return this.$store.state.messageCount
      }
    },
    methods: {
      // 导航栏收缩
      toggleClick () {
        this.hideMenuText = !this.hideMenuText
      },
      // 个人中心
      handleClickUserDropdown (name) {
        if (name === 'ownSpace') {
          util.openPage(this, 'ownspace_index', '个人中心')
        } else if (name === 'loginout') {
          // 退出登录
          localStorage.clear()
          sessionStorage.clear()
          this.$router.push({
            name: 'login'
          })
        }
      },
      // 全屏
      handleFullScreen () {
        let main = document.getElementById('main')
        if (this.isFullScreen) {
          if (document.exitFullscreen) {
            document.exitFullscreen()
          } else if (document.mozCancelFullScreen) {
            document.mozCancelFullScreen()
          } else if (document.webkitCancelFullScreen) {
            document.webkitCancelFullScreen()
          } else if (document.msExitFullscreen) {
            document.msExitFullscreen()
          }
        } else {
          if (main.requestFullscreen) {
            main.requestFullscreen()
          } else if (main.mozRequestFullScreen) {
            main.mozRequestFullScreen()
          } else if (main.webkitRequestFullScreen) {
            main.webkitRequestFullScreen()
          } else if (main.msRequestFullscreen) {
            main.msRequestFullscreen()
          }
        }
      },
      // 消息中心
      showMessage () {
        util.openPage(this, 'message_index', '消息中心')
      },
      // 锁屏
      lockScreen () {
        let lockScreenBack = document.getElementById('lock_screen_back')
        lockScreenBack.style.transition = 'all 3s'
        lockScreenBack.style.zIndex = 10000
        lockScreenBack.style.boxShadow = '0 0 0 ' + this.lockScreenSize + 'px #667aa6 inset'
        this.showUnlock = true
        this.$store.commit('lock')
        sessionStorage.setItem('last_page_name', this.$route.name) // 本地存储锁屏之前打开的页面以便解锁后打开
        setTimeout(() => {
          lockScreenBack.style.transition = 'all 0s'
          this.$router.push({
            name: 'locking'
          })
        }, 800)
      },
      init () {
        // 全屏相关
        document.addEventListener('fullscreenchange', () => {
          this.isFullScreen = !this.isFullScreen
        })
        document.addEventListener('mozfullscreenchange', () => {
          this.isFullScreen = !this.isFullScreen
        })
        document.addEventListener('webkitfullscreenchange', () => {
          this.isFullScreen = !this.isFullScreen
        })
        document.addEventListener('msfullscreenchange', () => {
          this.isFullScreen = !this.isFullScreen
        })
        // 锁屏相关
        let lockScreenBack = document.getElementById('lock_screen_back')
        let x = document.body.clientWidth
        let y = document.body.clientHeight
        let r = Math.sqrt(x * x + y * y)
        let size = parseInt(r)
        this.lockScreenSize = size
        window.addEventListener('resize', () => {
          let x = document.body.clientWidth
          let y = document.body.clientHeight
          let r = Math.sqrt(x * x + y * y)
          let size = parseInt(r)
          this.lockScreenSize = size
          lockScreenBack.style.transition = 'all 0s'
          lockScreenBack.style.width = lockScreenBack.style.height = size + 'px'
        })
        lockScreenBack.style.width = lockScreenBack.style.height = size + 'px'
        // 问候信息相关
        if (!sessionStorage.getItem('hasGreet')) {
          let now = new Date()
          let hour = now.getHours()
          let greetingWord = {
            title: '',
            words: ''
          }
          let userName = sessionStorage.getItem('user')
          if (hour < 6) {
            greetingWord = {
              title: '凌晨好~' + userName,
              words: '早起的鸟儿有虫吃~'
            }
          } else if (hour >= 6 && hour < 9) {
            greetingWord = {
              title: '早上好~' + userName,
              words: '来一杯咖啡开启美好的一天~'
            }
          } else if (hour >= 9 && hour < 12) {
            greetingWord = {
              title: '上午好~' + userName,
              words: '工作要加油哦~'
            }
          } else if (hour >= 12 && hour < 14) {
            greetingWord = {
              title: '中午好~' + userName,
              words: '午饭要吃饱~'
            }
          } else if (hour >= 14 && hour < 17) {
            greetingWord = {
              title: '下午好~' + userName,
              words: '下午也要活力满满哦~'
            }
          } else if (hour >= 17 && hour < 19) {
            greetingWord = {
              title: '傍晚好~' + userName,
              words: '下班没事问候下爸妈吧~'
            }
          } else if (hour >= 19 && hour < 21) {
            greetingWord = {
              title: '晚上好~' + userName,
              words: '工作之余品一品书香吧~'
            }
          } else {
            greetingWord = {
              title: '深夜好~' + userName,
              words: '夜深了，注意休息哦~'
            }
          }
          this.$Notice.config({
            top: 130
          })
          this.$Notice.info({
            title: greetingWord.title,
            desc: greetingWord.words,
            duration: 4,
            name: 'greeting'
          })
          sessionStorage.setItem('hasGreet', 1)
        }
      },
      statementput () {
        axios.put(`${util.url}/homedata/statement`)
      }
    },
    mounted () {
      this.$store.commit('Breadcrumbset', this.$route.matched[1].name)
      this.$store.state.currentPageName = this.$route.matched[1].name
      if (localStorage.getItem('pageOpenedList')) {
        this.$store.state.pageOpenedList = JSON.parse(localStorage.getItem('pageOpenedList'))
      } else {
        this.$store.state.pageOpenedList = [{
          title: '首页',
          path: '',
          name: 'home_index'
        }]
      }
      this.init()
      axios.get(`${util.url}/homedata/messages?username=${sessionStorage.getItem('user')}`)
        .then(res => {
          this.$store.state.messageCount = res.data.count.messagecount
          if (res.data.statement !== '1') {
            this.statement = true
          }
        })
    },
    created () {
      // 权限菜单过滤相关
      this.$store.commit('Menulist')
      axios.defaults.headers.common['Authorization'] = sessionStorage.getItem('jwt')
    }
  }
</script>
