<template>
  <div>
    <Row>
      <Card>
        <div>
          <Button type="primary" icon="md-people" @click="createModel">添加权限组</Button>
          <br>
          <br/>
          <Table border :columns="columns" :data="data6" stripe height="550"></Table>
        </div>
        <br>
        <Page :total="pagenumber" show-elevator @on-change="splicpage" :page-size="10" ref="total"></Page>
      </Card>
    </Row>

    <Modal v-model="addAuthGroupModal" :width="800">
      <h3 slot="header" style="color:#2D8CF0">权限组设置</h3>
      <Form :model="addAuthGroupForm" :label-width="120" label-position="right">
        <FormItem label="* 权限组名">
          <Input v-model="addAuthGroupForm.groupname" v-bind:readonly="isReadOnly"></Input>
        </FormItem>
        <template>
          <FormItem label="DDL及索引权限:">
            <RadioGroup v-model="permission.ddl">
              <Radio label="1">是</Radio>
              <Radio label="0">否</Radio>
            </RadioGroup>
          </FormItem>
          <template v-if="permission.ddl === '1'">
            <FormItem label="连接名:">
              <div style="border-bottom: 1px solid #e9e9e9;padding-bottom:6px;margin-bottom:6px;">
                <Checkbox
                  :indeterminate="indeterminate.ddl"
                  :value="checkAll.ddl"
                  @click.prevent.native="ddlCheckAll('ddlcon', 'ddl', 'connection')">全选
                </Checkbox>
              </div>
              <CheckboxGroup v-model="permission.ddlcon">
                <Checkbox v-for="i in connectionList.connection" :label="i.connection_name" :key="i.connection_name">
                  {{i.connection_name}}
                </Checkbox>
              </CheckboxGroup>
            </FormItem>
          </template>
          <hr style="height:1px;border:none;border-top:1px dashed #dddee1;"/>
          <br>
          <FormItem label="DML权限:">
            <RadioGroup v-model="permission.dml">
              <Radio label="1">是</Radio>
              <Radio label="0">否</Radio>
            </RadioGroup>
          </FormItem>
          <template v-if="permission.dml === '1'">
            <FormItem label="连接名:">
              <div style="border-bottom: 1px solid #e9e9e9;padding-bottom:6px;margin-bottom:6px;">
                <Checkbox
                  :indeterminate="indeterminate.dml"
                  :value="checkAll.dml"
                  @click.prevent.native="ddlCheckAll('dmlcon', 'dml', 'connection')">全选
                </Checkbox>
              </div>
              <CheckboxGroup v-model="permission.dmlcon">
                <Checkbox v-for="i in connectionList.connection" :label="i.connection_name" :key="i.connection_name">
                  {{i.connection_name}}
                </Checkbox>
              </CheckboxGroup>
            </FormItem>
          </template>
          <hr style="height:1px;border:none;border-top:1px dashed #dddee1;"/>
          <br>
          <FormItem label="数据查询权限:">
            <RadioGroup v-model="permission.query">
              <Radio label="1">是</Radio>
              <Radio label="0">否</Radio>
            </RadioGroup>
          </FormItem>
          <template v-if="permission.query === '1'">
            <FormItem label="连接名:">
              <div style="border-bottom: 1px solid #e9e9e9;padding-bottom:6px;margin-bottom:6px;">
                <Checkbox
                  :indeterminate="indeterminate.query"
                  :value="checkAll.query"
                  @click.prevent.native="ddlCheckAll('querycon', 'query', 'connection')">全选
                </Checkbox>
              </div>
              <CheckboxGroup v-model="permission.querycon">
                <Checkbox v-for="i in connectionList.connection" :label="i.connection_name" :key="i.connection_name">
                  {{i.connection_name}}
                </Checkbox>
              </CheckboxGroup>
            </FormItem>
          </template>
          <hr style="height:1px;border:none;border-top:1px dashed #dddee1;"/>
          <br>
          <FormItem label="选择上级审核人:">
            <div style="border-bottom: 1px solid #e9e9e9;padding-bottom:6px;margin-bottom:6px;">
              <Checkbox
                :indeterminate="indeterminate.person"
                :value="checkAll.person"
                @click.prevent.native="ddlCheckAll('person', 'person', 'person')">全选
              </Checkbox>
            </div>
            <CheckboxGroup v-model="permission.person">
              <Checkbox v-for="i in connectionList.person" :label="i.username" :key="i.username">{{i.username}}
              </Checkbox>
            </CheckboxGroup>
          </FormItem>
          <hr style="height:1px;border:none;border-top:1px dashed #dddee1;"/>
          <br>
          <FormItem label="数据字典权限:">
            <RadioGroup v-model="permission.dic">
              <Radio label="1">是</Radio>
              <Radio label="0">否</Radio>
            </RadioGroup>
          </FormItem>
          <template v-if="permission.dic === '1'">
            <FormItem label="数据字典修改权限:">
              <RadioGroup v-model="permission.dicedit">
                <Radio label="1">是</Radio>
                <Radio label="0">否</Radio>
              </RadioGroup>
            </FormItem>
            <FormItem label="数据字典导出权限:">
              <RadioGroup v-model="permission.dicexport">
                <Radio label="1">是</Radio>
                <Radio label="0">否</Radio>
              </RadioGroup>
            </FormItem>
            <FormItem label="连接名:">
              <div style="border-bottom: 1px solid #e9e9e9;padding-bottom:6px;margin-bottom:6px;">
                <Checkbox
                  :indeterminate="indeterminate.dic"
                  :value="checkAll.dic"
                  @click.prevent.native="ddlCheckAll('diccon', 'dic', 'dic')">全选
                </Checkbox>
              </div>
              <CheckboxGroup v-model="permission.diccon">
                <Checkbox v-for="i in connectionList.dic" :label="i.Name" :key="i.Name">{{i.Name}}</Checkbox>
              </CheckboxGroup>
            </FormItem>
          </template>
        </template>
        <template>
          <hr style="height:1px;border:none;border-top:1px dashed #dddee1;"/>
          <br>
          <FormItem label="用户管理权限:">
            <RadioGroup v-model="permission.user">
              <Radio label="1">是</Radio>
              <Radio label="0">否</Radio>
            </RadioGroup>
          </FormItem>
          <hr style="height:1px;border:none;border-top:1px dashed #dddee1;"/>
          <br>
          <FormItem label="数据库管理权限:">
            <RadioGroup v-model="permission.base">
              <Radio label="1">是</Radio>
              <Radio label="0">否</Radio>
            </RadioGroup>
          </FormItem>
        </template>
      </Form>
      <div slot="footer">
        <Button type="text" @click="addAuthGroupModal = false">取消</Button>
        <Button type="primary" @click="createAuthGroup" v-if="isAdd">创建</Button>
        <Button type="primary" @click="saveAddGroup" v-else>保存</Button>
      </div>
    </Modal>
  </div>
