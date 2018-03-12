<style lang="less">
.word {
    font-size: 14px;
    width: 100%;
    word-wrap: break-word;
    word-break: break-all;
    height: 500px;
    overflow: auto;
}
a:hover {
    color: #ff9900;
}
a:active {
    color: #ff6600;
}
.edittable-self-con {
    height: 100%;
}
@import '../../styles/common.less';
@import '../Order/components/table.less';
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
      <div class="edittable-self-con">
        <Form :label-width="80">
          <Form-item label="数据库连接:">
            <Select v-model="formItem.namedata" @on-change="InitializationTableInfo" filterable>
                <Option v-for="i in TableList" :value="i" :key="i">{{ i }}</Option>
              </Select>
          </Form-item>
          <Form-item label="数据库:">
            <Select v-model="formItem.select" @on-change="ShowTableInfo" filterable>
                <Option v-for="item in formItem.info" :value="item" :key="item">{{ item }}</Option>
              </Select>
          </Form-item>
          <Form-item label="搜索数据表:">
            <Select v-model="formItem.search" @on-change="OnlyTabkleInfo" filterable>
                <Option v-for="i in Limitpage" :value="i[0].TableName" :key="i[0].TableName">{{ i[0].TableName }}</Option>
              </Select>
          </Form-item>
          <Form-item label="操作:">
            <Button @click="ResetData" type="warning">刷新</Button>
            <Button @click="ExportData.off=true" type="info">导出</Button>
            <Button @click="AddtableInfo" type="success">添加</Button>
          </Form-item>

        </Form>
        <div class="word">
          <div v-for="i in this.TmpData" style="margin-top: 3%">
            <Icon type="location"></Icon>
            <a @click="OnlyTabkleInfotwo(i.table.TableName)">{{i.table.TableName}}</a>
            <br>
            <span style="color: #ff6600;font-size: 12px">{{i.comment[0].TableComment}}</span>
          </div>
          <br/>
          <Page :current="1" :total="PageNumber" simple style="margin-left: 10%" ref="Limit" :page-size="10" @on-change="spliceArrTwo"></Page>
        </div>
      </div>
    </Card>
    </Col>
    <Col span="18" class="padding-left-10">
    <Card>
      <p slot="title">
        <Icon type="android-remove"></Icon>
        表结构详情
      </p>
      <div class="edittable-table-height-con" style="height: 650px;overflow: auto ">
        <div style="width: 98%;margin-left: 1%;margin-top: 2%;" v-for="i in this.formItem.data ">
          <Icon type="information-circled"></Icon>
          <span>{{ i[0].TableName }}</span>
          <span style="color: #ff6600;margin-left: 1%">{{i[0].TableComment}}</span>
          <a style="margin-left: 2%" @click="EdiTtableInfo(i)">修改表备注</a>
          <Poptip confirm title="您确认删除这条内容吗？" @on-ok="Deltabledata(i)" style="margin-left: 2%">
            <a>删除表</a>
          </Poptip>
          <Table :columns="columnsInfo" :data="i" size="small" border stripe></Table>
        </div>
      </div>
      <Page :total="PageNumber" style="margin-left: 1%;margin-top: 1%" :page-size="3" @on-change="spliceArr" ref="totol"></Page>
    </Card>
    </Col>
  </Row>
  <Modal v-model="EditTableinfo.Onoff" width="360">
    <p slot="header" style="color:#5cadff;text-align:center">
      <Icon type="information-circled"></Icon>
      <span>修改表备注信息</span>
    </p>
    <div style="text-align:center">
      <Input v-model="EditTableinfo.comment" placeholder="该字段暂时没有备注信息"></Input>
    </div>
    <div slot="footer">
      <Button type="warning" size="large" @click="EditTableinfo.Onoff=false">取消</Button>
      <Button type="success" size="large" @click="EditCoreTable(EditTableinfo.id)">修改</Button>
    </div>
  </Modal>

  <Modal v-model="EditTableinfo.offon" width="360">
    <p slot="header" style="color:#5cadff;text-align:center">
      <Icon type="information-circled"></Icon>
      <span>修改字段备注信息</span>
    </p>
    <div style="text-align:center">
      <Input v-model="EditTableinfo.felidcomment" placeholder="该字段暂时没有备注信息"></Input>
    </div>
    <div slot="footer">
      <Button type="warning" size="large" @click="EditTableinfo.offon=false">取消</Button>
      <Button type="success" size="large" @click="EditFieldCore(EditTableinfo.id)">修改</Button>
    </div>
  </Modal>

  <Modal v-model="ExportData.off" width="360">
    <p slot="header" style="color:#5cadff;text-align:center">
      <Icon type="information-circled"></Icon>
      <span>导出数据字典</span>
    </p>
    <Form>
      <FormItem>
        <Checkbox :indeterminate="ExportData.indeterminate" :value="ExportData.checkAll" @click.prevent.native="handleCheckAll">全选</Checkbox>
        <CheckboxGroup v-model="ExportData.checkbox">
          <Checkbox v-for="i in Limitpage" :label="i[0].TableName" :key="i[0].TableName"></Checkbox>
        </CheckboxGroup>
      </FormItem>
    </Form>
    <div slot="footer">
      <Button type="warning" size="large" @click="ExportData.off=false">取消</Button>
      <Button type="success" size="large" @click="ExportDocx">生成导出数据</Button>
      <a v-if="this.ExportData.urloff" :href="ExportData.url">点击下载数据文档</a>
    </div>
  </Modal>

  <Modal v-model="AddTable.open" width="700" @on-ok="handleSubmit('formDynamic')" ok-text="提交">
    <p slot="header" style="color:#5cadff;text-align:center">
      <Icon type="information-circled"></Icon>
      <span>添加数据表/字段</span>
    </p>
    <Form ref="formDynamic" :model="formDynamic" :label-width="80" style="width: 650px">
      <FormItem
        :rules="{required: true, message: '请填写表名!', trigger: 'blur'}"
        prop="tablename"
        label="表名"
        >
        <Input type="text" v-model="formDynamic.tablename" placeholder="请输入表名" style="width: 20%"></Input>
        <Input type="text" v-model="formDynamic.tablecomment" placeholder="请输入表备注" style="width: 20%"></Input>
      </FormItem>
      <FormItem
        v-for="(item, index) in formDynamic.items"
        :key="index"
        :label="'字段 ' + item.index"
        :prop="'items.' + index + '.value'"
        :rules="{required: true, message: '字段 ' + item.index +' 不可为空', trigger: 'blur'}">
        <Row>
          <Col span="7">
          <Input type="text" v-model="item.value" placeholder="请输入字段名"></Input>
          </Col>
          <Col span="7">
          <Select type="text" v-model="item.type" placeholder="请输入字段类型">
            <Option v-for="i in optionData" :key="i" :value="i"> {{i}}</Option>
          </Select>
          </Col>
          <Col span="5">
          <Input type="text" v-model="item.extra" placeholder="请输入字段备注"></Input>
          </Col>
          <Col span="4" offset="1">
          <Button type="ghost" @click="handleRemove(index)">删除</Button>
          </Col>
        </Row>
      </FormItem>
      <FormItem>
        <Row>
          <Col span="12">
          <Button type="dashed" long @click="handleAdd" icon="plus-round">添加字段</Button>
          </Col>
        </Row>
      </FormItem>
      <FormItem>
        <Button type="ghost" @click="handleReset('formDynamic')" style="margin-left: 8px">重置</Button>
      </FormItem>
    </Form>
  </Modal>

