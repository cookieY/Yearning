<style lang="less">
@import '../../styles/common.less';
@import '../Order/components/table.less';
.demo-spin-icon-load {
    animation: ani-demo-spin 1s linear infinite;
}
</style>
<template>
<div>
  <Col span="6">
  <Card>
    <p slot="title">
      <Icon type="load-b"></Icon>
      添加主机
    </p>
    <div class="edittable-testauto-con">
      <Form ref="formValidate" :model="formItem" :label-width="100" :rules="ruleInline">
        <Form-item label="机房:">
          <Select v-model="formItem.add" placeholder="请选择">
            <Option v-for="list in dataset" :value="list" :key="list">{{ list }}</Option>
          </Select>
        </Form-item>
        <Form-item label="连接名称:" prop="name">
          <Input v-model="formItem.name" placeholder="请输入"></Input>
        </Form-item>
        <Form-item label="ssh地址:" prop="ip">
          <Input v-model="formItem.ip" placeholder="请输入"></Input>
        </Form-item>
        <Form-item label="端口:" prop="port">
          <Input v-model="formItem.port" placeholder="请输入"></Input>
        </Form-item>
        <Form-item label="用户名:" prop="username">
          <Input v-model="formItem.username" placeholder="请输入"></Input>
        </Form-item>
        <Form-item label="密码:" prop="password">
          <Input v-model="formItem.password" placeholder="请输入" type="password"></Input>
        </Form-item>
        <Form-item label="keyfile:" prop="ssh_key_file">
          <Input v-model="formItem.ssh_key_file" placeholder="请输入" ></Input>
        </Form-item>
        <Button type="info" @click="testlink()">测试连接</Button>
        <Button type="success" @click="add()" style="margin-left: 5%">确定</Button>
        <Button type="warning" @click="del()" style="margin-left: 5%">取消</Button>
      </Form>
    </div>
  </Card>
  </Col>
  <Col span="18" class="padding-left-10">
  <Card>
    <p slot="title">
      <Icon type="ios-crop-strong"></Icon>
      ssh详情表
    </p>
    <div class="edittable-con-1">
      <Table :columns="columns" :data="rowdata" height="550"></Table>
    </div>
    <br>
    <Page :total="pagenumber" show-elevator @on-change="mountdata" :page-size="10"></Page>
  </Card>
  </Col>
  <Modal v-model="delbaseModal" :width="500">
    <h3 slot="header" style="color:#2D8CF0">删除ssh</h3>
    <Form :label-width="100" label-position="right">
      <FormItem label="ssh连接名">
        <Input v-model="delbasename" readonly="readonly"></Input>
      </FormItem>
      <FormItem label="请输入ssh连接名">
        <Input v-model="delconfirmbasename" placeholder="请确认ssh连接名"></Input>
      </FormItem>
    </Form>
    <div slot="footer">
      <Button type="text" @click="delbaseModal = false">取消</Button>
      <Button type="primary" @click="delbaselink">删除</Button>
    </div>
  </Modal>
  
</div>
</template>
<script>
import '../../assets/tablesmargintop.css'
import axios from 'axios'
import util from '../../libs/util'
import ICol from '../../../node_modules/iview/src/components/grid/col';

