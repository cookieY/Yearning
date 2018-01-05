<style scoped>
.expand-row {
  margin-bottom: 16px;
}
</style>
<template>
<div>
  <Row class="expand-row">
    <Col span="8">
    <span class="expand-key">字段类型：</span>
    <Input class="expand-value" v-model="row.Type" v-on:on-blur="backdata"></Input>
    </Col>
    <Col span="8">
    <span class="expand-key">默认值：</span>
    <Input class="expand-value" v-model="row.Default"></Input>
    </Col>
    <Col span="8">
    <span class="expand-key">字段可以为空：</span>
    <Select v-model="row.Null">
        <Option value="YES">YES</Option>
        <Option value="NO">NO</Option>
      </Select>
    </Col>
  </Row>
  <Row>
    <Col span="8">
    <span class="expand-key">索引类型：</span>
    <Input class="expand-value" v-model="row.Key" disabled></Input>
    </Col>
    <Col span="8">
    <span class="expand-key">备注：</span>
    <Input class="expand-value" v-model="row.Extra"></Input>
    </Col>
  </Row>
</div>
</template>
<script>
export default {
  props: {
    row: Object
  },
  methods: {
    backdata () {
      this.$emit('backdata')
    }
  }
}
</script>
