<style scoped>
.expand-row {
  margin-bottom: 16px;
}
</style>
<template>
<div>
  <Row class="expand-row">
    <Col span="8">
    <span class="expand-key">是否唯一索引：</span>
    <Input class="expand-value" v-model="row.Non_unique" readonly="readonly"></Input>
    </Col>
    <Col span="8">
    <span class="expand-key">字段名：</span>
    <Input class="expand-value" v-model="row.column_name" readonly="readonly"></Input>
    </Col>
    <Col span="8">
    <span class="expand-key">索引方法：</span>
    <Input class="expand-value" v-model="row.index_type" readonly="readonly"></Input>
    </Col>
  </Row>
</div>
</template>
<script>
export default {
  props: {
    row: Object
  }
}
</script>
