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
        <FormItem label="电子邮箱">
          <Input v-model="userinfo.email" placeholder="请输入"></Input>
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

  <Modal v-model="editInfodModal"  :width="900">
    <h3 slot="header" style="color:#2D8CF0">权限设定</h3>
    <Form :model="editInfodForm" :label-width="100" label-position="right">
      <FormItem label="用户名">
        <Input v-model="username" readonly="readonly"></Input>
      </FormItem>
      <FormItem label="权限">
        <Select v-model="editInfodForm.group" placeholder="请选择">
            <Option value="admin">管理员</Option>
            <Option value="guest" v-if="this.userid !== 1">使用者</Option>
          </Select>
      </FormItem>
      <FormItem label="部门">
        <Input v-model="editInfodForm.department" placeholder="请输入新部门"></Input>
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
              @click.prevent.native="ddlCheckAll('ddlcon', 'ddl', 'connection')">全选</Checkbox>
          </div>
          <CheckboxGroup v-model="permission.ddlcon">
            <Checkbox  v-for="i in connectionList.connection" :label="i.connection_name" :key="i.connection_name">{{i.connection_name}}</Checkbox>
          </CheckboxGroup>
        </FormItem>
        </template>
        <hr style="height:1px;border:none;border-top:1px dashed #dddee1;" />
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
                @click.prevent.native="ddlCheckAll('dmlcon', 'dml', 'connection')">全选</Checkbox>
            </div>
            <CheckboxGroup v-model="permission.dmlcon">
              <Checkbox  v-for="i in connectionList.connection" :label="i.connection_name" :key="i.connection_name">{{i.connection_name}}</Checkbox>
            </CheckboxGroup>
          </FormItem>
      </template>
        <hr style="height:1px;border:none;border-top:1px dashed #dddee1;" />
        <br>
        <FormItem label="选择上级审核人:">
          <div style="border-bottom: 1px solid #e9e9e9;padding-bottom:6px;margin-bottom:6px;">
            <Checkbox
              :indeterminate="indeterminate.person"
              :value="checkAll.person"
              @click.prevent.native="ddlCheckAll('person', 'person', 'person')">全选</Checkbox>
          </div>
          <CheckboxGroup v-model="permission.person">
            <Checkbox  v-for="i in connectionList.person" :label="i.username" :key="i.username">{{i.username}}</Checkbox>
          </CheckboxGroup>
        </FormItem>
        <hr style="height:1px;border:none;border-top:1px dashed #dddee1;" />
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
              @click.prevent.native="ddlCheckAll('diccon', 'dic', 'dic')">全选</Checkbox>
          </div>
          <CheckboxGroup v-model="permission.diccon">
            <Checkbox  v-for="i in connectionList.dic" :label="i.Name" :key="i.Name">{{i.Name}}</Checkbox>
          </CheckboxGroup>
        </FormItem>
      </template>
        <hr style="height:1px;border:none;border-top:1px dashed #dddee1;" />
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
              @click.prevent.native="ddlCheckAll('querycon', 'query', 'connection')">全选</Checkbox>
          </div>
          <CheckboxGroup v-model="permission.querycon">
            <Checkbox  v-for="i in connectionList.connection" :label="i.connection_name" :key="i.connection_name">{{i.connection_name}}</Checkbox>
          </CheckboxGroup>
        </FormItem>
      </template>
      </template>
      <template v-if="this.editInfodForm.group === 'admin'">
        <hr style="height:1px;border:none;border-top:1px dashed #dddee1;" />
        <br>
        <FormItem label="用户管理权限:">
          <RadioGroup v-model="permission.user">
            <Radio label="1">是</Radio>
            <Radio label="0">否</Radio>
          </RadioGroup>
        </FormItem>
        <hr style="height:1px;border:none;border-top:1px dashed #dddee1;" />
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

  <Modal v-model="editemail" :closable='false' :mask-closable=false :width="500">
    <h3 slot="header" style="color:#2D8CF0">更改email邮箱</h3>
    <Form :label-width="100" label-position="right">
      <FormItem label="E-mail">
        <Input v-model="email"></Input>
      </FormItem>
    </Form>
    <div slot="footer">
      <Button type="text" @click="editemail=false">取消</Button>
      <Button type="warning" @click="putemail">更改</Button>
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
      percent: 0,
      permission: {
        ddl: '0',
        ddlcon: [],
        dml: '0',
        dmlcon: [],
        dic: '0',
        diccon: [],
        dicedit: '0',
        dicexport: '0',
        index: '0',
        indexcon: [],
        query: '0',
        querycon: [],
        user: '0',
        base: '0'
      },
      con: [],
      columns6: [
        {
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
          title: 'email',
          key: 'email',
          sortable: true
        },
        {
          title: '操作',
          key: 'action',
          width: 400,
          align: 'center',
          render: (h, params) => {
            if (params.row.id !== 1) {
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
                }, '权限'),
                h('Button', {
                  props: {
                    type: 'success',
                    size: 'small'
                  },
                  style: {
                    marginRight: '5px'
                  },
                  on: {
                    click: () => {
                      this.editEmail(params.index)
                    }
                  }
                }, 'email更改'),
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
            } else {
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
                      this.editgroup(params.index)
                    }
                  }
                }, '权限'),
                h('Button', {
                  props: {
                    type: 'success',
                    size: 'small'
                  },
                  style: {
                    marginRight: '5px'
                  },
                  on: {
                    click: () => {
                      this.editEmail(params.index)
                    }
                  }
                }, 'email更改')
              ])
            }
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
        department: '',
        email: ''
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
      editemail: false,
      email: '',
      // 用户名
      username: '',
      confirmuser: '',
      deluserModal: false,
      userid: null,
      dicadd: [],
      checkAll: {
        ddl: false,
        dml: false,
        query: false,
        dic: false,
        person: false
      },
      indeterminate: {
        ddl: true,
        dml: true,
        query: true,
        dic: true,
        person: true
      },
      connectionList: {
        connection: [],
        dic: [],
        person: []
      }
    }
  },
  methods: {
    edituser (index) {
      this.editPasswordModal = true
      this.username = this.data5[index].username
    },
    editgroup (index) {
      this.editInfodModal = true
      this.userid = this.data5[index].id
      this.username = this.data5[index].username
      this.editInfodForm.department = this.data5[index].department
      this.editInfodForm.group = this.data5[index].group
      axios.get(`${util.url}/userinfo/permissions?user=${this.username}`)
        .then(res => {
          this.permission = res.data
        })
    },
    deleteUser (index) {
      this.deluserModal = true
      this.username = this.data5[index].username
    },
    editEmail (index) {
      this.editemail = true
      this.username = this.data5[index].username
      this.email = this.data5[index].email
    },
    putemail () {
      axios.put(`${util.url}/userinfo/changemail`, {
        'username': this.username,
        'mail': this.email
      })
        .then(res => {
          this.$Notice.success({
            title: res.data
          })
          this.editemail = false
          this.refreshuser()
        })
        .catch(error => {
          util.ajanxerrorcode(this, error)
        })
    },
    Registered () {
      this.$refs['userinfova'].validate((valid) => {
        if (valid) {
          axios.post(util.url + '/userinfo/', {
              'username': this.userinfo.username,
              'password': this.userinfo.password,
              'group': this.userinfo.group,
              'department': this.userinfo.department,
              'email': this.userinfo.email
            })
            .then(res => {
              this.$Notice.success({
                title: res.data
              })
              this.refreshuser()
              this.userinfo = {
                username: '',
                password: '',
                confirmpassword: '',
                group: '',
                checkbox: '',
                department: '',
                email: ''}
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
          'department': this.editInfodForm.department,
          'permission': JSON.stringify(this.permission)
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
    },
    ddlCheckAll (name, indeterminate, ty) {
      if (this.indeterminate[indeterminate]) {
        this.checkAll[indeterminate] = false;
      } else {
        this.checkAll[indeterminate] = !this.checkAll[indeterminate];
      }
      this.indeterminate[indeterminate] = false;

      if (this.checkAll[indeterminate]) {
        if (ty === 'dic') {
          this.permission[name] = this.connectionList[ty].map(vl => vl.Name)
        } else if (ty === 'person') {
          this.permission[name] = this.connectionList[ty].map(vl => vl.username)
        } else {
          this.permission[name] = this.connectionList[ty].map(vl => vl.connection_name)
        }
      } else {
        this.permission[name] = [];
      }
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
        util.ajanxerrorcode(this, error)
      })
    this.refreshuser()
  }
}
</script>
<!-- reder put request  render_group put request  remove delete request-->