</template>

<script>
  import axios from 'axios'
  import '../../assets/tablesmargintop.css'
  import util from '../../libs/util'

  const structure = {
    ddl: '0',
    ddlcon: [],
    dml: '0',
    dmlcon: [],
    query: '0',
    querycon: [],
    dic: '0',
    diccon: [],
    dicedit: '0',
    dicexport: '0',
    user: '0',
    base: '0',
    person: []
  }
  export default {
    name: 'auth-group',
    data () {
      return {
        isAdd: true,
        isReadOnly: false,
        pagenumber: 1,
        data6: [],
        columns: [
          {
            title: 'ID',
            key: 'id',
            width: 85,
            sortable: true
          },
          {
            title: '权限组',
            key: 'username',
            sortable: true
          },
          {
            title: '操作',
            key: 'action',
            align: 'center',
            render: (h, params) => {
              return h('div', [
                h('Button', {
                  props: {
                    type: 'info',
                    size: 'small'
                  },
                  style: {
                    marginRight: '5px'
                  },
                  on: {
                    click: () => {
                      this.editAuthGroup(params.row)
                    }
                  }
                }, '查看与编辑'),
                h('Button', {
                  props: {
                    type: 'warning',
                    size: 'small'
                  },
                  style: {
                    marginRight: '5px'
                  },
                  on: {
                    click: () => {
                      this.deleteAuthGroup(params.row)
                    }
                  }
                }, '删除')
              ])
            }
          }
        ],
        permission: structure,
        indeterminate: {
          ddl: true,
          dml: true,
          query: true,
          dic: true,
          person: true
        },
        checkAll: {
          ddl: false,
          dml: false,
          query: false,
          dic: false,
          person: false
        },
        connectionList: {
          connection: [],
          dic: [],
          person: []
        },
        addAuthGroupForm: {
          groupname: ''
        },
        addAuthGroupModal: false
      }
    },
    methods: {
      editAuthGroup (vl) {
        this.addAuthGroupModal = true
        this.isAdd = false
        this.isReadOnly = true
        this.id = vl.id
        this.addAuthGroupForm.groupname = vl.username
        this.permission = vl.permissions
      },
      createModel () {
        this.addAuthGroupModal = true
        this.isReadOnly = false
        this.isAdd = true
        this.permission = structure
      },
      createAuthGroup () {
        for (let i of this.data6) {
          if (this.addAuthGroupForm.groupname === i.username) {
            return util.err_notice('不可创建相同名的权限组！')
          }
        }
        axios.post(`${util.url}/authgroup/`, {
          'groupname': this.addAuthGroupForm.groupname,
          'permission': JSON.stringify(this.permission)
        })
          .then(res => {
            util.notice(res.data)
            this.$refs.total.currentPage = 1
            this.refreshgroup()
          })
          .catch(error => {
            util.err_notice(error)
          })
        this.addAuthGroupModal = false
      },
      saveAddGroup () {
        axios.put(`${util.url}/authgroup/update`, {
          'groupname': this.addAuthGroupForm.groupname,
          'permission': JSON.stringify(this.permission)
        })
          .then(res => {
            util.notice(res.data)
            this.$refs.total.currentPage = 1
            this.refreshgroup()
          })
          .catch(error => {
            util.err_notice(error)
          })
        this.addAuthGroupModal = false
      },
      refreshgroup (vl = 1) {
        axios.get(`${util.url}/authgroup/all?page=${vl}`)
          .then(res => {
            this.data6 = res.data.data
            this.pagenumber = parseInt(res.data.page)
          })
          .catch(error => {
            util.ajanxerrorcode(this, error)
          })
      },
      splicpage (page) {
        this.refreshgroup(page)
      },
      ddlCheckAll (name, indeterminate, ty) {
        if (this.indeterminate[indeterminate]) {
          this.checkAll[indeterminate] = false
        } else {
          this.checkAll[indeterminate] = !this.checkAll[indeterminate]
        }
        this.indeterminate[indeterminate] = false
        if (this.checkAll[indeterminate]) {
          if (ty === 'dic') {
            this.permission[name] = this.connectionList[ty].map(vl => vl.Name)
          } else if (ty === 'person') {
            this.permission[name] = this.connectionList[ty].map(vl => vl.username)
          } else {
            this.permission[name] = this.connectionList[ty].map(vl => vl.connection_name)
          }
        } else {
          this.permission[name] = []
        }
      },
      deleteAuthGroup (vl) {
        axios.delete(`${util.url}/authgroup/${vl.username}`)
          .then(res => {
            util.notice(res.data)
            this.refreshgroup()
          })
          .catch(err => util.err_notice(err))
      }
    },
    mounted () {
      axios.put(`${util.url}/workorder/connection`, {'permissions_type': 'user'})
        .then(res => {
          this.connectionList.connection = res.data['connection']
          this.connectionList.dic = res.data['dic']
          this.connectionList.person = res.data['person']
        })
        .catch(error => {
          util.err_notice(error)
        })
      this.refreshgroup()
    }
  }
</script>

<style scoped>
</style>
