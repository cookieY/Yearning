<style lang="less">
  @import '../../styles/common.less';
  @import '../Order/components/table.less';
</style>

<template>
  <div>
    <Row>
      <Col span="4">
      <Card>
        <p slot="title">
          <Icon type="ios-redo"></Icon>
          选择数据库
        </p>
        <div class="edittable-test-con">
          <div id="showImage" class="margin-bottom-10">

            <Form ref="formItem" :model="formItem" :rules="ruleValidate" :label-width="80">
              <FormItem label="机房:" prop="computer_room">
                <Select v-model="formItem.computer_room" @on-change="Connection_Name">
                  <Option v-for="i in datalist.computer_roomlist" :key="i" :value="i" >{{i}}</Option>
                </Select>
              </FormItem>

              <FormItem label="连接名:" prop="connection_name">
                <Select v-model="formItem.connection_name" @on-change="DataBaseName" filterable>
                  <Option v-for="i in datalist.connection_name_list" :value="i.connection_name" :key="i.connection_name">{{ i.connection_name }}</Option>
                </Select>
              </FormItem>

              <FormItem label="库名:" prop="basename">
                <Select v-model="formItem.basename" filterable>
                  <Option v-for="item in datalist.basenamelist" :value="item" :key="item">{{ item }}</Option>
                </Select>
              </FormItem>
            </Form>
            <Alert style="height: 145px">
              SQL查询注意事项:
              <template slot="desc">
                <p>1.选择对应的数据库</p>
                <p>2.输入相应select语句</p>
                <p>注意:只支持select语句,其他语句统统不可达!</p>
              </template>
            </Alert>
          </div>
        </div>
      </Card>
      </Col>
      <Col span="20" class="padding-left-10">
      <Card>
        <p slot="title">
          <Icon type="ios-crop-strong"></Icon>
          填写sql语句
        </p>
        <editor v-model="formItem.textarea" @init="editorInit"></editor>
        <br>
        <br>
        <Button type="error" icon="trash-a" @click.native="ClearForm()">清除</Button>
        <Button type="info" icon="paintbucket" @click.native="beautify()">美化</Button>
        <Button type="success" icon="ios-redo" @click.native="Search_sql()">查询</Button>
        <Button type="primary" icon="ios-cloud-download" @click.native="exportdata()">导出查询数据</Button>
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
  import ICol from '../../../node_modules/iview/src/components/grid/col.vue'
  import axios from 'axios'
  import util from '../../libs/util'
  import Csv from '../../../node_modules/iview/src/utils/csv'
  import ExportCsv from '../../../node_modules/iview/src/components/table/export-csv';
  const exportcsv = function exportCsv (params) {
    if (params.filename) {
      if (params.filename.indexOf('.csv') === -1) {
        params.filename += '.csv';
      }
    } else {
      params.filename = 'table.csv';
    }

    let columns = [];
    let datas = [];
    if (params.columns && params.data) {
      columns = params.columns;
      datas = params.data;
    } else {
      columns = this.columns;
      if (!('original' in params)) params.original = true;
      datas = params.original ? this.data : this.rebuildData;
    }

    let noHeader = false;
    if ('noHeader' in params) noHeader = params.noHeader;
    const data = Csv(columns, datas, params, noHeader);
    if (params.callback) params.callback(data);
    else ExportCsv.download(params.filename, data);
  }
  export default {
  components: {
      ICol,
      editor: require('../../libs/editor')
    },
    name: 'SearchSQL',
    data () {
      return {
        validate_gen: true,
        formItem: {
          textarea: '',
          computer_room: '',
          connection_name: '',
          basename: '',
          text: '',
          backup: 0
        },
        columnsName: [
        ],
        Testresults: [],
        item: {},
        datalist: {
          connection_name_list: [],
          basenamelist: [],
          sqllist: [],
          computer_roomlist: util.computer_room
        },
        ruleValidate: {
          computer_room: [{
            required: true,
            message: '机房地址不得为空',
            trigger: 'change'
          }],
          connection_name: [{
            required: true,
            message: '连接名不得为空',
            trigger: 'change'
          }],
          basename: [{
            required: true,
            message: '数据库名不得为空',
            trigger: 'change'
          }]
        },
        id: null,
        total: 0,
        allsearchdata: []
      }
    },
    methods: {
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
            this.$Notice.error({
              title: '警告',
              desc: error
            })
          })
      },
      splice_arr (page) {
        this.Testresults = this.allsearchdata.slice(page * 10 - 10, page * 10)
      },
      Connection_Name (val) {
        this.datalist.connection_name_list = []
        this.datalist.basenamelist = []
        this.formItem.connection_name = ''
        this.formItem.basename = ''
        if (val) {
          this.ScreenConnection(val)
        }
      },
      ScreenConnection (val) {
        this.datalist.connection_name_list = this.item.filter(item => {
          if (item.computer_room === val) {
            return item
          }
        })
      },
      DataBaseName (index) {
        if (index) {
          this.id = this.item.filter(item => {
            if (item.connection_name === index) {
              return item
            }
          })
          axios.put(`${util.url}/workorder/basename`, {
            'id': this.id[0].id
          })
            .then(res => {
              this.datalist.basenamelist = res.data
            })
            .catch(() => {
              this.$Notice.error({
                title: '警告',
                desc: '无法连接数据库!请检查网络'
              })
            })
        }
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
          'id': this.id[0].id,
          'basename': this.formItem.basename
        }
        axios.post(`${util.url}/search`, {
          'sql': this.formItem.textarea,
          'address': JSON.stringify(address)
        })
          .then(res => {
            if (res.data['error']) {
              this.$Notice.error({
                title: '错误',
                desc: res.data['error']
              })
            } else {
              this.columnsName = res.data['title']
              this.allsearchdata = res.data['data']
              this.Testresults = this.allsearchdata.slice(0, 10)
              this.total = res.data['len']
            }
          })
          .catch(error => {
            util.ajanxerrorcode(this, error)
          })
      },
      exportdata () {
        exportcsv({
          filename: 'Yearning_Data',
          original: false,
          data: this.allsearchdata,
          columns: this.columnsName
        })
      }
    },
    mounted () {
      axios.put(`${util.url}/workorder/connection`, {'permissions_type': 'query'})
        .then(res => {
          this.item = res.data['connection']
        })
        .catch(error => {
          util.ajanxerrorcode(this, error)
        })
    }
  }
</script>
