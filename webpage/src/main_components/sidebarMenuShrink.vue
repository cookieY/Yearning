<template>
  <div>
    <Icon type="cube" size="40" class="MenuIcon"></Icon>
    <template v-for="(item, index) in menuList">
      <Dropdown v-if="item.children.length >= 1 && item.name != 'main'" placement="right-start" :key="index"
                @on-click="changeMenu">
        <Button style="width: 70px;margin-left: -5px;padding:10px 0;" type="text">
          <Icon :size="20" :color="iconColor" :type="item.icon"></Icon>
        </Button>
        <DropdownMenu style="width: 200px;" slot="list">
          <template v-for="child in item.children">
            <template v-if="filtermenulist[child.name] === '1'">
              <DropdownItem :name="child.name" :key="child.title">
                <Icon :type="child.icon"></Icon>
                <span style="padding-left:10px;">{{ child.title }}</span>
              </DropdownItem>
            </template>
          </template>
        </DropdownMenu>
      </Dropdown>
      <Dropdown v-else placement="right-start" :key="index" @on-click="changeMenu">
        <Button @click="changeMenu(item.children[0].name)" style="width: 70px;margin-left: -5px;padding:10px 0;"
                type="text">
          <Icon :size="20" :color="iconColor" :type="item.icon"></Icon>
        </Button>
        <DropdownMenu style="width: 200px;" slot="list">
          <DropdownItem :name="item.children[0].name" :key="item.children[0].title">
            <Icon :type="item.icon"></Icon>
            <span style="padding-left:10px;">{{ item.children[0].title }}</span>
          </DropdownItem>
        </DropdownMenu>
      </Dropdown>
    </template>
    <Dropdown placement="right-start" @on-click="changeMenu">
      <Button @click="changeMenu()" style="width: 70px;margin-left: -5px;padding:10px 0;" type="text">
        <Icon type="person" size="20" :color="iconColor"></Icon>
      </Button>
      <DropdownMenu slot="list">
        <DropdownItem name="myorder" key="myorder">我的工单</DropdownItem>
      </DropdownMenu>
    </Dropdown>
    <Dropdown placement="right-start" @on-click="changeMenu">
      <Button @click="changeMenu('login')" style="width: 70px;margin-left: -5px;padding:10px 0;" type="text">
        <Icon type="log-out" size="20" :color="iconColor"></Icon>
      </Button>
      <DropdownMenu slot="list">
        <DropdownItem name="login" key="login">退出</DropdownItem>
      </DropdownMenu>
    </Dropdown>
  </div>
</template>

<script>
  //
  import util from '../libs/util'
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
          'view-dml': '',
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
          util.openPage(this, active)
        }
      }
    },
    created () {
      axios.get(`${util.url}/homedata/menu`)
        .then(res => {
          let c = JSON.parse(res.data)
          this.filtermenulist.ddledit = c.ddl
          this.filtermenulist.indexedit = c.ddl
          this.filtermenulist.dmledit = c.dml
          this.filtermenulist['view-dml'] = c.dic
          this.filtermenulist['management-user'] = c.user
          this.filtermenulist['management-database'] = c.base
        })
    }
  }
</script>
