<style lang="less">
@import "../../styles/common.less";
@import "../order/components/table.less";

.tree {
  word-wrap: break-word;
  word-break: break-all;
  overflow-y: scroll;
  overflow-x: scroll;
  min-height: 600px;
}
</style>

<template>
  <div>
    <Row>
      <Col span="6">
        <Card>
          <p slot="title">
            <Icon type="ios-redo"></Icon>选择数据库
          </p>
          <div class="edittable-test-con">
            <div id="showImage" class="margin-bottom-10">
              <div class="tree">
                <Tree
                  :data="data1"
                  @on-select-change="Getbasename"
                  @on-toggle-expand="choseName"
                  @empty-text="数据加载中"
                ></Tree>
              </div>
            </div>
          </div>
        </Card>
      </Col>
      <Col span="18" class="padding-left-10">
        <Card>
          <p slot="title">
            <Icon type="ios-crop-strong"></Icon>填写sql语句
          </p>
          <editor v-model="formItem.textarea" @init="editorInit" @setCompletions="setCompletions"></editor>
          <br>
          <p>当前选择的库: {{put_info.base}}</p>
          <br>
          <Button type="error" icon="md-trash" @click.native="ClearForm()">清除</Button>
          <Button type="info" icon="md-brush" @click.native="beautify()">美化</Button>
          <Button type="success" icon="ios-redo" @click.native="Search_sql()" >查询</Button>
          <Button
            type="primary"
            icon="ios-cloud-download"
            @click.native="exportdata()"
            v-if="export_data"
          >导出查询数据</Button>
          <Button type="error" icon="md-backspace" @click.native="End_sql()">结束会话</Button>
          <br>
          <br>
          <p>查询结果:</p>
          <Table :columns="columnsName" :data="Testresults" highlight-row ref="table"></Table>
          <br>
          <Page :total="total" show-total @on-change="splice_arr" ref="totol"></Page>
        </Card>
      </Col>
    </Row>
  </div>
</template>
<script>
import flow from './workFlow'
import ICol from '../../../node_modules/iview/src/components/grid/col.vue'
import axios from 'axios'

import Csv from '../../../node_modules/iview/src/utils/csv'
import ExportCsv from '../../../node_modules/iview/src/components/table/export-csv'

const concat_ = function (arr1, arr2) {
  let arr = arr1.concat();
  for (let i = 0; i < arr2.length; i++) {
    arr.indexOf(arr2[i]) === -1 ? arr.push(arr2[i]) : 0;
  }
  return arr;
}

