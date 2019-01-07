<template>
  <div>
    <Row>
      <Card>
        <p slot="title">
          <Icon type="md-person"></Icon>
          权限审核
        </p>
        <Row>
          <Col span="24">
            <Poptip
              confirm
              title="您确认删除这些工单信息吗?"
              @on-ok="delrecordData"
            >
              <Button type="text" style="margin-left: -1%">删除记录</Button>
            </Poptip>
            <Table border :columns="permissoncolums" :data="permissondata" stripe ref="selection"
                   @on-selection-change="delrecordList"></Table>
            <br>
            <Page :total="per_pn" show-elevator @on-change="permisson_list" :page-size="20" ref="perpage"></Page>
          </Col>
        </Row>
      </Card>
    </Row>

    <Modal v-model="editInfodModal" :width="800">
      <h3 slot="header" style="color:#2D8CF0">权限申请单</h3>
      <Form :label-width="120" label-position="right">
        <FormItem label="权限组:">
          <p>{{perList.auth_group}}</p>
        </FormItem>
        <template>
          <FormItem label="DDL及索引权限:">
            <p v-if="perList.permissions.ddl === '0'">否</p>
            <p v-else>是</p>
          </FormItem>
          <template v-if="perList.permissions.ddl !== '0'">
            <FormItem label="连接名:">
              <Tag color="blue" v-for="i in perList.permissions.ddlcon" :key="i">{{i}}</Tag>
            </FormItem>
          </template>
          <hr style="height:1px;border:none;border-top:1px dashed #dddee1;"/>
          <br>
          <FormItem label="DML权限:">
            <p v-if="perList.permissions.dml === '0'">否</p>
            <p v-else>是</p>
          </FormItem>
          <template v-if="perList.permissions.dml === '1'">
            <FormItem label="连接名:">
              <Tag color="blue" v-for="i in perList.permissions.dmlcon" :key="i">{{i}}</Tag>
            </FormItem>
          </template>
          <hr style="height:1px;border:none;border-top:1px dashed #dddee1;"/>
          <br>
          <FormItem label="上级审核人范围:">
            <Tag color="blue" v-for="i in perList.permissions.person" :key="i">{{i}}</Tag>
          </FormItem>
          <hr style="height:1px;border:none;border-top:1px dashed #dddee1;"/>
          <br>
          <FormItem label="数据查询权限:">
            <p v-if="perList.permissions.query === '0'">否</p>
            <p v-else>是</p>
          </FormItem>
          <template v-if="perList.permissions.query === '1'">
            <FormItem label="连接名:">
              <Tag color="blue" v-for="i in perList.permissions.querycon" :key="i">{{i}}</Tag>
            </FormItem>
          </template>
          <hr style="height:1px;border:none;border-top:1px dashed #dddee1;"/>
        </template>
        <hr style="height:1px;border:none;border-top:1px dashed #dddee1;"/>
        <br>
        <FormItem label="用户管理权限:">
          <p v-if="perList.permissions.user === '0'">否</p>
          <p v-else>是</p>
        </FormItem>
        <hr style="height:1px;border:none;border-top:1px dashed #dddee1;"/>
        <br>
        <FormItem label="数据库管理权限:">
          <p v-if="perList.permissions.base === '0'">否</p>
          <p v-else>是</p>
        </FormItem>
      </Form>
      <div slot="footer">
        <Button type="primary" @click="editInfodModal=false">取消</Button>
        <Button type="error" @click="reject" v-if="perList.status === 2">驳回</Button>
        <Button type="success" @click="savedata" v-if="perList.status === 2">同意</Button>
      </div>
    </Modal>

  </div>
</template>

<script>
  import axios from 'axios'

  export default {
    name: 'permissions',
    data () {
      return {
        permissondata: [],
        permissoncolums: [
          {
            type: 'selection',
            width: 60,
            align: 'center'
          },
          {
            title: '申请编号',
            key: 'work_id'
          },
          {
            title: '申请人',
            key: 'username'
          },
          {
            title: '真实姓名',
            key: 'real_name'
          },
          {
            title: '状态',
            key: 'status',
            width: 150,
            render: (h, params) => {
              const row = params.row
              let color = ''
              let text = ''
              if (row.status === 2) {
                color = 'primary'
                text = '待审核'
              } else if (row.status === 0) {
                color = 'error'
                text = '驳回'
              } else if (row.status === 1) {
                color = 'success'
                text = '已执行'
              } else {
                color = 'warning'
                text = '执行中'
              }
              return h('Tag', {
                props: {
                  type: 'dot',
                  color: color
                }
              }, text)
            },
            sortable: true,
            filters: [{
              label: '已执行',
              value: 1
            },
              {
                label: '驳回',
                value: 0
              },
              {
                label: '待审核',
                value: 2
              },
              {
                label: '执行中',
                value: 3
              }
            ],
            //            filterMultiple: false 禁止多选,
            filterMethod (value, row) {
              if (value === 1) {
                return row.status === 1
              } else if (value === 2) {
                return row.status === 2
              } else if (value === 0) {
                return row.status === 0
              } else if (value === 3) {
                return row.status === 3
              }
            }
          },
          {
            title: '操作',
            key: 'action',
            width: 200,
            align: 'center',
            render: (h, params) => {
              return h('div', [
                h('Button', {
                  props: {
                    size: 'small',
                    type: 'text'
                  },
                  on: {
                    click: () => {
                      this.modalinfo(params.row)
                    }
                  }
                }, '查看')
              ])
            }
          }
        ],
        per_pn: 1,
        delrecord: Object,
        editInfodModal: false,
        perList: {
          permissions: Object,
          username: String,
          work_id: String,
          auth_group: String,
          status: Number
        }
      }
    },
    methods: {
      permisson_list (vl = 1) {
        axios.get(`${this.$config.url}/audit_grained/all/?page=${vl}`)
          .then(res => {
            this.permissondata = res.data['data']
            this.per_pn = res.data['pn']
          })
          .catch(error => {
            this.$config.err_notice(error)
          })
      },
      delrecordData () {
        axios.put(`${this.$config.url}/audit_grained/`, {'work_id': JSON.stringify(this.delrecord)})
          .then(res => {
            this.$config.notice(res.data)
            this.permisson_list()
          })
          .catch(error => {
            this.$config.err_notice(error)
          })
      },
      delrecordList (vl) {
        this.delrecord = vl.map(vl => vl.work_id)
      },
      modalinfo (vl) {
        this.editInfodModal = true
        this.perList = vl
      },
      savedata () {
        axios.post(`${this.$config.url}/audit_grained/`,
          {
            'status': 0,
            'user': this.perList.username,
            'work_id': this.perList.work_id,
            'auth_group': this.perList.auth_group
          })
          .then(res => {
            this.$config.notice(res.data)
            this.editInfodModal = false
            this.permisson_list()
          })
          .catch(error => {
            this.$config.err_notice(error)
          })
      },
      reject () {
        axios.post(`${this.$config.url}/audit_grained/`,
          {
            'status': 1,
            'user': this.perList.username,
            'work_id': this.perList.work_id
          })
          .then(res => {
            this.$config.notice(res.data)
            this.editInfodModal = false
            this.permisson_list()
          })
          .catch(error => {
            this.$config.err_notice(error)
          })
      }
    },
    mounted () {
      this.permisson_list()
    }
  }
</script>

<style scoped>
</style>
