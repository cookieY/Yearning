<style lang="less">
@import '../../styles/common.less';
@import '../Order/components/table.less';
</style>
<template>
<div>
  <Col span="6">
  <Card>
    <p slot="title">
      <Icon type="load-b"></Icon>
      添加用户
    </p>
    <div class="edittable-testauto-con">
      <Form :model="userinfo" :label-width="80" ref="userinfova" :rules="userinfoValidate">
        <FormItem label="用户名" prop="username">
          <Input v-model="userinfo.username" placeholder="请输入"></Input>
        </FormItem>
        <FormItem label="密码" prop="password">
          <Input v-model="userinfo.password" placeholder="请输入" type="password"></Input>
        </FormItem>
        <FormItem label="确认密码" prop="confirmpassword">
          <Input v-model="userinfo.confirmpassword" placeholder="请输入" type="password"></Input>
        </FormItem>
        <FormItem label="部门" prop="department">
          <Input v-model="userinfo.department" placeholder="请输入"></Input>
        </FormItem>
        <FormItem label="权限" prop="group">
          <Select v-model="userinfo.group" placeholder="请选择">
              <Option value="admin">管理员</Option>
              <Option value="guest">使用者</Option>
            </Select>
        </FormItem>
        <Button type="primary" @click.native="Registered" style="margin-left: 35%">注册</Button>
      </Form>
    </div>
  </Card>
  </Col>
  <Col span="18" class="padding-left-10">
  <Card>
    <p slot="title">
      <Icon type="ios-crop-strong"></Icon>
      系统用户表
    </p>
    <div class="edittable-con-1">
      <Table border :columns="columns6" :data="data5" stripe height="550"></Table>
    </div>
    <br>
    <Page :total="pagenumber" show-elevator @on-change="splicpage" :page-size="10"></Page>
  </Card>
  </Col>

  <Modal v-model="editPasswordModal" :closable='false' :mask-closable=false :width="500">
    <h3 slot="header" style="color:#2D8CF0">修改用户密码</h3>
    <Form ref="editPasswordForm" :model="editPasswordForm" :label-width="100" label-position="right" :rules="passwordValidate">
      <FormItem label="用户名">
        <Input v-model="username" readonly="readonly"></Input>
      </FormItem>
      <FormItem label="原密码" prop="oldPass">
        <Input v-model="editPasswordForm.oldPass" placeholder="请输入现在使用的密码"></Input>
      </FormItem>
      <FormItem label="新密码" prop="newPass">
        <Input v-model="editPasswordForm.newPass" placeholder="请输入新密码，至少6位字符"></Input>
      </FormItem>
      <FormItem label="确认新密码" prop="rePass">
        <Input v-model="editPasswordForm.rePass" placeholder="请再次输入新密码"></Input>
      </FormItem>
    </Form>
    <div slot="footer">
      <Button type="text" @click="cancelEditPass">取消</Button>
      <Button type="primary" @click="saveEditPass" :loading="savePassLoading">保存</Button>
    </div>
  </Modal>

  <Modal v-model="editInfodModal" :closable='false' :mask-closable=false :width="500">
    <h3 slot="header" style="color:#2D8CF0">修改用户信息</h3>
    <Form :model="editInfodForm" :label-width="100" label-position="right">
      <FormItem label="用户名">
        <Input v-model="username" readonly="readonly"></Input>
      </FormItem>
      <FormItem label="权限">
        <Select v-model="editInfodForm.group" placeholder="请选择">
            <Option value="admin">管理员</Option>
            <Option value="guest">使用者</Option>
          </Select>
      </FormItem>
      <FormItem label="部门">
        <Input v-model="editInfodForm.department" placeholder="请输入新部门"></Input>
      </FormItem>
    </Form>
    <div slot="footer">
      <Button type="text" @click="cancelEditInfo">取消</Button>
      <Button type="primary" @click="saveEditInfo">保存</Button>
    </div>
  </Modal>

  <Modal v-model="deluserModal" :closable='false' :mask-closable=false :width="500">
    <h3 slot="header" style="color:#2D8CF0">删除用户</h3>
    <Form :label-width="100" label-position="right">
      <FormItem label="用户名">
        <Input v-model="username" readonly="readonly"></Input>
      </FormItem>
      <FormItem label="请输入用户名">
        <Input v-model="confirmuser" placeholder="请确认用户名"></Input>
      </FormItem>
    </Form>
    <div slot="footer">
      <Button type="text" @click="cancelDelInfo">取消</Button>
      <Button type="warning" @click="delUser">删除</Button>
    </div>
  </Modal>