const exportcsv = function exportCsv (params) {
  if (params.filename) {
    if (params.filename.indexOf('.csv') === -1) {
      params.filename += '.csv'
    }
  } else {
    params.filename = 'table.csv'
  }

  let columns = []
  let datas = []
  if (params.columns && params.data) {
    columns = params.columns
    datas = params.data
  } else {
    columns = this.columns
    if (!('original' in params)) params.original = true
    datas = params.original ? this.data : this.rebuildData
  }

  let noHeader = false
  if ('noHeader' in params) noHeader = params.noHeader
  const data = Csv(columns, datas, params, noHeader)
  if (params.callback) params.callback(data)
  else ExportCsv.download(params.filename, data)
}
export default {
  components: {
    ICol,
    flow,
    editor: require('../../libs/editor')
  },
  name: 'SearchSQL',
  data () {
    return {
      data1: [],
      validate_gen: true,
      formItem: {
        textarea: ''
      },
      columnsName: [],
      Testresults: [],
      ruleValidate: {
        basename: [{
          required: true,
          message: '数据库名不得为空',
          trigger: 'change'
        }]
      },
      id: null,
      total: 0,
      allsearchdata: [],
      put_info: {
        base: '',
        tablename: ''
      },
      export_data: false,
      wordList: []
    }
  },
  methods: {
    setCompletions (editor, session, pos, prefix, callback) {
      let wordList = []
      wordList = this.wordList
      callback(null, wordList.map(function (word) {
        return {
          caption: word.vl,
          value: word.vl,
          meta: word.meta
        }
      }))
    },
    choseName (vl) {
      this.put_info.base = vl.title
      if (vl.expand === true) {
        this.$Spin.show({
          render: (h) => {
            return h('div', [
              h('Icon', {
                'class': 'demo-spin-icon-load',
                props: {
                  type: 'ios-loading',
                  size: 18
                }
              }),
              h('div', 'Loading')
            ])
          }
        })
        axios.put(`${this.$config.url}/query_worklf`, { 'mode': 'table', 'base': vl.title })
          .then(res => {
            this.wordList = concat_(this.wordList, res.data.highlight)
            for (let i = 0; i < this.data1[0].children.length; i++) {
              if (this.data1[0].children[i].title === vl.title) {
                this.data1[0].children[i].children = res.data.table
              }
            }
            this.$Spin.hide()
          })
          .catch(() => this.$Spin.hide())
      }
    },
    Getbasename (vl) {
      if (vl[0].children) {
        this.put_info.base = vl[0].title
        return
      }
      for (let i of this.data1[0].children) {
        for (let c of i.children) {
          if (c.title === vl[0].title && c.nodeKey === vl[0].nodeKey) {
            this.put_info.base = i.title
          }
        }
      }
      axios.put(`${this.$config.url}/search`, { 'base': this.put_info.base, 'table': vl[0].title })
        .then(res => {
          if (res.data['error']) {
            this.$config.err_notice(this, res.data['error'])
          } else {
            this.columnsName = res.data['title']
            this.allsearchdata = res.data['data']
            this.Testresults = this.allsearchdata.slice(0, 10)
            this.total = res.data['len']
          }
        })
    },
    editorInit: function () {
      require('brace/mode/mysql')
      require('brace/theme/xcode')
    },
    beautify () {
      axios.put(`${this.$config.url}/sqlsyntax/beautify`, {
        'data': this.formItem.textarea
      })
        .then(res => {
          this.formItem.textarea = res.data
        })
        .catch(error => {
          this.$config.err_notice(this, error)
        })
    },
    splice_arr (page) {
      this.Testresults = this.allsearchdata.slice(page * 10 - 10, page * 10)
    },
    ClearForm () {
      this.formItem.textarea = ''
      this.Testresults = []
      this.columnsName = []
      this.$refs.totol.currentPage = 1
      this.total = 0
    },
    Search_sql () {
      this.$Spin.show({
        render: (h) => {
          return h('div', [
            h('Icon', {
              props: {
                size: 30,
                type: 'ios-loading'
              },
              style: {
                animation: 'ani-demo-spin 1s linear infinite'
              }
            }),
            h('div', '正在查询,请稍后........')
          ])
        }
      })
      let address = {
        'basename': this.put_info.base
      }
      axios.post(`${this.$config.url}/search`, {
        'sql': this.formItem.textarea,
        'address': JSON.stringify(address)
      })
        .then(res => {
          if (!res.data['data']) {
            this.$config.err_notice(this, res.data)
          } else {
            this.columnsName = res.data['title']
            this.allsearchdata = res.data['data']
            this.Testresults = this.allsearchdata.slice(0, 10)
            this.total = res.data['len']
          }
          this.$Spin.hide()
        })
        .catch(err => {
          this.$config.err_notice(this, err)
          this.$Spin.hide()
        })
    },
    exportdata () {
      exportcsv({
        filename: 'Yearning_Data',
        original: true,
        data: this.allsearchdata,
        columns: this.columnsName
      })
    },
    End_sql () {
      axios.put(`${this.$config.url}/query_worklf`, { 'mode': 'end', 'username': sessionStorage.getItem('user') })
        .then(res => this.$config.notice(res.data))
        .catch(err => this.$config.err_notice(this, err))
      this.$router.push({
        name: 'serach-sql'
      })
    }
  },
  mounted () {
    axios.put(`${this.$config.url}/query_worklf`, { 'mode': 'status' })
      .then(res => {
        if (res.data !== 1) {
          this.$router.push({
            name: 'serach-sql'
          })
        } else {
          axios.put(`${this.$config.url}/query_worklf`, { 'mode': 'info' })
            .then(res => {
              this.data1 = JSON.parse(res.data['info'])
              let tWord = this.$config.highlight.split('|')
              for (let i of tWord) {
                this.wordList.push({ 'vl': i, 'meta': '关键字' })
              }
              this.wordList = this.wordList.concat(res.data.highlight)
              res.data['status'] === 1 ? this.export_data = true : this.export_data = false
            })
        }
      })
      .catch(err => this.$config.err_notice(this, err))
  }
}
</script>
