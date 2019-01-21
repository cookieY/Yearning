<style>

  .btn_hover {
    width: 60px;
    margin-left: 30%;
    padding: 10px 0;
  }

</style>

<template>
  <div>
    <Icon type="md-cube" size="40" class="MenuIcon"></Icon>
    <template v-for="(item, index) in menuList">
      <Dropdown v-if="item.children.length >= 1 && item.name != 'main'" placement="right-start" :key="index"
                @on-click="changeMenu">
        <div class="btn_hover">
          <Icon size="20" :color="iconColor" :type="item.icon"></Icon>
        </div>
        <DropdownMenu style="width: 200px;" slot="list">
          <template v-for="child in item.children">
            <template v-if="filtermenulist[child.name] === '1'">
              <DropdownItem :name="child.name" :key="child.title">
                <Icon :type="child.icon" size="20"></Icon>
                <span style="padding-left:10px;">{{ child.title }}</span>
              </DropdownItem>
            </template>
          </template>
        </DropdownMenu>
      </Dropdown>
      <Dropdown v-else placement="right-start" :key="index" @on-click="changeMenu">
        <div @click="changeMenu(item.children[0].name)" class="btn_hover">
          <Icon :size="20" :color="iconColor" :type="item.icon"></Icon>
        </div>
        <DropdownMenu style="width: 200px;" slot="list">
          <DropdownItem :name="item.children[0].name" :key="item.children[0].title">
            <Icon :type="item.icon" size="20"></Icon>
            <span style="padding-left:10px;">{{ item.children[0].title }}</span>
          </DropdownItem>
        </DropdownMenu>
      </Dropdown>
    </template>
    <Dropdown placement="right-start" @on-click="changeMenu">
      <div @click="changeMenu()" class="btn_hover">
        <Icon type="md-person" size="20" :color="iconColor"></Icon>
      </div>
      <DropdownMenu slot="list">
        <DropdownItem name="myorder" key="myorder">我的工单</DropdownItem>
      </DropdownMenu>
    </Dropdown>
    <Dropdown placement="right-start" @on-click="changeMenu">
      <div @click="changeMenu('login')" class="btn_hover">
        <Icon type="md-log-out" size="20" :color="iconColor"></Icon>
      </div>
      <DropdownMenu slot="list">
        <DropdownItem name="login" key="login">退出</DropdownItem>
      </DropdownMenu>
    </Dropdown>
  </div>
</template>

<script>
  import axios from 'axios'

  export default {
    name: 'sidebarMenuShrink',
    props: {
      menuList: {
        type: Array
      },
      iconColor: {
        type: String,
        default: 'white'
      }
    },
    data () {
      return {
        currentPageName: this.$route.name,
        openedSubmenuArr: this.$store.state.openedSubmenuArr,
        filtermenulist: {
          'ddledit': '',
          'dmledit': '',
          'indexedit': '',
          'serach-sql': '1',
          'management-user': '',
          'management-database': '',
          'audit-audit': '1',
          'audit-record': '1',
          'audit-permissions': '1',
          'search_order': '1',
          'query-review': '1',
          'query-audit': '1',
          'auth-group': '1'
        }
      }
    },
    methods: {
      changeMenu (active) {
        if (active === 'login') {
          localStorage.clear()
          sessionStorage.clear()
          this.$router.push({
            name: active
          })
        } else {
          this.$config.openPage(this, active)
        }
      }
    },
    created () {
      axios.get(`${this.$config.url}/homedata/menu`)
        .then(res => {
          let c = JSON.parse(res.data)
          this.filtermenulist.ddledit = c.ddl
          this.filtermenulist.indexedit = c.ddl
          this.filtermenulist.dmledit = c.dml
          this.filtermenulist['management-user'] = c.user
          this.filtermenulist['management-database'] = c.base
        })
    }
  }
</script>