</div>
</template>
<script>
import axios from 'axios'
import '../../assets/tablesmargintop.css'
import util from '../../libs/util'
export default {
  data () {
    const valideRePassword = (rule, value, callback) => { // eslint-disable-line no-unused-vars
      if (value !== this.editPasswordForm.newPass) {
        callback(new Error('两次输入密码不一致'));
      } else {
        callback();
      }
    };
    const valideuserinfoPassword = (rule, value, callback) => {
      if (value !== this.userinfo.password) {
        callback(new Error('两次输入密码不一致'));
      } else {
        callback();
      }
    };
    return {
      columns6: [{
          title: '用户名',
          key: 'username',
          sortable: true
        },
        {
          title: '权限',
          key: 'group',
          sortable: true
        },
        {
          title: '部门',
          key: 'department',
          sortable: true
        },
        {
          title: '操作',
          key: 'action',
          width: 400,
          align: 'center',
          render: (h, params) => {
            return h('div', [
              h('Button', {
                props: {
                  type: 'primary',
                  size: 'small'
                },
                style: {
                  marginRight: '5px'
                },
                on: {
                  click: () => {
                    this.edituser(params.index)
                  }
                }
              }, '更改密码'),
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
                    this.editgroup(params.index)
                  }
                }
              }, '更改所属组'),
              h('Button', {
                props: {
                  type: 'warning',
                  size: 'small'
                },
                on: {
                  click: () => {
                    this.deleteUser(params.index)
                  }
                }
              }, '删除')
            ])
          }
        }
      ],
      data5: [],
      pagenumber: 1,
      // 新建用户
      userinfo: {
        username: '',
        password: '',
        confirmpassword: '',
        group: '',
        checkbox: '',
        department: ''
      },
      userinfoValidate: {
        username: [{
          required: true,
          message: '请输入用户名',
          trigger: 'blur'
        }],
        password: [{
            required: true,
            message: '请输入密码',
            trigger: 'blur'
          },
          {
            min: 6,
            message: '请至少输入6个字符',
            trigger: 'blur'
          },
          {
            max: 32,
            message: '最多输入32个字符',
            trigger: 'blur'
          }
        ],
        confirmpassword: [{
            required: true,
            message: '请再次输入新密码',
            trigger: 'blur'
          },
          {
            validator: valideuserinfoPassword,
            trigger: 'blur'
          }
        ],
        group: [{
          required: true,
          message: '请输入权限',
          trigger: 'blur'
        }],
        department: [{
          required: true,
          message: '请输入部门名称',
          trigger: 'blur'
        }]
      },
      // 更改密码遮罩层状态
      editPasswordModal: false,
      // 更改密码
      editPasswordForm: {
        oldPass: '',
        newPass: '',
        rePass: ''
      },
      // 保存更改密码loding按钮状态
      savePassLoading: false,
      // 更改密码表单验证规则
      passwordValidate: {
        oldPass: [{
          required: true,
          message: '请输入原密码',
          trigger: 'blur'
        }],
        newPass: [{
            required: true,
            message: '请输入新密码',
            trigger: 'blur'
          },
          {
            min: 6,
            message: '请至少输入6个字符',
            trigger: 'blur'
          },
          {
            max: 32,
            message: '最多输入32个字符',
            trigger: 'blur'
          }
        ],
        rePass: [{
            required: true,
            message: '请再次输入新密码',
            trigger: 'blur'
          },
          {
            validator: valideRePassword,
            trigger: 'blur'
          }
        ]
      },
      // 更改部门及权限
      editInfodForm: {
        group: '',
        department: ''
      },
      // 更改部门及权限遮罩层状态
      editInfodModal: false,
      // 用户名
      username: '',
      confirmuser: '',
      deluserModal: false
    }
  },
  methods: {
    edituser (index) {
      this.editPasswordModal = true
      this.username = this.data5[index].username
    },
    editgroup (index) {
      this.editInfodModal = true
      this.username = this.data5[index].username
      this.editInfodForm.department = this.data5[index].department
      this.editInfodForm.group = this.data5[index].group
    },
    deleteUser (index) {
      this.deluserModal = true
      this.username = this.data5[index].username
    },
    Registered () {
      this.$refs['userinfova'].validate((valid) => {
        if (valid) {
          axios.post(util.url + '/userinfo/', {
              'username': this.userinfo.username,
              'password': this.userinfo.password,
              'group': this.userinfo.group,
              'department': this.userinfo.department
            })
            .then(res => {
              this.$Notice.success({
                title: res.data
              })
              this.refreshuser()
              this.userinfo = {}
            })
            .catch(() => {
              this.$Notice.error({
                title: '警告',
                desc: '用户名已注册过,请更换其他用户名注册！'
              })
            })
        }
      })
    },
    refreshuser (vl = 1) {
      axios.get(`${util.url}/userinfo/all?page=${vl}`)
        .then(res => {
          this.data5 = res.data.data
          this.pagenumber = parseInt(res.data.page.alter_number)
        })
        .catch(error => {
          util.ajanxerrorcode(this, error)
        })
    },
    splicpage (page) {
      this.refreshuser(page)
    },
    cancelEditPass () {
      this.editPasswordForm = {}
      this.editPasswordModal = false
    },
    cancelEditInfo () {
      this.editInfodModal = false;
      this.editInfodForm = {}
    },
    cancelDelInfo () {
      this.deluserModal = false;
      this.confirmuser = ''
    },
    saveEditPass () {
      this.$refs['editPasswordForm'].validate((valid) => {
        if (valid) {
          this.savePassLoading = true;
          axios.put(util.url + '/userinfo/changepwd', {
              'username': this.username,
              'new': this.editPasswordForm.newPass,
              'old': this.editPasswordForm.oldPass
            })
            .then(res => {
              this.$Notice.success({
                title: '通知',
                desc: res.data
              })
              this.editPasswordModal = false;
            })
            .catch(error => {
              util.ajanxerrorcode(this, error)
            })
          this.savePassLoading = false
        }
      });
    },
    saveEditInfo () {
      axios.put(util.url + '/userinfo/changegroup', {
          'username': this.username,
          'group': this.editInfodForm.group,
          'department': this.editInfodForm.department
        })
        .then(res => {
          this.$Notice.success({
            title: '通知',
            desc: res.data
          })
          this.refreshuser()
        })
        .catch(error => {
          util.ajanxerrorcode(this, error)
        })
      this.editInfodModal = false
    },
    delUser () {
      if (this.username === this.confirmuser) {
        axios.delete(util.url + '/userinfo/' + this.username)
          .then(res => {
            this.$Notice.success({
              title: '通知',
              desc: res.data
            })
            this.deluserModal = false
            this.refreshuser()
          })
          .catch(error => {
            util.ajanxerrorcode(this, error)
          })
      } else {
        this.$Message.error('用户名不一致!请重新操作!')
      }
    }
  },
  mounted () {
    this.refreshuser()
  }
}
</script>
<!-- reder put request  render_group put request  remove delete request-->
