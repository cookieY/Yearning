<style lang="less">
  @import '../../styles/common.less';
  @import '../order/components/table.less';

  .tree {
    word-wrap: break-word;
    word-break: break-all;
    overflow-y: scroll;
    overflow-x: scroll;
    height: 680px;
  }
</style>

<template>
  <div>
    <Row>
      <Col span="4">
        <Card>
          <div>
            <Icon type="ios-search"></Icon>
            <input type="text" placeholder="选择数据" class="ivu-input" style="width:90%" v-model="searchkey" value="searchkey"/>
          </div>
          <div class="edittable-test-con">
            <div id="showImage" class="margin-bottom-10">
              <div class="tree">
                <Tree :data="data1" @on-select-change="Getbasename" @on-toggle-expand="choseName"
                      @empty-text="数据加载中"></Tree>
              </div>
            </div>
          </div>
        </Card>
      </Col>
      <Col span="20" class="padding-left-10">
          <Row>
            <Card>
              <div slot="title">
                <Button type="error" icon="md-trash" @click.native="ClearForm()">清除</Button>
                <Button type="info" icon="md-brush" @click.native="beautify()">美化</Button>
                <Button type="success" icon="ios-redo" @click.native="Search_sql()">查询</Button>
                <Button type="primary" icon="ios-cloud-download" @click.native="exportdata()" v-if="export_data">导出查询数据</Button>
                <span>
                  <b>当前选择的库:</b>
                  <span v-if = "put_info.base">{{put_info.dbcon}} . {{put_info.base}}</span>
                </span>
              </div>
              <Button type="primary" icon="md-add" @click="search_perm()" slot="extra">查询权限</Button>
              <editor v-model="formItem.textarea" @init="editorInit" @setCompletions="setCompletions" value="请输入SQL"></editor>
            </Card>
          </Row>
          <Row>
            <Table :columns="columnsName"
              :data="Testresults"
              highlight-row
              ref="table"
              stripe
              no-data-text="请输入SQL"
              border
              ></Table>
            <Page :total="total" show-total show-sizer @on-change="splice_arr" @on-page-size-change="splice_len"  ref="totol"></Page>
          </Row>
      </Col>
    </Row>
  </div>
</template>
<script>
  import flow from './workFlow'
  import ICol from '../../../node_modules/iview/src/components/grid/col.vue'
  import axios from 'axios'
  import util from '../../libs/util'
  import Csv from '../../../node_modules/iview/src/utils/csv'
  import ExportCsv from '../../../node_modules/iview/src/components/table/export-csv'
  const _ = require('lodash')

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
          tablename: '',
          dbcon: ''
        },
        export_data: false,
        wordList: [],
        searchkey: '',
        splice_length: 10
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
      matchNode (node, vl) {
        return (node.title === vl.title && node.nodeKey === vl.nodeKey)
      },
      choseName (vl) {
        this.put_info.base = ''
        this.put_info.dbcon = ''
        this.put_info.tablename = ''
        // 使用抛出异常快速退出
        try {
          for (let c of this.data1) {
            if (this.matchNode(c, vl)) {
              this.put_info.dbcon = c.title
              throw Error('custom_return')
            }
            for (let i of c.children) {
              if (this.matchNode(i, vl)) {
                this.put_info.dbcon = c.title
                this.put_info.base = i.title
                throw Error('custom_return')
              }
              for (let t of i.children) {
                if (this.matchNode(t, vl)) {
                  this.put_info.base = i.title
                  this.put_info.dbcon = c.title
                  this.put_info.tablename = t.title
                  throw Error('custom_return')
                }
              }
            }
          }
        } catch (e) {
          if (e.message !== 'custom_return') {
            throw e
          }
        }
      },
      Getbasename (vl) {
        if (vl.length !== 0) {
          this.choseName(vl[0])
          if (this.put_info.dbcon !== '' && this.put_info.base !== '' && this.put_info.tablename !== '') {
            axios.put(`${util.url}/search`, {'base': this.put_info.base, 'table': this.put_info.tablename, 'dbcon': this.put_info.dbcon})
            .then(res => {
              if (res.data['error']) {
                util.err_notice(res.data['error'])
              } else {
                this.columnsName = res.data['title']
                this.allsearchdata = res.data['data']
                this.Testresults = this.allsearchdata.slice(0, this.splice_length)
                this.total = res.data['len']
              }
            })
          } else {
            this.columnsName = []
            this.allsearchdata = []
            this.Testresults = []
            this.total = 0
          }
        }
      },
      editorInit: function () {
        require('brace/mode/mysql')
        require('brace/theme/xcode')
      },
      beautify () {
        axios.put(`${util.url}/sqlsyntax/beautify`, {
          'data': this.formItem.textarea
        })
          .then(res => {
            this.formItem.textarea = res.data
          })
          .catch(error => {
            util.err_notice(error)
          })
      },
      splice_arr (page) {
        this.Testresults = this.allsearchdata.slice((page - 1) * this.splice_length, page * this.splice_length)
      },
      splice_len (length) {
        this.splice_length = length
        this.Testresults = this.allsearchdata.slice(0, this.splice_length)
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
          'dbcon': this.put_info.dbcon,
          'basename': this.put_info.base
        }
        if (this.put_info.dbcon && this.put_info.base) {
          axios.post(`${util.url}/search`, {
            'sql': this.formItem.textarea,
            'address': JSON.stringify(address)
          })
            .then(res => {
              if (!res.data['data']) {
                util.err_notice(res.data)
              } else {
                this.columnsName = res.data['title']
                this.allsearchdata = res.data['data']
                this.Testresults = this.allsearchdata.slice(0, 10)
                this.total = res.data['len']
              }
            })
        } else {
          util.err_notice('请选择 数据库!')
        }
        this.$Spin.hide()
      },
      exportdata () {
        exportcsv({
          filename: 'Yearning_Data',
          original: false,
          data: this.allsearchdata,
          columns: this.columnsName
        })
      },
      search_perm () {
        this.$router.push({
          name: 'queryready'
        })
      },
      keyfilter () {
        if (this.searchkey.length !== 0) {
          let tdata = JSON.parse(JSON.stringify(this.data2))
          this.data1 = []
          for (let node of tdata) {
            let tnode = util.filternode(node, this.searchkey)
            tnode && this.data1.push(tnode)
          }
        } else {
          this.data1 = JSON.parse(JSON.stringify(this.data2))
        }
      }
    },
    mounted () {
      axios.put(`${util.url}/query_worklf`, {'mode': 'status'})
        .then(res => {
          if (res.data === 2) {
            this.$router.push({
              name: 'queryready'
            })
          } else if (res.data === 1) {
            axios.put(`${util.url}/query_worklf`, {'mode': 'info'})
              .then(res => {
                this.data1 = JSON.parse(res.data['info'])
                this.data2 = JSON.parse(res.data['info'])
                let tWord = util.highlight.split('|')
                for (let i of tWord) {
                  this.wordList.push({'vl': i, 'meta': '关键字'})
                }
                this.wordList = this.wordList.concat(res.data.highlight)
              })
          } else {
            this.$router.push({
              name: 'serach-perm'
            })
          }
        })
    },
    watch: {
      searchkey: function (newkey, oldkey) {
        this.debouncedFilter()
      }
    },
    created: function () {
      this.debouncedFilter = _.debounce(this.keyfilter, 500)
    }
  }
</script>
