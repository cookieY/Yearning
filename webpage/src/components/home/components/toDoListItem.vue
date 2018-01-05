<style lang="less">
@import './styles/to-do-list-item.less';
</style>

<template>
<Row class="to-do-list-item">
  <Col span="2" class="height-100">
  <Row type="flex" justify="center" align="middle" class="height-100">
    <Checkbox v-model="todoitem" @on-change="handleHasDid(content)"></Checkbox>
  </Row>
  </Col>
  <Col span="22" class="height-100">
  <Row type="flex" justify="start" align="middle" class="height-100">
    <p class="to-do-list-item-text" @click="handleHasDid(content)" :class="{hasDid: todoitem}">{{ content }}</p>
  </Row>
  </Col>
</Row>
</template>

<script>
export default {
  name: 'toDoListItem',
  data () {
    return {
      todoitem: false
    };
  },
  props: {
    content: String
  },
  methods: {
    handleHasDid (val) {
      let vm = this
      this.todoitem = true
      setTimeout(function () {
        vm.$emit('deltodo', val)
      }, 2000)
    }
  }
};
</script>
