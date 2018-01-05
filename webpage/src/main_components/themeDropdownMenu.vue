<template>
<Dropdown trigger="click" @on-click="setTheme">
  <a href="javascript:void(0)">
    <Icon :style="{marginTop: '-2px', verticalAlign: 'middle'}" color="#495060" :size="18" type="paintbucket"></Icon>
    <Icon type="arrow-down-b"></Icon>
  </a>
  <DropdownMenu slot="list">
    <DropdownItem v-for="(item, index) in themeList" :key="index" :name="item.name">
      <Row type="flex" justify="center" align="middle">
        <span><Icon :size="22" type="record" :color="item.element"/></span>
      </Row>
    </DropdownItem>
  </DropdownMenu>
</Dropdown>
</template>

<script>
export default {
  name: 'themeDropdownMenu',
  data () {
    return {
      themeList: [{
          name: 'black',
          element: '#495060'
        },
        {
          name: 'light',
          element: '#d4cfd6'
        }
      ]
    };
  },
  methods: {
    setTheme (themeFile) {
      if (themeFile === 'black') {
        // 黑色菜单
        this.$store.commit('changeMenuTheme', 'dark');
      } else {
        this.$store.commit('changeMenuTheme', 'light');
      }
    }
  }
};
</script>
