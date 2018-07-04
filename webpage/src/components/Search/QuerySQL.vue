<style lang="less">
  @import '../../styles/common.less';
  @import '../Order/components/table.less';

  .tree {
    word-wrap: break-word;
    word-break: break-all;
    overflow-y: scroll;
    overflow-x: scroll;
    height: 600px;
  }
</style>

<template>
  <div>
    <Row>
      <Col span="6">
        <Card>
          <p slot="title">
            <Icon type="ios-redo"></Icon>
            选择数据库
          </p>
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
      <Col span="18" class="padding-left-10">
        <Card>
          <p slot="title">
            <Icon type="ios-crop-strong"></Icon>
            填写sql语句
          </p>
          <editor v-model="formItem.textarea" @init="editorInit"></editor>
          <br>
          <p>当前选择的库: {{put_info.base}}</p>
          <br>
          <Button type="error" icon="trash-a" @click.native="ClearForm()">清除</Button>
          <Button type="info" icon="paintbucket" @click.native="beautify()">美化</Button>
          <Button type="success" icon="ios-redo" @click.native="Search_sql()">查询</Button>
          <Button type="primary" icon="ios-cloud-download" @click.native="exportdata()" v-if="export_data">导出查询数据
          </Button>
          <Button type="error" icon="backspace-outline" @click.native="End_sql()">结束查询</Button>
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
  import flow from './work_flow'
  import ICol from '../../../node_modules/iview/src/components/grid/col.vue'
  import axios from 'axios'
  import util from '../../libs/util'
  import Csv from '../../../node_modules/iview/src/utils/csv'
  import ExportCsv from '../../../node_modules/iview/src/components/table/export-csv'

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
        export_data: false
      }
    },
    methods: {
      choseName (vl) {
        if (vl.expand === true) {
          this.put_info.base = vl.title
        }
      },
      Getbasename (vl) {
        for (let i of this.data1[0].children) {
          for (let c of i.children) {
            if (c.title === vl[0].title && c.nodeKey === vl[0].nodeKey) {
              this.put_info.base = i.title
            }
          }
        }
        axios.put(`${util.url}/search`, {'base': this.put_info.base, 'table': vl[0].title})
          .then(res => {
            if (res.data['error']) {
              util.err_notice(res.data['error'])
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
        let address = {
          'basename': this.put_info.base
        }
        axios.post(`${util.url}/search`, {
          'sql': this.formItem.textarea,
          'address': JSON.stringify(address)
        })
          .then(res => {
            if (res.data['error']) {
              util.err_notice(res.data['error'])
            } else {
              this.columnsName = res.data['title']
              this.allsearchdata = res.data['data']
              this.Testresults = this.allsearchdata.slice(0, 10)
              this.total = res.data['len']
            }
          })
          .catch(error => {
            util.err_notice(error)
          })
      },
      exportdata () {
        exportcsv({
          filename: 'Yearning_Data',
          original: false,
          data: this.allsearchdata,
          columns: this.columnsName
        })
      },
      End_sql () {
        axios.put(`${util.url}/query_worklf`, {'mode': 'end'})
          .then(res => util.notice(res.data))
          .catch(err => util.err_notice(err))
        this.$router.push({
          name: 'serach-sql'
        })
      }
    },
    mounted () {
      axios.put(`${util.url}/query_worklf`, {'mode': 'status'})
        .then(res => {
          if (res.data !== 1) {
            this.$router.push({
              name: 'serach-sql'
            })
          } else {
            axios.put(`${util.url}/query_worklf`, {'mode': 'info'})
              .then(res => {
                this.data1 = JSON.parse(res.data['info'])
                res.data['status'] === 1 ? this.export_data = true : this.export_data = false
              })
          }
        })
    }
  }
</script>
