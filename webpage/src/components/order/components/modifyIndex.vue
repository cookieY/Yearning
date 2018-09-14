<template>
<div>
  <Table stripe :columns="tabcolumns" :data="tabledata" border ></Table>
  <br/>
  <Table stripe :columns="addcolums" :data="add_row" border></Table>
  <br>
  <p>
    <Icon type="plus-round"></Icon> 添加索引:</p>
  <div>
    <Input v-model="add_tmp.key_name" placeholder="索引名称" style="width: 15%"></Input>
    <Select v-model="add_tmp.Non_unique" placeholder="索引是否唯一" style="width: 15%">
        <Option value="YES" >YES</Option>
        <Option value="NO" >NO</Option>
      </Select>
    <Input v-model="add_tmp.column_name" placeholder="字段名" style="width: 15%"></Input>
    <Select v-model="add_tmp.extra" placeholder="是否为全文索引" style="width: 20%" transfer>
        <Option value="YES" >设置为全文索引</Option>
        <Option value="NO" >不设置为全文索引</Option>
      </Select>
    <Button type="primary" @click.native="addcolumns">  添加</Button>
    <Button type="success" @click.native="confirm2()">生成索引语句</Button>
  </div>
</div>
</template>
<script>
import axios from 'axios'
import util from '../../../libs/util'
export default {
  name: 'editindex',
  props: {
    tabledata: Array,
    table_name: String
  },
  data () {
    return {
      add_tmp: {
        key_name: '',
        Non_unique: '',
        column_name: '',
        extra: 'NO'
      }, // 添加的index信息参数存储
      add_row: [], // 添加Index信息
      tabcolumns: [
        {
          title: '索引名称',
          key: 'key_name'
        },
        {
          title: '是否唯一索引',
          key: 'Non_unique'
        },
        {
          title: '字段名',
          key: 'column_name'
        },
        {
          title: '操作',
          key: 'action',
          width: 150,
          align: 'center',
          render: (h, params) => {
            return h('div', [
              h('Button', {
                props: {
                  size: 'small'
                },
                on: {
                  click: () => {
                    this.remove(params.index)
                  }
                }
              }, '删除')
            ])
          }
        }
      ], // index现有表字段
      addcolums: [{
          title: '索引名称',
          key: 'key_name'
        },
        {
          title: '是否唯一索引',
          key: 'Non_unique'
        },
        {
          title: '字段名',
          key: 'column_name'
        },
        {
          title: '是否为全文索引',
          key: 'fulltext'
        },
        {
          title: 'action',
          width: 80,
          render: (h, params) => {
            return h('Button', {
              props: {
                type: 'text'
              },
              on: {
                click: () => {
                  this.$Notice.error({
                    title: '临时字段删除成功!'
                  })
                  this.add_row.splice(params.index, 1)
                }
              }
            }, '删除')
          }
        }
      ], // index添加表字段
      putdata: [], // 携带最终生成参数
      children: [] // 生成语句
    }
  },
  methods: {
    addcolumns () {
      this.add_row.push({
        'key_name': this.add_tmp.key_name,
        'Non_unique': this.add_tmp.Non_unique,
        'column_name': this.add_tmp.column_name,
        'fulltext': this.add_tmp.extra
      })
      this.add_tmp = {}
      this.add_tmp.extra = 'NO'
    },
    confirm2 () {
      this.putdata.push({
        'addindex': this.add_row,
        'table_name': this.table_name
      })
      axios.put(`${util.url}/gensql/index`, {
          'data': JSON.stringify(this.putdata)
        })
        .then(mm => {
          this.children = mm.data
          this.putdata = []
          this.add_row = []
          this.$emit('on-indexdata', this.children)
        })
        .catch(() => {
          this.$Notice.error({
            title: '警告',
            desc: '服务端无法生成相关语句，请检查语法或查看后台日志信息'
          })
        })
    },
    remove (index) {
      if (this.tabledata[index].key_name !== 'PRIMARY') {
        this.$Notice.success({
          title: `${this.tabledata[index].key_name}-索引删除成功!`
        })
        this.putdata.push({
          'delindex': this.tabledata[index],
          'table_name': this.table_name
        })
        this.tabledata.splice(index, 1)
      } else {
        this.$Notice.error({
          title: '主键不支持删除!'
        })
      }
    }
  }
}
</script>
