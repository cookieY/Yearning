<style>
  .Menu span {
    color: #ffffff;
    font-weight: bold;
    margin-left: 10%;
  }

  .MenuIcon {
    margin-left: 10%;
    margin-top: 0.1%;
    color: #5cadff;
  }
</style>
<template>
  <Menu width="auto" theme="dark" @on-select="currentPageTab" :active-name="currentPageName" accordion>
    <MenuItem name="main">
      <Icon type="md-cube" size="50" class="MenuIcon"></Icon>
      <br>
      <span>JLC SQL审核平台</span>
    </MenuItem>
    <MenuItem name="home_index">
      <Icon type="md-home" :size="iconSize"></Icon>
      <span class="layout-text">首页</span>
    </MenuItem>
    <MenuItem name="myorder">
      <Icon type="md-person" :size="iconSize"></Icon>
      <span class="layout-text">我的工单</span>
    </MenuItem>
    <template v-for="item of menuList">
      <Submenu v-if="item.children.length >= 1 && item.name !== 'main'" :name="item.name" :key="item.path">
        <template slot="title">
          <Icon :type="item.icon" :size="iconSize"></Icon>
          <span class="layout-text">{{ item.title }}</span>
        </template>
        <template v-for="child of item.children">
          <MenuItem :name="child.name" :key="child.name" style="margin-left: -5%">
            <Icon :type="child.icon" :size="iconSize" :key="child.name"></Icon>
            <span class="layout-text" :key="child.name + 1">{{ child.title }}</span>
          </MenuItem>
        </template>
      </Submenu>
    </template>
    <Menu-item name="login">
      <Icon type="md-log-out" :size="iconSize"></Icon>
      <span class="layout-text">退出</span>
    </Menu-item>
  </Menu>
</template>
<script>
  //
  import util from '../libs/util'
  import axios from 'axios'

  export default {
    name: 'sidebarMenu',
    props: {
      menuList: Array,
      iconSize: Number
    },
    data () {
      return {
        filtermenulist: []
      }
    },
    computed: {
      currentPageName () {
        return this.$store.state.currentPageName
      }
    },
    methods: {
      currentPageTab (val) {
        if (val === 'login') {
          localStorage.clear()
          sessionStorage.clear()
          this.$router.push({
            name: 'login'
          })
        } else {
          util.openPage(this, val)
        }
      }
    },
    created () {
      axios.get(`${util.url}/homedata/menu`)
        .then(res => {
          let c = JSON.parse(res.data)
          this.filtermenulist = Object.keys(c).filter((item) => c[item] === '1')
        })
    }
  }
</script>