</div>
</template>
<script>
import ICol from '../../../node_modules/iview/src/components/grid/col.vue'
import axios from 'axios'
import util from '../../libs/util'
export default {
  components: {
    ICol
  },
  name: 'DataBaseDic',
  data () {
    return {
      AddTable: {
        open: false
      },
      index: 1,
      formDynamic: {
        items: [
          {
            value: '',
            index: 1,
            type: '',
            extra: ''
          }
        ],
        tablename: '',
        tablecomment: ''
      },
      optionData: [
        'varchar',
        'int',
        'char',
        'tinytext',
        'text',
        'mediumtext',
        'longtext',
        'blob',
        'mediumblob',
        'longblob',
        'tinyint',
        'smallint',
        'mediumint',
        'bigint',
        'time',
        'year',
        'date',
        'datetime',
        'timestamp',
        'decimal',
        'float',
        'double',
        'jason'
      ],
      formItem: {
        info: [],
        data: [],
        select: '',
        namedata: ''
      },
      columnsInfo: [
        {
          title: '字段名',
          key: 'Field'
        },
        {
          title: '类型',
          key: 'Type'
        },
        {
          title: '备注',
          key: 'Extra'
        },
        {
          title: '操作',
          key: 'action',
          align: 'center',
          render: (h, params) => {
            return h('div', [
              h('a', {
                props: {
                  size: 'small'
                },
                on: {
                  click: () => {
                    this.EditField(params.row, params.index)
                  }
                }
              }, '更改字段备注'),
              h('Poptip', {
                props: {
                  confirm: true,
                  transfer: true,
                  title: '您确认删除这条内容吗?'
                },
                style: {
                  marginLeft: '5%'
                },
                on: {
                  'on-ok': () => {
                    let data = {
                      'name': this.formItem.namedata,
                      'basename': params.row.BaseName,
                      'tablename': params.row.TableName,
                      'field': params.row.Field
                    }
                    let auth = ''
                    axios.post(`${util.url}/auth_twice`, {
                      'permissions_type': 'dic'
                    })
                      .then(res => {
                        auth = res.data
                        if (auth === '1') {
                          axios.put(`${util.url}/adminsql/delfield`, {
                            'data': JSON.stringify(data)
                          })
                            .then(res => {
                              this.$Notice.success({
                                title: '通知',
                                desc: res.data
                              })
                              this.ResetData()
                            })
                            .catch(error => {
                              util.ajanxerrorcode(this, error)
                            })
                        } else {
                          this.$Notice.error({
                            title: '警告:',
                            desc: '账号权限不足，无法提供修改功能！'
                          })
                        }
                      })
                  }
                }
              }, [
                h('a', '删除字段')
              ])
            ])
          }
        }
      ],
      PageNumber: null,
      TmpData: [],
      EditTableinfo: {
        Onoff: false,
        offon: false,
        comment: '',
        id: '1',
        singleid: '0'
      },
      Limitpage: [],
      TableList: [],
      ExportData: {
        off: false,
        indeterminate: true,
        checkAll: false,
        checkbox: [],
        url: '',
        urloff: false
      }
    }
  },
  methods: {
    AddtableInfo () {
      axios.post(`${util.url}/auth_twice`, {
        'permissions_type': 'dic'
      })
        .then(res => {
          if (res.data === '1') {
            this.AddTable.open = true
          } else {
            this.$Notice.error({
              title: '警告:',
              desc: '账号权限不足，无法提供修改功能！'
            })
          }
        })
    },
    handleReset (name) {
      this.$refs[name].resetFields();
    },
    handleRemove (index) {
      this.formDynamic.items.splice(index, 1)
    },
    handleSubmit (name) {
      this.$refs[name].validate((valid) => {
        if (valid) {
          axios.put(`${util.url}/adminsql/addtable`, {
            'tablename': this.formDynamic.tablename,
            'basename': this.formItem.select,
            'name': this.formItem.namedata,
            'text': JSON.stringify(this.formDynamic.items),
            'tablecomment': this.formDynamic.tablecomment
          })
            .then(res => {
              this.$Notice.success({
                title: '通知:',
                desc: res.data
              })
            })
            .catch(error => {
              util.ajanxerrorcode(this, error)
            })
        } else {
          this.$Message.error('请填下相关必填项之后再提交!');
        }
      })
    },
    handleAdd () {
      this.index++;
      this.formDynamic.items.push({
        value: '',
        index: this.index
      });
    },
    ExportDocx () {
      this.$Spin.show({
        render: (h) => {
          return h('div', [
            h('Icon', {
              'class': 'demo-spin-icon-load',
              props: {
                type: 'load-c',
                size: 30
              }
            }),
            h('div', '导出url正在生成,请稍后........')
          ])
        }
      });
      axios.post(`${util.url}/exportdocx/`, {
          'data': JSON.stringify(this.ExportData.checkbox),
          'connection_name': this.formItem.namedata,
          'basename': this.formItem.select,
          'permissions_type': 'dic'
        })
        .then(res => {
          this.ExportData.urloff = true
          this.$Notice.success({
            title: '通知',
            desc: res.data.status
          })
          if (res.data.url === '') {
            this.ExportData.urloff = false
          } else {
            this.ExportData.url = `${util.url}/download/?url=${res.data.url}`
          }
          this.$Spin.hide();
        })
        .catch(error => {
          util.ajanxerrorcode(this, error)
          this.$Spin.hide();
        })
    },
    handleCheckAll () {
      if (this.ExportData.indeterminate) {
        this.ExportData.checkAll = false;
      } else {
        this.ExportData.checkAll = !this.ExportData.checkAll;
      }
      this.ExportData.indeterminate = false;

      if (this.ExportData.checkAll) {
        for (let i of this.Limitpage) {
          this.ExportData.checkbox.push(i[0].TableName)
        }
      } else {
        this.ExportData.checkbox = [];
      }
    },
    // 获取表结构数据，
    // Limitpage获取完整数据信息.
    // TmpData当前数据表列表的10条分页记录.
    // PageNumber数据总长度，用于获得页面数
    // formItem.data表结构数据当前3条分页记录
    ShowTableInfo () {
      if (this.formItem.select.length === 0) {}
      this.$Spin.show({
        render: (h) => {
          return h('div', [
            h('Icon', {
              'class': 'demo-spin-icon-load',
              props: {
                type: 'load-c',
                size: 30
              }
            }),
            h('div', '数据库字典正在读取中,请稍后........')
          ])
        }
      });
      axios.put(`${util.url}/sqldic/info`, {
          'basename': this.formItem.select,
          'name': this.formItem.namedata,
          'hello': '1',
          'tablelist': '1'
        })
        .then(res => {
          this.$refs.totol.currentPage = 1
          this.$refs.Limit.currentPage = 1
          this.Limitpage = res.data.all
          this.TmpData = res.data.tablelist
          this.PageNumber = res.data.tablepage
          this.formItem.data = res.data.dic
          this.$Spin.hide()
        })
        .catch(error => {
          this.$Notice.error({
            title: error
          })
        })
    },
    // 表结构数据分页处理
    spliceArr (c) {
      this.EditTableinfo.id = c
      axios.put(`${util.url}/sqldic/datalist`, {
          'basename': this.formItem.select,
          'name': this.formItem.namedata,
          'hello': c
        })
        .then(res => {
          this.formItem.data = res.data
        })
        .catch(() => {
          this.$Notice.error({
            title: '警告',
            desc: '分页获取失败!'
          })
        })
      this.EditTableinfo.singleid = '0'
    },
    // 数据表列表分页处理
    spliceArrTwo (c) {
      axios.put(`${util.url}/sqldic/tablelist`, {
          'basename': this.formItem.select,
          'name': this.formItem.namedata,
          'tablelist': c
        })
        .then(res => {
          this.TmpData = res.data
        })
        .catch(() => {
          this.$Notice.error({
            title: '警告',
            desc: '分页获取失败!'
          })
        })
    },
    // 获得点击表名后获得的单表数据
    OnlyTabkleInfo (c) {
      if (this.formItem.select.length === 0) {} else {
        this.$refs.totol.currentPage = 1
        axios.put(`${util.url}/sqldic/single`, {
            'basename': this.formItem.select,
            'name': this.formItem.namedata,
            'tablename': c
          })
          .then(res => {
            this.formItem.data = res.data
            this.EditTableinfo.singleid = '1'
          })
          .catch(() => {
            this.$Notice.error({
              title: '警告',
              desc: '表单数据获取失败!'
            })
          })
      }
    },
    OnlyTabkleInfotwo (c) {
      this.$refs.totol.currentPage = 1
      axios.put(`${util.url}/sqldic/single`, {
          'basename': this.formItem.select,
          'name': this.formItem.namedata,
          'tablename': c
        })
        .then(res => {
          this.formItem.data = res.data
          this.EditTableinfo.singleid = '1'
        })
        .catch(() => {
          this.$Notice.error({
            title: '警告',
            desc: '表单数据获取失败!'
          })
        })
    },
    // 重置按钮
    ResetData () {
      this.$refs.totol.currentPage = 1
      this.$refs.Limit.currentPage = 1
      this.ShowTableInfo()
      this.EditTableinfo.singleid = '0'
    },
    // 表备注model
    EdiTtableInfo (c) {
      let auth = ''
      axios.post(`${util.url}/auth_twice`, {
          'permissions_type': 'dic'
        })
        .then(res => {
          auth = res.data
          if (auth === '1') {
            this.EditTableinfo.Onoff = true
            this.EditTableinfo.comment = c[0].TableComment
            this.EditTableinfo.basename = c[0].BaseName
            this.EditTableinfo.tablename = c[0].TableName
          } else {
            this.$Notice.error({
              title: '警告:',
              desc: '账号权限不足，无法提供修改功能！'
            })
          }
        })
    },
    // 删除表
    Deltabledata (c) {
      let auth = ''
      axios.post(`${util.url}/auth_twice`, {
          'permissions_type': 'dic'
        })
        .then(res => {
          auth = res.data
          if (auth === '1') {
            axios.put(`${util.url}/adminsql/deltable`, {
                'basename': c[0].BaseName,
                'tablename': c[0].TableName,
                'ConnectionName': this.formItem.namedata
              })
              .then(() => {
                this.$Notice.success({
                  title: '通知',
                  desc: `${c[0].TableName}表删除成功!`
                })
                this.ShowTableInfo()
              })
              .catch(error => {
                util.ajanxerrorcode(this, error)
              })
          } else {
            this.$Notice.error({
              title: '警告:',
              desc: '账号权限不足，无法提供删除功能！'
            })
          }
        })
    },
    // 表备注model提交
    EditCoreTable () {
      axios.put(`${util.url}/adminsql/edittableinfo`, {
          'tablename': this.EditTableinfo.tablename,
          'basename': this.EditTableinfo.basename,
          'comment': this.EditTableinfo.comment,
          'name': this.formItem.namedata,
          'hello': this.EditTableinfo.id,
          'singleid': this.EditTableinfo.singleid
        })
        .then(res => {
          this.$Notice.success({
            title: '提示',
            desc: `${this.EditTableinfo.tablename}表备注修改成功`
          })
          this.formItem.data = res.data
        })
        .catch(error => {
          this.$Notice.error({
            title: error
          })
        })
      this.EditTableinfo.Onoff = false
    },
    // 字段备注model
    EditField (row) {
      let auth = ''
      axios.post(`${util.url}/auth_twice`, {
          'permissions_type': 'dic'
        })
        .then(res => {
          auth = res.data
          if (auth === '1') {
            this.EditTableinfo.offon = true
            this.EditTableinfo.felid = row.Field
            this.EditTableinfo.felidcomment = row.Extra
            this.EditTableinfo.tableName = row.TableName
            this.EditTableinfo.baseName = row.BaseName
          } else {
            this.$Notice.error({
              title: '警告:',
              desc: '账号权限不足，无法提供修改功能！'
            })
          }
        })
    },
    // 字段备注model提交
    EditFieldCore () {
      axios.put(`${util.url}/adminsql/editfelid`, {
          'tablename': this.EditTableinfo.tableName,
          'basename': this.EditTableinfo.baseName,
          'comment': this.EditTableinfo.felidcomment,
          'felid': this.EditTableinfo.felid,
          'name': this.formItem.namedata,
          'hello': this.EditTableinfo.id,
          'singleid': this.EditTableinfo.singleid
        })
        .then(res => {
          this.$Notice.success({
            title: '提示',
            desc: `${this.EditTableinfo.tableName}字段更新成功`
          })
          this.formItem.data = res.data
        })
        .catch(error => {
          this.$Notice.error({
            title: error
          })
        })
      this.EditTableinfo.offon = false
    },
    // 初始化加载数据库表名列表
    InitializationTableInfo (val) {
      if (this.formItem.namedata.length === 0) {
        return
      }
      axios.post(`${util.url}/sqldic/`, {
          'name': val
        })
        .then(res => {
          this.formItem.info = res.data.map(item => item.BaseName)
        })
        .catch(error => {
          this.$Notice.error({
            title: '警告',
            desc: error
          })
        })
    }
  },
  mounted () {
    axios.get(`${util.url}/sqldic/all?permissions_type=dic`)
      .then(res => {
        this.TableList = res.data
      })
      .catch(error => {
       util.ajanxerrorcode(this, error)
      })
  }
}
</script>