export default {
  components: {
    ICol
  },
  name: 'sshlist',
  data () {
    return {
      columns: [
        {
          title: '连接名称',
          key: 'connection_name'
        },
        {
          title: 'ssh地址',
          key: 'ip'
        },
        {
          title: '机房',
          key: 'computer_room'
        },
        {
          title: '操作',
          key: 'action',
          width: 300,
          render: (h, params) => {
            return h('div', [
              h('Button', {
                props: {
                  size: 'small',
                  type: 'info'
                },
                on: {
                  click: () => {
                    this.edit_tab(params.index)
                  }
                }
              }, '查看信息'),
              h('Button', {
                style: {
                  marginLeft: '8px'
                },
                props: {
                  type: 'warning',
                  size: 'small'
                },
                on: {
                  click: () => {
                    this.delssh(params.index)
                  }
                }
              }, '删除')
            ])
          }
        }
      ],
      rowdata: [],
      modal: false,
      // 添加数据库信息
      formItem: {
        name: '',
        ip: '',
        add: '',
        username: '',
        password: '',
        port: '',
        ssh_key_file: ''
      },
      // 添加表单验证规则
      ruleInline: {
        name: [{
          required: true,
          message: '请填写连接名称',
          trigger: 'blur'
        }],
        ip: [{
          required: true,
          message: '请填写连接地址',
          trigger: 'blur'
        }],
        username: [{
          required: true,
          message: '请填写用户名',
          trigger: 'blur'
        }],
        port: [{
          required: true,
          message: '请填写端口',
          trigger: 'blur'
        }],
        password: [{
          required: true,
          message: '请填写密码',
          trigger: 'blur'
        }],
        ssh_key_file: [{
          required: false,
          message: '请填写sshkey',
          trigger: 'blur'
        }]
      },
      // 生成字典规则
      dataset: util.computer_room,
      Generate: {
        textarea: '',
        add: '',
        name: ''
      },
      dictionary: {
        name: '',
        add: '',
        databases: [],
        databasesList: [],
        indeterminate: false,
        checkAll: false,
        getdellist: [],
        getdel: [],
        delname: ''
      },
      delsshModal: false,
      delsshname: '',
      delconfirmsshname: '',
      pagenumber: 1
     }
  },
  methods: {
    del () {
      this.modal = false
      this.formItem = {}
    },
    testlink () {
      axios.put(util.url + '/management_ssh/', {
          'ip': this.formItem.ip,
          'user': this.formItem.username,
          'password': this.formItem.password,
          'port': this.formItem.port,
          'ssh_key_file': this.formItem.ssh_key_file
        })
        .then(res => {
          this.$Notice.success({
            title: '通知',
            desc: res.data
          })
        })
        .catch(error => {
          util.ajanxerrorcode(this, error)
        })
    },
    add () {
      for (let i of this.rowdata) {
        if (i.connection_name === this.formItem.name) {
          this.$Notice.error({
            title: '警告',
            desc: '连接名称重复,请更改为其他！'
          })
          return
        }
      }
      this.$refs['formValidate'].validate((valid) => {
        if (valid) {
          let data = {
            'connection_name': this.formItem.name,
            'ip': this.formItem.ip,
            'computer_room': this.formItem.add,
            'username': this.formItem.username,
            'password': this.formItem.password,
            'port': this.formItem.port,
            'ssh_key_file': this.formItem.ssh_key_file
          }
          axios.post(util.url + '/management_ssh/', {
              'data': JSON.stringify(data)
            })
            .then(() => {
              this.$Notice.success({
                title: '通知',
                desc: '数据库信息添加成功!'
              })
              this.mountdata()
            })
            .catch(error => {
              this.$Notice.error({
                title: '警告',
                desc: error
              })
            })
          this.del()
        }
      })
    },
    edit_tab (index) {
      this.$Modal.info({
        title: 'ssh连接信息',
        content: `机房: ${this.rowdata[index].computer_room}<br> 连接名称：${this.rowdata[index].connection_name}<br>
                  ssh地址：${this.rowdata[index].ip}<br>端口: ${this.rowdata[index].port}<br>用户名: ${this.rowdata[index].username}`
      })
    },
    // 删除ssh
    delssh (index) {
      this.delsshModal = true
      this.delsshname = this.rowdata[index].connection_name
    },

    // 重置
    cleardata () {
      this.dictionary.name = ''
      this.dictionary.databases = []
      this.dictionary.databasesList = []
      this.dictionary.getdellist = []
      this.dictionary.getdel = []
      this.dictionary.delname = ''
    },
    delsshlink () {
      if (this.delsshname === this.delconfirmsshname) {
        axios.delete(`${util.url}/management_ssh?del=${this.delsshname}`)
          .then(res => {
            this.$Notice.success({
              title: '通知',
              desc: res.data
            })
            this.delbaseModal = false
            this.delconfirmbasename = ''
            this.mountdata()
          })
          .catch(error => {
            util.ajanxerrorcode(this, error)
          })
      } else {
        this.$Message.error({
          content: '请确认输入的连接名称一致！'
        })
      }
    },
    mountdata (vl = 1) {
      axios.get(`${util.url}/management_ssh?page=${vl}&permissions_type=base`)
        .then(res => {
          this.rowdata = res.data.data
          this.pagenumber = parseInt(res.data.page.alter_number)
          this.diclist = res.data.diclist
          this.mail_switch = res.data.mail_switch
          this.dingding_switch = res.data.ding_switch
        })
        .catch(error => {
          util.ajanxerrorcode(this, error)
        })
    }
  },
  mounted () {
    this.mountdata()
  }
}
</script>
